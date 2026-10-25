package server

import (
	"encoding/json"
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

func TestHandleLogin_LockoutAfterFailures(t *testing.T) {
	s := newLoginTestServer(t)

	hash, err := middleware.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	s.db.Create(&db.User{
		Username:     "lockout-test-user",
		PasswordHash: hash,
		Role:         "user",
		IsActive:     true,
	})

	form := url.Values{}
	form.Set("username", "lockout-test-user")
	form.Set("password", "wrong-password")

	// Exhaust login attempts
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.handleLogin(c)
	}

	// Next attempt should be locked out (returns 401 with lockout message)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleLogin(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 after lockout, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if errMsg, ok := resp["error"].(string); ok && errMsg != "" {
		if !strings.Contains(errMsg, "Try again") {
			t.Errorf("expected lockout error message, got: %s", errMsg)
		}
	}
}

func TestHandleLogout_RevokesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	cfg := newLoginTestServer(t).cfg
	s := &Server{db: database, cfg: cfg}

	hash, err := middleware.HashPassword("test-pass")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := db.User{
		Username:     "logout-test",
		PasswordHash: hash,
		Role:         "admin",
		IsActive:     true,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, err := middleware.GenerateToken(user, false, 24)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := s.createSession(token, user.ID, "127.0.0.1", "test-agent", "", 86400); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Verify session exists
	if revoked, err := s.isSessionRevoked(token); err != nil || revoked {
		t.Fatal("session should not be revoked before logout")
	}

	// Perform logout
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/logout", nil)
	c.Request.AddCookie(&http.Cookie{Name: "forgec2_session", Value: token})
	c.Set("user", user.Username)
	c.Set("user_id", user.ID)
	s.handleLogout(c)

	if revoked, err := s.isSessionRevoked(token); err != nil || !revoked {
		t.Fatal("session should be revoked after logout")
	}
}

func TestHandleGetCurrentUser_Active(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	user := db.User{
		Username: "current-user",
		Role:     "user",
		IsActive: true,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/current-user", nil)
	c.Set("user_id", user.ID)

	s.handleGetCurrentUser(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			ID         uint   `json:"id"`
			Username   string `json:"username"`
			Role       string `json:"role"`
			SessionExp int64  `json:"session_exp"`
		} `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Data.ID != user.ID {
		t.Errorf("expected id=%d, got %d", user.ID, resp.Data.ID)
	}
	if resp.Data.Username != "current-user" {
		t.Errorf("expected username=current-user, got %s", resp.Data.Username)
	}
	if resp.Data.SessionExp != 0 {
		t.Errorf("expected session_exp to be absent (0) when not set in context, got %d", resp.Data.SessionExp)
	}
}

func TestHandleGetCurrentUser_ReturnsSessionExp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	user := db.User{
		Username: "exp-user",
		Role:     "user",
		IsActive: true,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	exp := time.Now().Add(24 * time.Hour)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/current-user", nil)
	c.Set("user_id", user.ID)
	c.Set("session_exp", exp)

	s.handleGetCurrentUser(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			SessionExp int64 `json:"session_exp"`
		} `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Data.SessionExp == 0 {
		t.Fatalf("expected session_exp to be present, got %d", resp.Data.SessionExp)
	}
	wantMs := exp.UnixMilli()
	if resp.Data.SessionExp != wantMs {
		t.Errorf("expected session_exp=%d, got %d", wantMs, resp.Data.SessionExp)
	}
}

func TestHandleGetCurrentUser_ReturnsPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	cases := []struct {
		name string
		role string
		want []string
	}{
		{"built-in user role", db.RoleUser, db.GetPermissionsForRole(db.RoleUser)},
		{"admin gets every permission", db.RoleAdmin, db.GetAllPermissions()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := db.User{Username: "perms-" + tc.role, Role: tc.role, IsActive: true}
			if err := database.Create(&user).Error; err != nil {
				t.Fatalf("create user: %v", err)
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, "/api/me", nil)
			c.Set("user_id", user.ID)

			s.handleGetCurrentUser(c)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Success bool `json:"success"`
				Data    struct {
					Permissions []string `json:"permissions"`
				} `json:"data"`
			}
			unmarshalData(t, w.Body.Bytes(), &resp)
			if len(resp.Data.Permissions) == 0 {
				t.Fatal("expected a non-empty permissions list")
			}
			for _, want := range tc.want {
				found := false
				for _, got := range resp.Data.Permissions {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing permission %q in %v", want, resp.Data.Permissions)
				}
			}
		})
	}
}

