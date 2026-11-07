package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestRequirePermissionMiddlewareGate documents the behaviour the update-check
// refresh route now depends on: a viewer is refused and a role holding the
// permission passes.
func TestRequirePermissionMiddlewareGate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(role string) int {
		w := httptest.NewRecorder()
		engine := gin.New()
		engine.POST("/probe", func(c *gin.Context) { c.Set("user_role", role) }, middleware.RequirePermission(db.PermSettingsWrite), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/probe", nil))
		return w.Code
	}

	if code := run("viewer"); code != http.StatusForbidden {
		t.Fatalf("viewer = %d, want 403", code)
	}
	if code := run(""); code != http.StatusForbidden {
		t.Fatalf("empty role = %d, want 403", code)
	}
	if code := run(db.RoleAdmin); code != http.StatusOK {
		t.Fatalf("admin = %d, want 200", code)
	}
	if code := run(db.RoleUser); code != http.StatusForbidden {
		t.Fatalf("plain user = %d, want 403 (no settings.write)", code)
	}
}

// TestInfraFrontListIsARegisteredRead pins the Domain Fronting list endpoint.
// The handler only returns in-memory configuration, so requiring agents.write
// answered 403 for every read-only role (the demo observer holds agents.read).
// The config write must stay in the write group.
func TestInfraFrontListIsARegisteredRead(t *testing.T) {
	src, err := os.ReadFile("routes_extended.go")
	if err != nil {
		t.Fatalf("read routes_extended.go: %v", err)
	}
	all := string(src)
	if strings.Contains(all, `extWrite.POST("/infra/front/list"`) {
		t.Fatal("/infra/front/list is back in the agents.write group; read-only roles lose the page")
	}
	if !strings.Contains(all, `extRead.POST("/infra/front/list", s.handleAPIInfraFrontList)`) {
		t.Fatal("/infra/front/list is not registered on the read group")
	}
	if !strings.Contains(all, `extWrite.POST("/infra/front/config"`) {
		t.Fatal("/infra/front/config lost its write guard")
	}
}

// TestUpdateCheckRefreshRouteIsPermissionGuarded is a source-contract test: the
// route is registered on the root auth group, so nothing else would stop a
// viewer from forcing outbound update checks if the permission middleware were
// dropped in a refactor.
func TestUpdateCheckRefreshRouteIsPermissionGuarded(t *testing.T) {
	src, err := os.ReadFile("routes_misc.go")
	if err != nil {
		t.Fatalf("read routes_misc.go: %v", err)
	}
	idx := strings.Index(string(src), "/api/update-check/refresh")
	if idx < 0 {
		t.Fatal("refresh route not found in routes_misc.go")
	}
	// Look at the registration statement (up to the closing paren of the call).
	all := string(src)
	window := all[idx:]
	if end := strings.Index(window, "\n\n"); end > 0 {
		window = window[:end]
	}
	if !strings.Contains(window, "middleware.RequirePermission(db.PermSettingsWrite)") {
		t.Fatalf("refresh route lost its permission middleware:\n%s", window)
	}

	// The hot-update route must stay admin-only (it replaces the binary).
	if !strings.Contains(all, `"/api/update-check/hot-update", middleware.RequireRole(db.RoleAdmin)`) {
		t.Fatal("hot-update route lost its admin requirement")
	}
}
