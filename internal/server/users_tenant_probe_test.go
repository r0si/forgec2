package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestUsersPageTenantIsolation documents whether a tenant-scoped admin sees
// other tenants' users through the user-management page payload. It is a probe
// first: if it fails, the page leaks usernames across tenants.
func TestUsersPageTenantIsolation(t *testing.T) {
	s := mustTenantServer(t)
	// A tenant-1 admin and a tenant-2 user.
	if err := s.db.Create(&db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.User{Username: "tenant2-ops", Role: db.RoleUser, TenantID: 2, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/users", nil)
	c.Set("user", "tenant1-admin")
	c.Set("user_role", db.RoleAdmin)
	s.handleUsersPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("users page = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "tenant2-ops") {
		t.Fatalf("users page showed a tenant-2 user to a tenant-1 admin")
	}
	if !strings.Contains(body, "tenant1-admin") {
		t.Fatalf("users page is missing the caller's own tenant: %.200s", body)
	}
}

// TestAddUserStampsCallerTenant checks that a user created by a tenant admin
// belongs to that tenant instead of the legacy unscoped bucket.
func TestAddUserStampsCallerTenant(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 7, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader("username=newop&password=Str0ng!Passw0rd&role=user"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	s.handleAddUser(c)
	if w.Code != http.StatusOK {
		t.Fatalf("add user = %d %s", w.Code, w.Body.String())
	}
	var created db.User
	if err := s.db.Where("username = ?", "newop").First(&created).Error; err != nil {
		t.Fatalf("created user missing: %v", err)
	}
	if created.TenantID != 7 {
		t.Fatalf("created user tenant = %d, want the caller's tenant 7", created.TenantID)
	}
}

func TestAddUserAcceptsExistingObserverRoleOnly(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.User{Username: "demo-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.CustomRole{Name: "demo-observer", Permissions: `["agents.read","tasks.read"]`}).Error; err != nil {
		t.Fatal(err)
	}
	add := func(username, role string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(
			"username="+username+"&password=Str0ng!Passw0rd&role="+role))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.Set("user", "demo-admin")
		c.Set("user_role", db.RoleAdmin)
		s.handleAddUser(c)
		return w.Code
	}
	if got := add("viewer1", "demo-observer"); got != http.StatusOK {
		t.Fatalf("observer user creation status=%d", got)
	}
	var created db.User
	if err := s.db.Where("username = ?", "viewer1").First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.Role != "demo-observer" {
		t.Fatalf("created role=%q", created.Role)
	}
	if got := add("viewer2", "unknown-role"); got != http.StatusBadRequest {
		t.Fatalf("unknown role status=%d, want 400", got)
	}
	var count int64
	if err := s.db.Model(&db.User{}).Where("username = ?", "viewer2").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unknown role silently created a user")
	}
}

// TestSetUserPasswordTenantIsolation pins the cross-tenant takeover fix: a
// tenant admin must not be able to reset another tenant's password by id.
func TestSetUserPasswordTenantIsolation(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.User{Username: "admin1", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	foreign := db.User{Username: "ops2", Role: db.RoleUser, TenantID: 2, IsActive: true, PasswordHash: "original-hash"}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	call := func(caller string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/users/1/password",
			strings.NewReader("password=NewStr0ng!Passw0rd"))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.Set("user", caller)
		c.Set("user_role", db.RoleAdmin)
		c.Params = gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}}
		s.handleSetUserPassword(c)
		return w.Code
	}

	if code := call("admin1"); code != http.StatusNotFound {
		t.Fatalf("cross-tenant password reset = %d, want 404", code)
	}
	var reloaded db.User
	if err := s.db.Where("id = ?", foreign.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PasswordHash != "original-hash" {
		t.Fatal("cross-tenant admin changed the password")
	}

	// A same-tenant admin may reset it, and existing sessions are revoked.
	if err := s.db.Model(&db.User{}).Where("id = ?", foreign.ID).Update("tenant_id", 1).Error; err != nil {
		t.Fatal(err)
	}
	seedSession(t, s, foreign.ID, "foreign-user-token")
	if code := call("admin1"); code != http.StatusOK {
		t.Fatalf("same-tenant password reset = %d", code)
	}
	if err := s.db.Where("id = ?", foreign.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PasswordHash == "original-hash" {
		t.Fatal("password was not updated")
	}
	revoked, err := s.isSessionRevoked("foreign-user-token")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("password reset did not revoke the target's sessions")
	}
}