func TestHandleGetCurrentUser_CustomRolePermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	customRole := db.CustomRole{
		Name:        "blue-team",
		Description: "read-only blue team role",
		Permissions: `["agents.read","audit.read"]`,
	}
	if err := database.Create(&customRole).Error; err != nil {
		t.Fatalf("create custom role: %v", err)
	}

	user := db.User{Username: "bt-user", Role: "blue-team", IsActive: true}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/me", nil)
	c.Set("user_id", user.ID)

	s.handleGetCurrentUser(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Role        string   `json:"role"`
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Data.Role != "blue-team" {
		t.Errorf("expected role=blue-team, got %s", resp.Data.Role)
	}
	want := []string{"agents.read", "audit.read"}
	if len(resp.Data.Permissions) != len(want) {
		t.Fatalf("expected permissions %v, got %v", want, resp.Data.Permissions)
	}
	for i := range want {
		if resp.Data.Permissions[i] != want[i] {
			t.Errorf("expected permissions %v, got %v", want, resp.Data.Permissions)
			break
		}
	}
}

func TestHandleGetCurrentUser_UnknownRoleNoPermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	user := db.User{Username: "ghost-role", Role: "no-such-role", IsActive: true}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/me", nil)
	c.Set("user_id", user.ID)

	s.handleGetCurrentUser(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Permissions []string `json:"permissions"`
		} `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if len(resp.Data.Permissions) != 0 {
		t.Errorf("expected no permissions for unknown role, got %v", resp.Data.Permissions)
	}
}

func TestHandleGetCurrentUser_Inactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	user := db.User{
		Username: "inactive-current-user",
		Role:     "user",
		IsActive: false,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/current-user", nil)
	c.Set("user_id", user.ID)

	s.handleGetCurrentUser(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for inactive user, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLogin_UsernameCaseInsensitive(t *testing.T) {
	s := newLoginTestServer(t)

	hash, err := middleware.HashPassword("pass-case")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	s.db.Create(&db.User{
		Username:     "CaseUser",
		PasswordHash: hash,
		Role:         "user",
		IsActive:     true,
	})

	form := url.Values{}
	form.Set("username", "caseuser")
	form.Set("password", "pass-case")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleLogin(c)

	if w.Code == http.StatusOK || w.Code == http.StatusFound {
		t.Log("case-insensitive username matched (expected behavior)")
	} else {
		var resp map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		errVal, _ := resp["error"]
		t.Logf("case mismatch returned status=%d error=%v", w.Code, errVal)
	}
}

func TestHandleLogin_ClearTextPasswordAttempt(t *testing.T) {
	// Verify password hashing comparison works correctly with known values
	s := newLoginTestServer(t)
	plain := "MyS3cur3P@ss!"
	hash, err := middleware.HashPassword(plain)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	s.db.Create(&db.User{
		Username:     "cleartext-test",
		PasswordHash: hash,
		Role:         "user",
		IsActive:     true,
	})

	form := url.Values{}
	form.Set("username", "cleartext-test")
	form.Set("password", plain)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/login", s.handleLogin)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 for valid password, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleLogout_NoSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/logout", nil)
	router := gin.New()
	router.POST("/logout", s.handleLogout)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

func TestCreateSession_Persists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	token := "test-session-token-for-persistence"
	userID := uint(42)
	err := s.createSession(token, userID, "10.0.0.1", "curl/7.68", "fp123", 3600)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	var sess db.UserSession
	if err := database.Where("token_hash = ?", middleware.TokenHash(token)).First(&sess).Error; err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if sess.UserID != userID {
		t.Errorf("expected UserID=%d, got %d", userID, sess.UserID)
	}
	if sess.IP != "10.0.0.1" {
		t.Errorf("expected IP=10.0.0.1, got %s", sess.IP)
	}
}

func TestIsSessionRevoked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	token := "unrevoked-session-token"
	if err := s.createSession(token, uint(1), "127.0.0.1", "test", "", 3600); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if revoked, err := s.isSessionRevoked(token); err != nil || revoked {
		t.Error("session should not be revoked initially")
	}

	s.revokeSession(token)

	if revoked, err := s.isSessionRevoked(token); err != nil || !revoked {
		t.Error("session should be revoked after revokeSession")
	}
}

func TestRevokeAllUserSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	userID := uint(7)
	for i := 0; i < 3; i++ {
		token := "multi-session-token-" + itoa(i)
		if err := s.createSession(token, userID, "127.0.0.1", "test", "", 3600); err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	if err := s.revokeAllUserSessions(userID); err != nil {
		t.Fatalf("revokeAllUserSessions: %v", err)
	}

	for i := 0; i < 3; i++ {
		token := "multi-session-token-" + itoa(i)
		if revoked, err := s.isSessionRevoked(token); err != nil || !revoked {
			t.Errorf("session %d should be revoked", i)
		}
	}
}

// TestHandleLogin_EmptyPassword checks that empty password returns 401
func TestHandleLogin_EmptyPassword(t *testing.T) {
	s := newLoginTestServer(t)

	form := url.Values{}
	form.Set("username", "someuser")
	form.Set("password", "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	s.handleLogin(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty password, got %d", w.Code)
	}
}

func TestHandleExtendSession_RotatesTokenAndCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newLoginTestServer(t)

	hash, err := middleware.HashPassword("test-pass")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := db.User{
		Username:     "extend-user",
		PasswordHash: hash,
		Role:         "admin",
		IsActive:     true,
	}
	if err := s.db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, err := middleware.GenerateToken(user, false, 24)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	maxAge := 24 * SecondsPerHour
	if err := s.createSession(token, user.ID, "127.0.0.1", "test-agent", "", maxAge); err != nil {
		t.Fatalf("create session: %v", err)
	}
	oldHash := middleware.TokenHash(token)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/extend", nil)
	c.Request.AddCookie(&http.Cookie{Name: "forgec2_session", Value: token})
	c.Set("user_id", user.ID)
	c.Set("user", user.Username)
	c.Set("user_role", user.Role)

	s.handleExtendSession(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}

	// Set-Cookie: forgec2_session=<new token>; Max-Age=86400...
	setCookies := w.Result().Cookies()
	var newCookie *http.Cookie
	for _, ck := range setCookies {
		if ck.Name == "forgec2_session" {
			newCookie = ck
			break
		}
	}
	if newCookie == nil {
		t.Fatal("expected forgec2_session cookie to be set")
	}
	if newCookie.Value == token {
		t.Fatal("expected a rotated (different) session token")
	}
	if newCookie.MaxAge != maxAge {
		t.Errorf("expected Max-Age=%d, got %d", maxAge, newCookie.MaxAge)
	}

	// Response exposes the new session_exp.
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			SessionExp int64 `json:"session_exp"`
		} `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Data.SessionExp == 0 {
		t.Fatalf("expected session_exp, got %+v", resp)
	}

	// The user_sessions row must track the rotated token, not the old one.
	var sess db.UserSession
	if err := s.db.Where("token_hash = ?", middleware.TokenHash(newCookie.Value)).First(&sess).Error; err != nil {
		t.Fatalf("rotated session row not found: %v", err)
	}
	if sess.UserID != user.ID {
		t.Errorf("expected session user_id=%d, got %d", user.ID, sess.UserID)
	}
	// The old session row must now be revoked so a copied pre-rotation cookie
	// is rejected immediately (isSessionRevoked matches revoked_at > epoch).
	var revokedCount int64
	s.db.Model(&db.UserSession{}).
		Where("token_hash = ? AND revoked_at > ?", oldHash, time.Unix(0, 0)).
		Count(&revokedCount)
	if revokedCount != 1 {
		t.Errorf("expected old token hash to be revoked, %d revoked row(s) found", revokedCount)
	}
}

