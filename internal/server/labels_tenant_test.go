package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestTagsAndGroupsAreTenantScoped closes the last documented gap: agent_tags, agent_groups and
// auto_tag_rules had no tenant column at all, so every tenant listed, renamed and deleted every
// other tenant's labels - and the tag NAME index was globally unique, which meant tenant B could
// not even create a tag tenant A already had.
func TestTagsAndGroupsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	ctxFor := func(method, target string, body []byte, tenant uint) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(3))
		c.Set(middleware.TenantIDContextKey, tenant)
		return c, w
	}

	// Tenant 1 creates a tag and a group; the tenant is stamped by the handler.
	body, _ := json.Marshal(map[string]string{"name": "critical", "color": "#ff0000"})
	c, w := ctxFor(http.MethodPost, "/api/tags", body, 1)
	s.handleAPITagCreate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant 1 tag create = %d (%s)", w.Code, w.Body.String())
	}
	groupBody, _ := json.Marshal(map[string]string{"name": "sekrit-group"})
	c, w = ctxFor(http.MethodPost, "/api/groups", groupBody, 1)
	c.Params = gin.Params{}
	s.handleAPICreateGroup(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("tenant 1 group create = %d (%s)", w.Code, w.Body.String())
	}

	// Tenant 2 does not see either.
	c, w = ctxFor(http.MethodGet, "/api/tags", nil, 2)
	s.handleAPITagList(c)
	if bytes.Contains(w.Body.Bytes(), []byte("critical")) {
		t.Fatalf("tenant 2 listed tenant 1's tag: %s", w.Body.String())
	}
	c, w = ctxFor(http.MethodGet, "/api/groups", nil, 2)
	s.handleAPIGroups(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-group")) {
		t.Fatalf("tenant 2 listed tenant 1's group: %s", w.Body.String())
	}

	// Tenant 2 CAN create a tag with the same name: uniqueness is per tenant now.
	c, w = ctxFor(http.MethodPost, "/api/tags", body, 2)
	s.handleAPITagCreate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant 2 could not reuse the tag name: %d (%s)", w.Code, w.Body.String())
	}

	// Tenant 1 still sees its own.
	c, w = ctxFor(http.MethodGet, "/api/tags", nil, 1)
	s.handleAPITagList(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("critical")) {
		t.Fatalf("tenant 1 cannot see its own tag: %s", w.Body.String())
	}
}
