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
	"golang.org/x/crypto/bcrypt"
)

func seedLiveSession(t *testing.T, s *Server, userID uint, ip string, age time.Duration) {
	t.Helper()
	sess := db.UserSession{
		UserID:    userID,
		TokenHash: "hash-" + ip,
		IP:        ip,
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now().Add(-age),
	}
	if err := s.db.Create(&sess).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func activeSessionCount(t *testing.T, s *Server, userID uint) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(&db.UserSession{}).
		Where("user_id = ? AND revoked_at <= ? AND expires_at > ?", userID, time.Unix(0, 0), time.Now()).
		Count(&n).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

// TestEnforceSessionCap proves overflow evicts oldest-first and the newest
// (just-created login) always survives.
func TestEnforceSessionCap(t *testing.T) {
	s := mustTenantServer(t)
	var user db.User
	if err := s.db.Where("username = ?", "cap-user").First(&user).Error; err != nil {
		user = db.User{Username: "cap-user", Role: "user", TenantID: 1, IsActive: true}
		if err := s.db.Create(&user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	seedLiveSession(t, s, user.ID, "10.0.0.1", 3*time.Hour) // oldest: evicted
	seedLiveSession(t, s, user.ID, "10.0.0.2", 2*time.Hour)
	seedLiveSession(t, s, user.ID, "10.0.0.3", time.Hour) // newest: survives

	if n := s.enforceSessionCap(user.ID, 2); n != 1 {
		t.Fatalf("evicted=%d, want 1", n)
	}
	if n := activeSessionCount(t, s, user.ID); n != 2 {
		t.Fatalf("active=%d, want 2", n)
	}
	var oldest db.UserSession
	if err := s.db.Where("user_id = ? AND ip = ?", user.ID, "10.0.0.1").First(&oldest).Error; err != nil {
		t.Fatalf("reload oldest: %v", err)
	}
	if oldest.RevokedAt.IsZero() {
		t.Fatalf("oldest session not revoked")
	}
	// Unlimited (0) evicts nothing.
	if n := s.enforceSessionCap(user.ID, 0); n != 0 {
		t.Fatalf("unlimited cap evicted %d", n)
	}
}

// TestIsKnownLoginIP proves first-sighting detection with DB-error fail-open.
func TestIsKnownLoginIP(t *testing.T) {
	s := mustTenantServer(t)
	var user db.User
	if err := s.db.Where("username = ?", "ip-user").First(&user).Error; err != nil {
		user = db.User{Username: "ip-user", Role: "user", TenantID: 1, IsActive: true}
		if err := s.db.Create(&user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if s.isKnownLoginIP(user.ID, "9.9.9.9") {
		t.Fatalf("unknown IP reported known")
	}
	seedLiveSession(t, s, user.ID, "9.9.9.9", time.Minute)
	if !s.isKnownLoginIP(user.ID, "9.9.9.9") {
		t.Fatalf("known IP reported unknown")
	}
	if !s.isKnownLoginIP(user.ID, "") {
		t.Fatalf("empty IP must not block login")
	}
}

// TestLoginSessionCapAndNewIP exercises the full login path: cap eviction
// plus the first-sighting audit row, end to end.
func TestLoginSessionCapAndNewIP(t *testing.T) {
	s := newLoginTestServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("test-pass-123"), 4)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	user := db.User{Username: "cap-login", PasswordHash: string(hash), Role: "user", TenantID: 1, IsActive: true}
	if err := s.db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	s.cfg.Auth.SessionMaxConcurrent = 2
	seedLiveSession(t, s, user.ID, "10.0.0.1", 3*time.Hour)
	seedLiveSession(t, s, user.ID, "10.0.0.2", 2*time.Hour)

	form := url.Values{}
	form.Set("username", "cap-login")
	form.Set("password", "test-pass-123")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.RemoteAddr = "3.3.3.3:1234"
	s.handleLogin(c)

	if w.Code != http.StatusFound && w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s, want redirect/200", w.Code, w.Body.String())
	}
	if n := activeSessionCount(t, s, user.ID); n != 2 {
		t.Fatalf("active sessions=%d, want 2 (oldest evicted, fresh survives)", n)
	}
	var evicted, newIP int64
	s.db.Model(&db.AuditLog{}).Where("action = ? AND success = ?", "session_evict", true).Count(&evicted)
	s.db.Model(&db.AuditLog{}).Where("action = ? AND success = ?", "login_new_ip", true).Count(&newIP)
	if evicted == 0 {
		t.Fatalf("no session_evict audit row")
	}
	if newIP == 0 {
		t.Fatalf("no login_new_ip audit row")
	}
	var login db.AuditLog
	if err := s.db.Where("action = ? AND resource = ?", "login", "auth").Order("id desc").First(&login).Error; err != nil {
		t.Fatalf("no successful login audit row: %v", err)
	}
	if login.IP != "3.3.3.3" {
		t.Fatalf("login audit IP=%q, want 3.3.3.3", login.IP)
	}
	if login.User != "cap-login" {
		t.Fatalf("login audit user=%q, want cap-login", login.User)
	}
	if login.TenantID != 1 {
		t.Fatalf("login audit tenant=%d, want 1", login.TenantID)
	}

	badForm := url.Values{}
	badForm.Set("username", "cap-login")
	badForm.Set("password", "wrong-password")
	failed := httptest.NewRecorder()
	failedCtx, _ := gin.CreateTestContext(failed)
	failedCtx.Request, _ = http.NewRequest(http.MethodPost, "/login", strings.NewReader(badForm.Encode()))
	failedCtx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	failedCtx.Request.RemoteAddr = "4.4.4.4:4321"
	s.handleLogin(failedCtx)
	var failure db.AuditLog
	if err := s.db.Where("action = ? AND resource = ?", "login_failed", "auth").Order("id desc").First(&failure).Error; err != nil {
		t.Fatalf("no failed login audit row: %v", err)
	}
	if failure.IP != "4.4.4.4" {
		t.Fatalf("failed login audit IP=%q, want 4.4.4.4", failure.IP)
	}
}
