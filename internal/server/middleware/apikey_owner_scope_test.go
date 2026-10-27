package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func scopeCtx(role string, scopes map[string]bool) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/whatever", nil)
	c.Set("user", "keyowner")
	c.Set("user_role", role)
	c.Set("auth_via_api_key", true)
	c.Set("api_key_scopes", scopes)
	return c, w
}

// TestScopedKeyCannotExceedItsOwner pins a real authorization gap: RequirePermission used
// to grant a scoped API key whatever its scope set said, without intersecting that set with
// the owner's CURRENT role. The scope set is fixed when the key is created, so demoting the
// owner (admin -> user) left the key with every capability it was born with - a revocation
// path that silently did nothing.
func TestScopedKeyCannotExceedItsOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := RequirePermission(db.PermUsersWrite)

	// The owner is now a plain user, who has no users.write. The key must not work.
	c, w := scopeCtx(db.RoleUser, map[string]bool{db.PermUsersWrite: true})
	handler(c)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("a demoted owner's key still reached users.write: status %d", w.Code)
	}

	// The same key under an admin owner is allowed, because admin really has that perm.
	c, w = scopeCtx(db.RoleAdmin, map[string]bool{db.PermUsersWrite: true})
	handler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("an admin owner's scoped key was refused: %d (%s)", w.Code, w.Body.String())
	}

	// A key that does not carry the scope is refused even for an admin owner.
	c, w = scopeCtx(db.RoleAdmin, map[string]bool{db.PermAgentsRead: true})
	handler(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a key without the scope reached the route: %d", w.Code)
	}
}

// TestScopedKeyAllowedWhenOwnerKeepsThePermission is the positive half: a user whose role
// does carry the permission can use a key scoped to it.
func TestScopedKeyAllowedWhenOwnerKeepsThePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := RequirePermission(db.PermAgentsRead)
	c, w := scopeCtx(db.RoleUser, map[string]bool{db.PermAgentsRead: true})
	handler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("a user owning agents.read was refused their own scoped key: %d", w.Code)
	}
}

// TestLegacyFullKeyStillUsesRoleLogic keeps the legacy path untouched: a key with no scope
// set falls through to normal role checks.
func TestLegacyFullKeyStillUsesRoleLogic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := RequirePermission(db.PermUsersWrite)

	// nil scopes = legacy full-access key; the owner is a plain user -> refused by role.
	c, w := scopeCtx(db.RoleUser, nil)
	handler(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a legacy key on a plain-user owner passed a users.write route: %d", w.Code)
	}

	// Admin owner -> allowed, exactly as a session request would be.
	c, w = scopeCtx(db.RoleAdmin, nil)
	handler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("an admin-owned legacy key was refused: %d", w.Code)
	}
}
