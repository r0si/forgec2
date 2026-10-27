package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAgentStatusHistoryIsScopedThroughTheAgent pins the fix for a scope applied to a table
// that has no tenant column: db.AgentStatusEvent carries only (agent_id, status, timestamp),
// so tenantScope added "WHERE tenant_id = ?" to a query that cannot support it - the feature
// answered 500 for every tenant-scoped operator and, without the filter, pooled every tenant's
// events. The scope now runs through the AGENT (which does carry the tenant).
func TestAgentStatusHistoryIsScopedThroughTheAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	agent := db.Implant{ID: "status-agent-1", Hostname: "host", Status: "online", TenantID: 1, LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"online", "stale", "offline"} {
		if err := s.db.Create(&db.AgentStatusEvent{AgentID: agent.ID, Status: st, Timestamp: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	call := func(tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID+"/status-history", nil)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		c.Set("user", "operator")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(3))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleAgentStatusHistory(c)
		return w
	}

	// The owning tenant gets the events, in ascending time order.
	w := call(1)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant 1 status history = %d (%s)", w.Code, w.Body.String())
	}
	for _, st := range []string{"online", "stale", "offline"} {
		if !containsAny(w.Body.String(), st) {
			t.Fatalf("event %q missing from the answer: %s", st, w.Body.String())
		}
	}

	// Another tenant must not read them, even with the exact agent id.
	w2 := call(2)
	if w2.Code == http.StatusOK && containsAny(w2.Body.String(), "offline") {
		t.Fatalf("tenant 2 read tenant 1's status history: %s", w2.Body.String())
	}
	if w2.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status history answered %d, want 404", w2.Code)
	}
}
