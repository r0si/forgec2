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

// TestAgentTagsAreTenantScoped covers the read side of the tagging feature. Both handlers take
// agent ids from the request (a path parameter, and a JSON list), and agent_tags has no tenant
// column, so neither the single nor the batch form could scope through it - which told an
// operator in one tenant how another tenant had classified its hosts.
func TestAgentTagsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	agent := db.Implant{ID: "tag-victim", Hostname: "victim", Status: "online", TenantID: 1, LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	tag := db.AgentTag{Name: "sekrit-tag", Color: "#ff0000"}
	if err := s.db.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.AgentTagAssignment{AgentTagID: tag.ID, ImplantID: agent.ID}).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(method, target string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		c.Set("user", "tenant2-admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		c.Set(middleware.TenantIDContextKey, uint(2))
		return c, w
	}

	// Single form: refused outright.
	c, w := ctxFor(http.MethodGet, "/api/agents/"+agent.ID+"/tags", nil)
	s.handleAgentTags(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 read tenant 1's agent tags: %d (%s)", w.Code, w.Body.String())
	}

	// Batch form: the foreign id simply yields no tags.
	payload, _ := json.Marshal(map[string][]string{"agent_ids": {agent.ID}})
	c, w = ctxFor(http.MethodPost, "/api/agents/tags/batch", payload)
	s.handleBatchAgentTags(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-tag")) {
		t.Fatalf("the batch form leaked the foreign tag: %s", w.Body.String())
	}

	// The owning tenant still sees it, so the scope is not a blanket denial.
	c, w = ctxFor(http.MethodGet, "/api/agents/"+agent.ID+"/tags", nil)
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleAgentTags(c)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("sekrit-tag")) {
		t.Fatalf("tenant 1 cannot read its own agent's tags: %d (%s)", w.Code, w.Body.String())
	}
}
