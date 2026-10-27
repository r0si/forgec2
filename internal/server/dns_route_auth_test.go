package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestDNSListenerRoutesRequireListenerPermission pins the authorization fix: the DNS routes
// hang off the global auth group, which applies AuthRequired but no permission check - so any
// authenticated role, including a read-only one, could bind :53 and start or stop the DNS C2
// listener. Starting C2 infrastructure is at least as privileged as creating a task.
func TestDNSListenerRoutesRequireListenerPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// A role without the listener grants (a custom role UNKNOWN to the table fails closed).
	// The built-in "user" role does hold listeners.write, so it is the wrong probe for the
	// authorization layer - it would reach the handler and fail on its own state instead.
	userRole := "viewer"
	router := gin.New()
	auth := router.Group("/")
	auth.Use(func(c *gin.Context) {
		c.Set("user", "plain")
		c.Set("user_role", userRole)
		c.Set("user_id", uint(2))
		c.Set(middleware.TenantIDContextKey, uint(1))
	})
	s.registerDNSRoutes(auth)

	for _, path := range []string{"/api/dns/start", "/api/dns/stop"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s = %d for a plain user, want 403", path, w.Code)
		}
	}

	// The status route needs the read permission, so the same role is refused there too.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/dns/status", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /api/dns/status = %d for a plain user, want 403", w.Code)
	}

	// An admin passes the permission layer (the handler's own response is irrelevant here).
	adminRouter := gin.New()
	admin := adminRouter.Group("/")
	admin.Use(func(c *gin.Context) {
		c.Set("user", "root")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
	})
	s.registerDNSRoutes(admin)
	w = httptest.NewRecorder()
	adminRouter.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/dns/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/dns/status = %d for an admin, want 200", w.Code)
	}
}
