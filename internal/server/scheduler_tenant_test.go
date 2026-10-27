package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestOneShotScheduleIsTenantScoped covers the third table in this family that has no tenant
// column of its own (after agent_status_events): a scheduled task carries only agent_id, so the
// listing and the cancel-by-id have to scope through the agent. Without that, every operator
// saw every tenant's scheduled COMMANDS and could cancel another tenant's pending job.
func TestOneShotScheduleIsTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	mine := db.Implant{ID: "sched-mine", Hostname: "h1", Status: "online", TenantID: 1, LastSeen: time.Now()}
	theirs := db.Implant{ID: "sched-theirs", Hostname: "h2", Status: "online", TenantID: 2, LastSeen: time.Now()}
	for _, a := range []*db.Implant{&mine, &theirs} {
		if err := s.db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
	}
	ownTask := db.OneShotTask{AgentID: mine.ID, Type: "shell", Command: "whoami", RunAt: time.Now().Add(time.Hour), Status: "pending"}
	foreignTask := db.OneShotTask{AgentID: theirs.ID, Type: "shell", Command: "sekrit-command", RunAt: time.Now().Add(time.Hour), Status: "pending"}
	for _, row := range []*db.OneShotTask{&ownTask, &foreignTask} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	list := func(tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/scheduler", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(3))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleListOneShotTasks(c)
		return w
	}

	// The owning tenant sees its own row and NOT the other tenant's command.
	w := list(1)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("tenant 1 list = %d (%s)", w.Code, body)
	}
	if !bytes.Contains([]byte(body), []byte("whoami")) {
		t.Fatalf("tenant 1 cannot see its own schedule: %s", body)
	}
	if bytes.Contains([]byte(body), []byte("sekrit-command")) {
		t.Fatalf("tenant 1 saw another tenant's scheduled command: %s", body)
	}

	// And cannot cancel it either.
	cancel := func(tenant uint, id uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/scheduler/"+uintString(id), nil)
		c.Params = gin.Params{{Key: "id", Value: uintString(id)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(3))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleCancelOneShotTask(c)
		return w
	}
	if w := cancel(1, foreignTask.ID); w.Code != http.StatusNotFound {
		t.Fatalf("tenant 1 cancelled tenant 2's pending job: %d", w.Code)
	}
	var stillPending db.OneShotTask
	if err := s.db.First(&stillPending, foreignTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != "pending" {
		t.Fatalf("the foreign job was cancelled (status %q)", stillPending.Status)
	}

	// The owner can still cancel its own.
	if w := cancel(1, ownTask.ID); w.Code != http.StatusOK {
		t.Fatalf("tenant 1 could not cancel its own job: %d (%s)", w.Code, w.Body.String())
	}
}
