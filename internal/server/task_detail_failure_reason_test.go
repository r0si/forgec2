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

// TestTaskDetailCarriesFailureReason covers the reason code on a single task.
//
// The failure report groups by these codes, and a reader who drills into one task should not have to infer the category from
// the message text - nor should the console carry a second copy of the classification rules that could drift from the
// report's.
func TestTaskDetailCarriesFailureReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	seed := func(taskType, errText, status string) uint {
		task := db.Task{
			AgentID: "a-1", TenantID: 1, Type: taskType, Command: "x", Status: status, Error: errText,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
		return task.ID
	}
	failedTimeout := seed("shell", "context deadline exceeded", "failed")
	failedOther := seed("shell", "something nobody has seen", "failed")
	completed := seed("shell", "", "completed")

	call := func(id uint) map[string]interface{} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/x", nil)
		c.Params = gin.Params{{Key: "taskId", Value: itoaUint(id)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleGetTaskStatus(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Data
	}

	// 1. A timeout is classified, and the code matches the report's vocabulary.
	if data := call(failedTimeout); data["failure_reason"] != "timeout" {
		t.Fatalf("failure_reason = %v, want timeout (payload %v)", data["failure_reason"], data)
	}
	// 2. An unrecognised error is still classified - as "other", never omitted, so a client has one code path.
	if data := call(failedOther); data["failure_reason"] != "other" {
		t.Fatalf("failure_reason = %v, want other", data["failure_reason"])
	}
	// 3. A task that did not fail carries no reason at all: the field's presence means something.
	if data := call(completed); data["failure_reason"] != nil {
		t.Fatalf("a completed task reported failure_reason %v", data["failure_reason"])
	}
}