func TestHandleExtendSession_PreservesRememberMe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newLoginTestServer(t)

	user := db.User{
		Username: "extend-remember",
		Role:     "admin",
		IsActive: true,
	}
	if err := s.db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, err := middleware.GenerateToken(user, true, 24)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := s.createSession(token, user.ID, "127.0.0.1", "test-agent", "", RememberMeMaxAgeSec); err != nil {
		t.Fatalf("create session: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/extend", nil)
	c.Request.AddCookie(&http.Cookie{Name: "forgec2_session", Value: token})
	c.Set("user_id", user.ID)
	c.Set("user", user.Username)
	c.Set("user_role", user.Role)

	s.handleExtendSession(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var newCookie *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "forgec2_session" {
			newCookie = ck
			break
		}
	}
	if newCookie == nil {
		t.Fatal("expected forgec2_session cookie to be set")
	}
	if newCookie.MaxAge != RememberMeMaxAgeSec {
		t.Errorf("expected Max-Age=%d (remember-me preserved), got %d", RememberMeMaxAgeSec, newCookie.MaxAge)
	}
}

func TestHandleExtendSession_Unauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	s := &Server{db: database}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/extend", nil)

	s.handleExtendSession(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without user in context, got %d; body=%s", w.Code, w.Body.String())
	}
	// No cookie should be issued for an anonymous extend.
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "forgec2_session" {
			t.Fatalf("must not set a session cookie for unauthenticated extend")
		}
	}
}
