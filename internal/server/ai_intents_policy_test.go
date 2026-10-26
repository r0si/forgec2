package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func intentCtx(id string, uid uint, role string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/ai/intents/x/approve", nil)
	c.Set("user", "ai-user")
	c.Set("user_role", role)
	c.Set("user_id", uid)
	c.Params = gin.Params{{Key: "id", Value: id}}
	return c, w
}

func seedAIIntent(t *testing.T, s *Server, tenant uint, status, tool, args, requiredPerm string) db.AIExecutionIntent {
	t.Helper()
	intent := db.AIExecutionIntent{
		ID: uuid.NewString(), TenantID: tenant, OwnerID: 1, Owner: "ai-user",
		SessionID: 1, RunID: "run-1",
		ToolName: tool, Arguments: args, RequiredPerm: requiredPerm,
		Status: status, Risk: "high", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return intent
}

// TestAIIntentApproveTenantAndPermissionGates pins the two gates on the
// approval path: an intent from another tenant is invisible, and a required
// permission the caller lacks is refused.
func TestAIIntentApproveTenantAndPermissionGates(t *testing.T) {
	s := setupToolExecServer(t)
	if err := s.db.AutoMigrate(&db.AIChatSession{}, &db.AIChatMessage{}); err != nil {
		t.Fatal(err)
	}
	// The AI principal is resolved from the user row, so seed real users.
	me := seedAIUser(t, s, "ai-user", 1)

	foreign := seedAIIntent(t, s, 2, "pending", "list_agents", `{}`, "")
	needsOps := seedAIIntent(t, s, 1, "pending", "execute_command", `{"agent_id":"a-win","command":"whoami"}`, db.PermAgentsWrite)

	// Another tenant's intent: 404, and it stays pending.
	c, w := intentCtx(foreign.ID, me.ID, db.RoleAdmin)
	s.handleAIIntentApprove(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign intent = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.AIExecutionIntent
	if err := s.db.Where("id = ?", foreign.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "pending" {
		t.Fatalf("foreign intent status = %q, want it untouched", reloaded.Status)
	}

	// Already decided intents are a conflict, not a re-execution.
	decided := seedAIIntent(t, s, 1, "executed", "list_agents", `{}`, "")
	c, w = intentCtx(decided.ID, me.ID, db.RoleAdmin)
	s.handleAIIntentApprove(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("decided intent = %d %s", w.Code, w.Body.String())
	}

	// A required permission the caller does not hold is refused. The seeded
	// admin role holds agents.write, so drop the role to a plain user whose
	// custom permission set is empty.
	if err := s.db.Model(&db.User{}).Where("id = ?", me.ID).Update("role", "guest").Error; err != nil {
		t.Fatal(err)
	}
	c, w = intentCtx(needsOps.ID, me.ID, "guest")
	s.handleAIIntentApprove(c)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("permission gate = %d %s", w.Code, w.Body.String())
	}
	reloaded = db.AIExecutionIntent{}
	if err := s.db.Where("id = ?", needsOps.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != "pending" {
		t.Fatalf("permission-denied intent status = %q", reloaded.Status)
	}
}

// TestAIIntentRejectMarksRejected covers the reject path and its state guard.
func TestAIIntentRejectMarksRejected(t *testing.T) {
	s := setupToolExecServer(t)
	me := seedAIUser(t, s, "ai-user", 1)
	// Reject carries no tools.permission requirement of its own.
	intent := seedAIIntent(t, s, 1, "pending", "list_agents", `{}`, "")

	c, w := intentCtx(intent.ID, me.ID, db.RoleAdmin)
	s.handleAIIntentReject(c)
	if w.Code != http.StatusOK {
		t.Fatalf("reject = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.AIExecutionIntent
	if err := s.db.Where("id = ?", intent.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status == "pending" || reloaded.Status == "executing" {
		t.Fatalf("rejected intent status = %q", reloaded.Status)
	}
	if reloaded.DecidedBy != "ai-user" {
		t.Fatalf("decided_by = %q", reloaded.DecidedBy)
	}

	// Re-rejecting a decided intent is a conflict.
	c, w = intentCtx(intent.ID, me.ID, db.RoleAdmin)
	s.handleAIIntentReject(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("second reject = %d %s", w.Code, w.Body.String())
	}
}

// TestAIIntentsListScopesToTenant covers the pending list.
func TestAIIntentsListScopesToTenant(t *testing.T) {
	s := setupToolExecServer(t)
	me := seedAIUser(t, s, "ai-user", 1)
	mine := seedAIIntent(t, s, 1, "pending", "list_agents", `{}`, "")
	seedAIIntent(t, s, 2, "pending", "list_agents", `{}`, "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ai/intents", nil)
	c.Set("user", "ai-user")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", me.ID)
	s.handleAIIntentsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	var intents []db.AIExecutionIntent
	unmarshalData(t, w.Body.Bytes(), &intents)
	if len(intents) != 1 || intents[0].ID != mine.ID {
		t.Fatalf("intents = %+v, want only the caller's tenant", intents)
	}
}
