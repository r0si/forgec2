package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBatchAgentTagsIsReadableByReadOnlyRoles pins the permission of the
// batch tag read. The console fetches the tags of every visible beacon in one
// POST, so a role that may read agents but not write them must be able to read
// the tags too. It used to sit behind agents.write, which 403'd every
// read-only operator (and the public demo's observer) and made the beacons
// page report "Could not read: beacon tags" above a fleet it could see.
func TestBatchAgentTagsIsReadableByReadOnlyRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	run := func(role string, perms map[string]bool) int {
		router := gin.New()
		auth := router.Group("/")
		auth.Use(func(c *gin.Context) {
			c.Set("user", "plain")
			c.Set("user_role", role)
			c.Set("user_id", uint(2))
			c.Set(middleware.TenantIDContextKey, uint(1))
			// The auth middleware resolves a custom role's grants once and
			// hands them over here; the permission check reads this map.
			c.Set("role_permissions", perms)
		})
		s.registerExtendedRoutes(auth)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/agents/batch/tags", strings.NewReader(`{"agent_ids":["a1"]}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w.Code
	}

	if code := run("readonly-ops", map[string]bool{db.PermAgentsRead: true}); code != http.StatusOK {
		t.Fatalf("read-only role = %d, want 200 (the batch tag read is a read)", code)
	}
	// A role without agents.read still cannot read the tags.
	if code := run("viewer", map[string]bool{}); code != http.StatusForbidden {
		t.Fatalf("role without agents.read = %d, want 403", code)
	}
}