// TestAddUserValidation covers the create-time gates.
func TestAddUserValidation(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	post := func(body string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		s.handleAddUser(c)
		return w.Code
	}

	if code := post("username=&password="); code != http.StatusBadRequest {
		t.Fatalf("missing fields = %d", code)
	}
	if code := post("username=x&password=short"); code != http.StatusBadRequest {
		t.Fatalf("weak password = %d", code)
	}
	if code := post("username=admin&password=Str0ng!Passw0rd"); code != http.StatusConflict {
		t.Fatalf("duplicate = %d", code)
	}
	if code := post("username=coerced&password=Str0ng!Passw0rd&role=admin"); code != http.StatusOK {
		t.Fatalf("create admin-capable user = %d", code)
	}
	var coerced db.User
	if err := s.db.Where("username = ?", "coerced").First(&coerced).Error; err != nil {
		t.Fatal(err)
	}
	if coerced.Role != db.RoleAdmin {
		t.Fatalf("explicit admin role = %q, want admin", coerced.Role)
	}
	// Unknown roles must fail closed instead of silently creating a more
	// privileged built-in user. Existing custom roles are covered separately.
	if code := post("username=plainrole&password=Str0ng!Passw0rd&role=viewer"); code != http.StatusBadRequest {
		t.Fatalf("create with unknown role = %d, want 400", code)
	}
	var plainCount int64
	if err := s.db.Model(&db.User{}).Where("username = ?", "plainrole").Count(&plainCount).Error; err != nil {
		t.Fatal(err)
	}
	if plainCount != 0 {
		t.Fatal("unknown role silently created a user")
	}
}

// TestDeleteUserGuards covers self-deletion and the cross-tenant 404.
func TestDeleteUserGuards(t *testing.T) {
	s := mustTenantServer(t)
	me := db.User{Username: "admin1", Role: db.RoleAdmin, TenantID: 1, IsActive: true}
	if err := s.db.Create(&me).Error; err != nil {
		t.Fatal(err)
	}
	foreign := db.User{Username: "ops2", Role: db.RoleUser, TenantID: 2, IsActive: true}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	local := db.User{Username: "ops1", Role: db.RoleUser, TenantID: 1, IsActive: true}
	if err := s.db.Create(&local).Error; err != nil {
		t.Fatal(err)
	}
	seedSession(t, s, local.ID, "local-token")

	del := func(targetID uint) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/users/x", nil)
		c.Set("user", "admin1")
		c.Set("user_role", db.RoleAdmin)
		c.Params = gin.Params{{Key: "id", Value: jsonNumber(targetID)}}
		s.handleDeleteUser(c)
		return w.Code
	}

	if code := del(me.ID); code != http.StatusBadRequest {
		t.Fatalf("self delete = %d", code)
	}
	if code := del(foreign.ID); code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete = %d", code)
	}
	if code := del(local.ID); code != http.StatusOK {
		t.Fatalf("own-tenant delete = %d", code)
	}
	var count int64
	s.db.Model(&db.User{}).Where("id = ?", local.ID).Count(&count)
	if count != 0 {
		t.Fatal("user row survived delete")
	}
	var sessions int64
	s.db.Model(&db.UserSession{}).Where("user_id = ?", local.ID).Count(&sessions)
	if sessions != 0 {
		t.Fatal("user sessions survived delete")
	}
}
