package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestEmergencyStopRequiresAdminAndStaysInTenant pins both halves of a real hole: the handler
// re-authenticated the caller with their password but never checked a ROLE, so any authenticated
// operator who knew their own password could arm the kill switch for the whole installation - and
// the targeting query had no tenant scope, so a tenant-scoped admin would also have killed every
// other tenant's implants.
func TestEmergencyStopRequiresAdminAndStaysInTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	hash, err := middleware.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	plain := db.User{Username: "plain-user", Role: db.RoleUser, TenantID: 1, IsActive: true, PasswordHash: hash}
	if err := s.db.Create(&plain).Error; err != nil {
		t.Fatal(err)
	}
	t2admin := db.User{Username: "tenant2-admin", Role: db.RoleAdmin, TenantID: 2, IsActive: true, PasswordHash: hash}
	if err := s.db.Create(&t2admin).Error; err != nil {
		t.Fatal(err)
	}
	t1admin := db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true, PasswordHash: hash}
	if err := s.db.Create(&t1admin).Error; err != nil {
		t.Fatal(err)
	}

	t1agent := db.Implant{ID: "kill-t1", Hostname: "t1", Status: "online", TenantID: 1, LastSeen: time.Now()}
	t2agent := db.Implant{ID: "kill-t2", Hostname: "t2", Status: "online", TenantID: 2, LastSeen: time.Now()}
	for _, a := range []*db.Implant{&t1agent, &t2agent} {
		if err := s.db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
	}

	stop := func(uid uint, role, username string, tenant uint) *httptest.ResponseRecorder {
		form := url.Values{"password": {"correct-horse-battery"}}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/emergency/stop", strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.Set("user", username)
		c.Set("user_role", role)
		c.Set("user_id", uid)
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleEmergencyStop(c)
		return w
	}

	// 1. A plain operator is refused outright.
	if w := stop(plain.ID, db.RoleUser, "plain-user", 1); w.Code != http.StatusForbidden {
		t.Fatalf("plain user emergency stop = %d, want 403 (%s)", w.Code, w.Body.String())
	}
	var untouched db.Implant
	if err := s.db.Where("id = ?", t1agent.ID).First(&untouched).Error; err != nil {
		t.Fatal(err)
	}
	if untouched.KillDate != nil && !untouched.KillDate.IsZero() {
		t.Fatal("a plain user armed the kill switch")
	}

	// 2. A tenant-2 admin stops only tenant-2 agents.
	if w := stop(t2admin.ID, db.RoleAdmin, "tenant2-admin", 2); w.Code != http.StatusOK {
		t.Fatalf("tenant 2 admin stop = %d (%s)", w.Code, w.Body.String())
	}
	var t1after, t2after db.Implant
	if err := s.db.Where("id = ?", t1agent.ID).First(&t1after).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Where("id = ?", t2agent.ID).First(&t2after).Error; err != nil {
		t.Fatal(err)
	}
	if t1after.KillDate != nil && !t1after.KillDate.IsZero() {
		t.Fatal("tenant 2's emergency stop killed tenant 1's agent")
	}
	if t2after.KillDate == nil || t2after.KillDate.IsZero() {
		t.Fatal("tenant 2's own agent was not stopped, so the scope is too wide")
	}
}
