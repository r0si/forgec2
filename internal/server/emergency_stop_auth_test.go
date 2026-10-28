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

// TestKillSwitchAndChangePasswordRequireAPrincipal covers the other two sites
// with the same unscoped-lookup bug.
func TestKillSwitchAndChangePasswordRequireAPrincipal(t *testing.T) {
	s := mustTenantServer(t)
	hash, err := middleware.HashPassword("current-pass")
	if err != nil {
		t.Fatal(err)
	}
	user := db.User{Username: "ops", Role: db.RoleAdmin, TenantID: 1, IsActive: true, PasswordHash: hash}
	if err := s.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	// Kill switch: no user_id must be a 401, never a lookup of the first row.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/killswitch", strings.NewReader(`{"action":"arm","password":"current-pass"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "ops")
	c.Set("user_role", db.RoleAdmin)
	s.handleKillSwitch(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("killswitch without principal = %d %s", w.Code, w.Body.String())
	}
	var ks db.KillSwitch
	if err := s.db.First(&ks, 1).Error; err == nil && ks.Armed {
		t.Fatal("kill switch armed without a resolved principal")
	}

	// Password change: same.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/settings/password",
		strings.NewReader("current_password=current-pass&new_password=NewPassw0rd!&confirm_password=NewPassw0rd!"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "ops")
	c.Set("user_role", db.RoleAdmin)
	s.handleChangePassword(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("password change without principal = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.User
	if err := s.db.Where("id = ?", user.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PasswordHash != hash {
		t.Fatal("password rotated without a resolved principal")
	}
}

// TestEmergencyStopRequiresAPrincipal checks that the kill-switch password
// confirmation cannot be satisfied without a resolved user. The handler used to
// look the user up with `s.db.First(&user, c.Get("user_id"))`; when the key was
// absent the argument was nil, so GORM matched the first row and the
// confirmation was checked against an arbitrary account.
func TestEmergencyStopRequiresAPrincipal(t *testing.T) {
	s := mustTenantServer(t)
	hash, err := middleware.HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	admin := db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true, PasswordHash: hash}
	if err := s.db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "em-agent", TenantID: 1, Hostname: "em", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}

	// No user_id in the context, but the right password for the first user.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/emergency/stop", strings.NewReader("password=correct-horse"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "nobody")
	s.handleEmergencyStop(c)

	var armed db.Implant
	if err := s.db.Where("id = ?", "em-agent").First(&armed).Error; err != nil {
		t.Fatal(err)
	}
	if armed.KillDate != nil {
		// If this ever trips, the confirmation was satisfied without knowing
		// which user is asking: an auth bypass.
		t.Fatalf("kill date armed without a resolved principal (status=%d body=%s)", w.Code, w.Body.String())
	}
	if w.Code == http.StatusOK {
		t.Fatalf("emergency stop returned 200 without a resolved principal: %s", w.Body.String())
	}

	// With a resolved principal and the right password the kill switch arms.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/emergency/stop", strings.NewReader("password=correct-horse"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin")
	c.Set("user_id", admin.ID)
	// The role is part of the contract now: arming the kill switch is an admin action, so a
	// context carrying an identity but no role is refused (see emergency_stop_authz_test.go).
	c.Set("user_role", db.RoleAdmin)
	c.Set("tenant_id", uint(1))
	s.handleEmergencyStop(c)
	if w.Code != http.StatusOK {
		t.Fatalf("emergency stop with principal = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("id = ?", "em-agent").First(&armed).Error; err != nil {
		t.Fatal(err)
	}
	if armed.KillDate == nil {
		t.Fatal("kill date not armed for a valid request")
	}

	// A wrong password is refused.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/emergency/stop", strings.NewReader("password=nope"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin")
	c.Set("user_id", admin.ID)
	c.Set("user_role", db.RoleAdmin)
	c.Set("tenant_id", uint(1))
	s.handleEmergencyStop(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d %s", w.Code, w.Body.String())
	}
}
