package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestRerunBySignature covers "re-run the timeouts, not every failure on the host".
//
// The selector is the same normalisation the failure report and the handover bundle use, so the row an operator is looking at
// and the rows this re-dispatches are the same set by construction - which is the whole point of passing a signature rather
// than a list of ids.
func TestRerunBySignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	seed := func(taskType, errText string, age time.Duration) uint {
		task := db.Task{
			AgentID: "a-1", TenantID: 1, Type: taskType, Command: "x", Status: "failed", Error: errText,
			CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		return task.ID
	}
	// Two permission failures differing only by path, one timeout, and one permission failure OUTSIDE the window.
	seed("ls", "open /tmp/a/one.txt: permission denied", time.Hour)
	seed("ls", "open /tmp/b/two.txt: permission denied", 2*time.Hour)
	seed("ps", "context deadline exceeded", 3*time.Hour)
	seed("ls", "open /tmp/c/old.txt: permission denied", 30*24*time.Hour)

	call := func(body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/tasks/rerun-by-signature", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleRerunBySignature(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. Any spelling of the same shape selects both in-window tasks, and not the out-of-window one.
	code, data := call(map[string]interface{}{
		"signature": "open /tmp/zzz/whatever.txt: permission denied",
		"since":     "7d",
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["matched"] != float64(2) {
		t.Fatalf("matched = %v, want the two in-window permission failures", data["matched"])
	}
	if data["queued"] != float64(2) {
		// The skips name why, which is what a caller needs when the count is lower than the match.
		t.Fatalf("queued = %v, skipped = %v", data["queued"], data["skipped"])
	}

	// 2. A signature that matches nothing says so, rather than looking like a failed request.
	code, data = call(map[string]interface{}{"signature": "no such failure anywhere", "since": "7d"})
	if code != http.StatusOK {
		t.Fatalf("unknown signature = %d, want 200 with a note", code)
	}
	if data["matched"] != float64(0) {
		t.Fatalf("unknown signature matched %v task(s)", data["matched"])
	}
	if note, _ := data["note"].(string); note == "" {
		t.Fatal("an empty result carries no explanation")
	}

	// 3. The guards: a signature is required, and so is a sane window.
	if code, _ := call(map[string]interface{}{"since": "7d"}); code != http.StatusBadRequest {
		t.Fatalf("empty signature = %d, want 400", code)
	}
	if code, _ := call(map[string]interface{}{"signature": "x", "since": "forever"}); code != http.StatusBadRequest {
		t.Fatalf("bad window = %d, want 400", code)
	}

	// 4. The dispatch is audited with the shape, so "what did we re-run" has an answer.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "tasks_rerun_by_signature").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("tasks_rerun_by_signature rows = %d, want 1", audited)
	}
}
