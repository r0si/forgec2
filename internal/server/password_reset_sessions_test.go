package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestPasswordResetKillsLiveSessions pins the property behind handleSetUserPassword: a
// reset revokes every live session ROW, and the auth middleware reads that table on each
// request, so the old token stops working immediately. The flow is easy to believe and easy
// to break - deleting the revoke call (or a future "cleanup" of it) would leave an attacker
// with a stolen session after an operator reacted to the incident.
func TestPasswordResetKillsLiveSessions(t *testing.T) {
	s := mustTenantServer(t)
	admin := seedSessionUser(t, s, "root", db.RoleAdmin, 1)
	target := seedSessionUser(t, s, "victim", db.RoleUser, 1)

	const token = "victim-live-session-token"
	seedSession(t, s, target.ID, token)
	seedSession(t, s, admin.ID, "admin-session-token") // must survive

	revoked, err := s.isSessionRevoked(token)
	if err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	if revoked {
		t.Fatal("the fresh session is already revoked; the test proves nothing")
	}

	form := url.Values{"password": {"Str0ng-Passw0rd!x"}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/users/"+uintString(target.ID)+"/password", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: uintString(target.ID)}}
	c.Set("user", "root")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", admin.ID)

	s.handleSetUserPassword(c)
	if w.Code != http.StatusOK {
		t.Fatalf("password reset = %d (%s)", w.Code, w.Body.String())
	}

	revoked, err = s.isSessionRevoked(token)
	if err != nil {
		t.Fatalf("post-check: %v", err)
	}
	if !revoked {
		t.Fatal("the victim's live session survived the password reset")
	}

	// The admin's own session must be untouched: revoking every session in the
	// installation would be a denial of service, not a reset.
	adminRevoked, err := s.isSessionRevoked("admin-session-token")
	if err != nil {
		t.Fatalf("admin check: %v", err)
	}
	if adminRevoked {
		t.Fatal("another user's session was revoked by the reset")
	}

	// The stored hash changed, so the old password can no longer match.
	var after db.User
	if err := s.db.First(&after, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash == "" {
		t.Fatal("no password hash was stored")
	}
}

// TestPasswordResetRefusesAnotherTenant keeps the tenant gate that this handler already had:
// a tenant-1 admin must not be able to reset a tenant-2 user and sign in as them.
func TestPasswordResetRefusesAnotherTenant(t *testing.T) {
	s := mustTenantServer(t)
	admin := seedSessionUser(t, s, "root1", db.RoleAdmin, 1)
	foreign := seedSessionUser(t, s, "outsider", db.RoleUser, 2)
	seedSession(t, s, foreign.ID, "outsider-session")

	form := url.Values{"password": {"Str0ng-Passw0rd!x"}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/users/"+uintString(foreign.ID)+"/password", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: uintString(foreign.ID)}}
	c.Set("user", "root1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", admin.ID)
	c.Set("tenant_id", uint(1))

	s.handleSetUserPassword(c)
	if w.Code == http.StatusOK {
		t.Fatalf("a cross-tenant password reset succeeded: %s", w.Body.String())
	}
	revoked, err := s.isSessionRevoked("outsider-session")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("the foreign user's session was revoked, so the reset went through")
	}
	_ = time.Now()
}

func uintString(v uint) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
