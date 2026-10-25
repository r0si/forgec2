package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func seedImplantForBatch(t *testing.T, s *Server, id string) {
	t.Helper()
	if err := s.db.Create(&db.Implant{ID: id, Hostname: "H", IP: "10.0.0.1", LastSeen: time.Now()}).Error; err != nil {
		t.Fatalf("seed implant: %v", err)
	}
}

func batchCommandRequest(t *testing.T, s *Server, taskType, command, operator string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"agent_ids": []string{"batch-agent"},
		"task_type": taskType,
		"command":   command,
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/batch", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_role", "admin")
	c.Set("user", operator)
	s.handleBatchCommand(c)
	return w
}

// Batch dangerous tasks must honor the two-man rule exactly like the single
// task path: with RequireApproval on, they start in pending_approval and are
// not claimable by the beacon until approved (regression for the batch bypass).
func TestBatchCommandDangerousTaskHonorsApproval(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = true
	seedImplantForBatch(t, s, "batch-agent")

	w := batchCommandRequest(t, s, "uninstall", "self delete", "alice")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}

	var stored db.Task
	if err := s.db.Where("agent_id = ?", "batch-agent").First(&stored).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != TaskStatusPendingApproval {
		t.Fatalf("batch dangerous task must start pending_approval, got %q", stored.Status)
	}
	if stored.CreatedBy != "alice" {
		t.Fatalf("expected CreatedBy=alice, got %q", stored.CreatedBy)
	}
	if claimed := s.fetchPendingTasks("batch-agent"); len(claimed) != 0 {
		t.Fatalf("pending_approval batch task must not be claimable, got %d", len(claimed))
	}
}

func TestBatchCommandNonDangerousTaskPendingWhenApproval(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = true
	seedImplantForBatch(t, s, "batch-agent")

	w := batchCommandRequest(t, s, "shell", "whoami", "alice")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var stored db.Task
	if err := s.db.Where("agent_id = ?", "batch-agent").First(&stored).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != "pending" {
		t.Fatalf("non-dangerous batch task should stay pending, got %q", stored.Status)
	}
}

func TestBatchCommandDangerousTaskPendingWhenApprovalOff(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = false
	seedImplantForBatch(t, s, "batch-agent")

	w := batchCommandRequest(t, s, "uninstall", "self delete", "alice")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var stored db.Task
	if err := s.db.Where("agent_id = ?", "batch-agent").First(&stored).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != "pending" {
		t.Fatalf("with approval off, dangerous batch task should be pending, got %q", stored.Status)
	}
}

// TestBatchCommandIdempotencyKeyDedupesRetry proves a retried batch with the
// same key is a per-agent no-op instead of duplicating work on every agent.
func TestBatchCommandIdempotencyKeyDedupesRetry(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	seedImplantForBatch(t, s, "batch-agent")
	seedImplantForBatch(t, s, "batch-agent-2")

	send := func() map[string]interface{} {
		body, _ := json.Marshal(map[string]interface{}{
			"agent_ids":       []string{"batch-agent", "batch-agent-2"},
			"task_type":       "shell",
			"command":         "whoami",
			"idempotency_key": "batch-k1",
		})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPost, "/batch", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user_role", "admin")
		c.Set("user", "alice")
		s.handleBatchCommand(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var out map[string]interface{}
		unmarshalData(t, w.Body.Bytes(), &out)
		return out
	}

	first := send()
	if first["tasks_created"] != float64(2) {
		t.Fatalf("first batch created=%v, want 2", first["tasks_created"])
	}
	second := send()
	if second["tasks_created"] != float64(0) {
		t.Fatalf("retry created=%v, want 0", second["tasks_created"])
	}
	if second["deduped"] != float64(2) {
		t.Fatalf("retry deduped=%v, want 2", second["deduped"])
	}
	var count int64
	s.db.Model(&db.Task{}).Where("idempotency_key = ?", "batch-k1").Count(&count)
	if count != 2 {
		t.Fatalf("stored keyed rows=%d, want 2", count)
	}
}
