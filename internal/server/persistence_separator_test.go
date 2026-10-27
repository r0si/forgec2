package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func agentFormCtx(t *testing.T, agentID, path string, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+path, strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

func countAgentTasks(t *testing.T, s *Server, agentID, taskType string) int64 {
	t.Helper()
	var count int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agentID, taskType).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

// TestHandlePersistenceRejectsSeparator pins the guard on the method|binary_path
// wire format: an unvalidated '|' shifts the path, so a REMOVE would delete a
// different persistence entry than the operator picked.
func TestHandlePersistenceRejectsSeparator(t *testing.T) {
	s := mustTenantServer(t)
	agentID := seedSprayAgent(t, s)

	cases := []struct {
		name   string
		action string
		form   url.Values
	}{
		{"add method separator", "add", url.Values{"action": {"add"}, "method": {"registry|run"}, "binary_path": {`C:\x.exe`}}},
		{"add path separator", "add", url.Values{"action": {"add"}, "method": {"registry"}, "binary_path": {`C:\x|y.exe`}}},
		{"remove method separator", "remove", url.Values{"action": {"remove"}, "method": {"schtask|daily"}, "binary_path": {`C:\x.exe`}}},
		{"remove path separator", "remove", url.Values{"action": {"remove"}, "method": {"schtask"}, "binary_path": {`C:\x|y.exe`}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := agentFormCtx(t, agentID, "/persistence", tc.form)
			s.handlePersistence(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "invalid characters") {
				t.Fatalf("refusal = %s", w.Body.String())
			}
		})
	}
	// None of the refusals queued a task.
	if got := countAgentTasks(t, s, agentID, "persistence_add") + countAgentTasks(t, s, agentID, "persistence_remove"); got != 0 {
		t.Fatalf("refused requests queued %d task(s)", got)
	}

	// The clean path still works.
	c, w := agentFormCtx(t, agentID, "/persistence", url.Values{"action": {"add"}, "method": {"registry"}, "binary_path": {`C:\ProgramData\x.exe`}})
	s.handlePersistence(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("clean add = %d (%s)", w.Code, w.Body.String())
	}
	if got := countAgentTasks(t, s, agentID, "persistence_add"); got != 1 {
		t.Fatalf("persistence_add tasks = %d, want 1", got)
	}
}

// TestHandleUACBypassRejectsSeparator pins the same guard on method|payload: a
// shifted payload makes the bypass run a different command than the operator
// chose.
func TestHandleUACBypassRejectsSeparator(t *testing.T) {
	s := mustTenantServer(t)
	agentID := seedSprayAgent(t, s)

	c, w := agentFormCtx(t, agentID, "/uac-bypass", url.Values{"method": {"fodhelper|eventvwr"}, "payload": {`C:\x.exe`}})
	s.handleUACBypass(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("method separator: status = %d (%s)", w.Code, w.Body.String())
	}

	c, w = agentFormCtx(t, agentID, "/uac-bypass", url.Values{"method": {"fodhelper"}, "payload": {`C:\x|y.exe`}})
	s.handleUACBypass(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("payload separator: status = %d (%s)", w.Code, w.Body.String())
	}

	if got := countAgentTasks(t, s, agentID, "uac_bypass"); got != 0 {
		t.Fatalf("refused requests queued %d uac_bypass task(s)", got)
	}

	// A clean request still queues.
	c, w = agentFormCtx(t, agentID, "/uac-bypass", url.Values{"method": {"fodhelper"}, "payload": {`C:\ProgramData\x.exe`}})
	s.handleUACBypass(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("clean uac bypass = %d (%s)", w.Code, w.Body.String())
	}
	if got := countAgentTasks(t, s, agentID, "uac_bypass"); got != 1 {
		t.Fatalf("uac_bypass tasks = %d, want 1", got)
	}
}
