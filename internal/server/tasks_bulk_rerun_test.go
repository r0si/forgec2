package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBulkRerunFailedTasks covers the bulk form of rerun: recovering from an outage that failed
// everything queued against one beacon used to mean clicking each task.
func TestBulkRerunFailedTasks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	// seedAgent (the shared helper) creates an unowned-row implant; give each one its tenant here so
	// the tenant scoping under test has something to compare against.
	if err := s.db.Create(&db.Implant{ID: "a-1", TenantID: 1}).Error; err != nil {
		t.Fatalf("seed a-1: %v", err)
	}
	if err := s.db.Create(&db.Implant{ID: "b-2", TenantID: 2}).Error; err != nil {
		t.Fatalf("seed b-2: %v", err)
	}

	now := time.Now()
	seed := func(agentID string, tenant uint, taskType, status string, age time.Duration) db.Task {
		task := db.Task{
			AgentID: agentID, TenantID: tenant, Type: taskType, Command: "whoami",
			Shell: "cmd.exe", Status: status, CreatedAt: now.Add(-age),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		return task
	}

	f1 := seed("a-1", 1, "shell", "failed", time.Hour)
	f2 := seed("a-1", 1, "shell", "failed", 2*time.Hour)
	seed("a-1", 1, "screenshot", "failed", 3*time.Hour)
	// Not rerun: successful, control-type, another tenant's, and outside the window.
	seed("a-1", 1, "shell", "completed", time.Hour)
	seed("a-1", 1, "kill_agent", "failed", 30*time.Minute)
	seed("b-2", 2, "shell", "failed", time.Hour)
	seed("a-1", 1, "shell", "failed", 72*time.Hour)

	call := func(query string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/a-1/tasks/rerun-failed"+query, nil)
		c.Params = gin.Params{{Key: "id", Value: "a-1"}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleRerunFailedTasks(c)
		return w
	}

	w := call("", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Queued    int    `json:"queued"`
			TaskIDs   []uint `json:"task_ids"`
			Truncated bool   `json:"truncated"`
			Excluded  int64  `json:"excluded"`
			Skipped   []struct {
				ID     uint   `json:"id"`
				Reason string `json:"reason"`
			} `json:"skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// Three eligible tasks in the 24h window: the two shells plus the screenshot.
	if resp.Data.Queued != 3 {
		t.Fatalf("queued = %d, want 3 (%s)", resp.Data.Queued, w.Body.String())
	}
	// The control task is excluded from the candidates but REPORTED, so the caller is not left
	// wondering why fewer tasks ran than failed.
	if resp.Data.Excluded != 1 {
		t.Fatalf("excluded = %d, want 1 (the kill_agent failure): %s", resp.Data.Excluded, w.Body.String())
	}

	// Every queued id exists and is a fresh pending task with the original's parameters.
	for _, id := range resp.Data.TaskIDs {
		var created db.Task
		if err := s.db.First(&created, id).Error; err != nil {
			t.Fatalf("queued task %d missing: %v", id, err)
		}
		if created.Status != "pending" {
			t.Fatalf("queued task %d status = %q", id, created.Status)
		}
		if created.Command != "whoami" || created.Shell != "cmd.exe" {
			t.Fatalf("queued task did not copy the original parameters: %+v", created)
		}
	}

	// The originals are untouched: a rerun adds work, it does not rewrite history.
	for _, original := range []db.Task{f1, f2} {
		var reloaded db.Task
		if err := s.db.First(&reloaded, original.ID).Error; err != nil {
			t.Fatalf("original %d missing: %v", original.ID, err)
		}
		if reloaded.Status != "failed" {
			t.Fatalf("original %d status changed to %q", original.ID, reloaded.Status)
		}
	}

	// A second run has nothing left in the window except the control task, which is skipped.
	w2 := call("", 1)
	var second struct {
		Data struct {
			Queued int `json:"queued"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &second)
	if second.Data.Queued != 0 {
		t.Fatalf("a second bulk rerun queued %d tasks", second.Data.Queued)
	}

	// limit is honoured and reported as truncated when it cuts the answer. Fresh failures are needed
	// here because the earlier ones are already queued (the deterministic rerun key returns the live
	// row, which is the double-click protection).
	seed("a-1", 1, "shell", "failed", 10*time.Minute)
	seed("a-1", 1, "shell", "failed", 20*time.Minute)
	w3 := call("?limit=1", 1)
	var third struct {
		Data struct {
			Queued    int  `json:"queued"`
			Truncated bool `json:"truncated"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w3.Body.Bytes(), &third)
	if third.Data.Queued != 1 || !third.Data.Truncated {
		t.Fatalf("limit=1 queued %d truncated=%v (%s)", third.Data.Queued, third.Data.Truncated, w3.Body.String())
	}

	// An invalid window is rejected rather than silently ignoring the filter.
	if got := call("?since=forever", 1); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid since = %d, want 400", got.Code)
	}
}
