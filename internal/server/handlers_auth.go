package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	dummyPasswordHash string
	dummyHashOnce     sync.Once
)

// dummyHash returns a valid bcrypt hash used to equalize login timing for
// unknown users / disabled accounts. Without it, a user-not-found branch
// returns far faster than a wrong-password branch, leaking username existence.
func dummyHash() string {
	dummyHashOnce.Do(func() {
		h, err := middleware.HashPassword("forgec2-timing-equalizer-dummy")
		if err == nil {
			dummyPasswordHash = h
		}
	})
	return dummyPasswordHash
}

func (s *Server) handleLoginPage(c *gin.Context) {
	if tokenStr, err := c.Cookie("forgec2_session"); err == nil && tokenStr != "" {
		if _, err := middleware.ParseToken(tokenStr); err == nil {
			c.Redirect(http.StatusFound, "/")
			return
		}
		middleware.SetCookieWithSameSite(c, "forgec2_session", "", -1, "/", middleware.CookieSecure, true, http.SameSiteLaxMode)
	}

	s.renderLoginPage(c, gin.H{
		"Title": "ForgeC2 - Login",
		"Error": "",
	})
}

func (s *Server) renderLoginPage(c *gin.Context, data gin.H) {
	s.renderPageOrJSON(c, data)
}

func (s *Server) renderLoginError(c *gin.Context, errMsg string) {
	respondError(c, http.StatusUnauthorized, errMsg)
}

func (s *Server) handleLogin(c *gin.Context) {

	username := c.PostForm("username")
	password := c.PostForm("password")
	rememberMe := c.PostForm("remember_me") == "on"
	clientIP := c.ClientIP()

	slog.Info("Login attempt", "ip", clientIP)

	// Note: no audit record is written for requests rejected while in lockout —
	// the failures that triggered the lockout were already audited, and writing one
	// per subsequent attempt lets an attacker flood the audit log during a lockout.
	if locked, retryAfter := s.checkLoginLockout(clientIP); locked {
		s.renderLoginError(c, fmt.Sprintf("Too many login attempts. Try again in %d seconds.", retryAfter))
		return
	}

	if locked, retryAfter := s.checkAccountLockout(username); locked {
		s.renderLoginError(c, fmt.Sprintf("Account temporarily locked. Try again in %d seconds.", retryAfter))
		return
	}

	if username == "" || password == "" {
		s.renderLoginError(c, "Username and password required")
		return
	}

	if middleware.RequireTLSForAuth && !middleware.IsSecureConnection(c) {
		slog.Warn("Login blocked: RequireTLSForAuth=true but connection is not TLS",
			"username", username, "ip", c.ClientIP())
		s.renderLoginError(c, "Login requires a secure (HTTPS) connection")
		return
	}

	var user db.User
	result := s.db.Where("username = ?", username).First(&user)
	if result.Error != nil {
		// Equalize timing with the wrong-password branch (bcrypt compare).
		if h := dummyHash(); h != "" {
			middleware.CheckPassword(h, password)
		}
		slog.Warn("Login failed: invalid credentials", "ip", c.ClientIP())
		s.LogAuditRecord(c, "login_failed", "auth", username, "User not found", false, nil)
		if locked, retryAfter := s.recordLoginFailure(clientIP, username); locked {
			s.renderLoginError(c, fmt.Sprintf("Too many login attempts. Try again in %d seconds.", retryAfter))
			return
		}
		s.renderLoginError(c, "Invalid username or password")
		return
	}

	if !user.IsActive {
		// Equalize timing with the wrong-password branch.
		if h := dummyHash(); h != "" {
			middleware.CheckPassword(h, password)
		}
		slog.Warn("Login failed: account disabled", "username", username, "ip", c.ClientIP())
		s.LogAuditRecord(c, "login_failed", "auth", username, "Account disabled", false, nil)
		s.recordLoginFailure(clientIP, username)
		s.renderLoginError(c, "Invalid username or password")
		return
	}

	if user.PasswordHash == "" {
		slog.Warn("Login failed: user has no password hash (admin reset required)", "username", username, "ip", c.ClientIP())
		s.LogAuditRecord(c, "login_failed", "auth", username, "Empty password hash", false, nil)
		if h := dummyHash(); h != "" {
			middleware.CheckPassword(h, password)
		}
		if locked, retryAfter := s.recordLoginFailure(clientIP, username); locked {
			s.renderLoginError(c, fmt.Sprintf("Too many login attempts. Try again in %d seconds.", retryAfter))
			return
		}
		s.renderLoginError(c, "Invalid username or password")
		return
	} else if !middleware.CheckPassword(user.PasswordHash, password) {
		slog.Warn("Login failed: invalid credentials", "ip", c.ClientIP())
		s.LogAuditRecord(c, "login_failed", "auth", username, "Wrong password", false, nil)
		if err := s.db.Model(&user).UpdateColumn("login_attempts", gorm.Expr("login_attempts + 1")).Error; err != nil {
			slog.Error("Failed to increment login attempts", "user_id", user.ID, "err", err)
		}
		if locked, retryAfter := s.recordLoginFailure(clientIP, username); locked {
			s.renderLoginError(c, fmt.Sprintf("Too many login attempts. Try again in %d seconds.", retryAfter))
			return
		}
		if locked, retryAfter := s.recordAccountLoginFailure(username, clientIP); locked {
			s.renderLoginError(c, fmt.Sprintf("Account temporarily locked. Try again in %d seconds.", retryAfter))
			return
		}
		s.renderLoginError(c, "Invalid username or password")
		return
	} else {
		if err := s.db.Model(&user).Updates(map[string]interface{}{
			"last_login":     time.Now(),
			"last_ip":        c.ClientIP(),
			"login_attempts": 0,
		}).Error; err != nil {
			slog.Error("Failed to update login success", "username", username, "err", err)
		}
	}

	s.configMu.RLock()
	sessionMaxAgeHours := s.cfg.Server.SessionMaxAgeHours
	s.configMu.RUnlock()

	token, err := middleware.GenerateToken(user, rememberMe, sessionMaxAgeHours)
	if err != nil {
		s.renderLoginError(c, "Token generation error")
		return
	}

	sessionHours := sessionMaxAgeHours
	if sessionHours < 1 {
		sessionHours = DefaultSessionHours
	}
	maxAge := sessionHours * SecondsPerHour
	if rememberMe {
		maxAge = RememberMeMaxAgeSec
	}
	middleware.SetCookieWithSameSite(c, "forgec2_session", token, maxAge, "/", middleware.CookieSecure, true, http.SameSiteLaxMode)

	// Fail closed when the CSRF binding key is unavailable: issuing a session
	// without its CSRF cookie only produces confusing 403s on every later
	// mutation. Check before createSession so no orphaned session row is left.
	csrfSecret := middleware.GetCSRFSecret()
	if csrfSecret == nil {
		slog.Error("CSRF secret unavailable during login, refusing session")
		respondError(c, http.StatusInternalServerError, "server misconfigured (CSRF unavailable)")
		return
	}
	// New-IP sighting must be evaluated BEFORE createSession inserts the
	// current login's own row (which would otherwise always self-match).
	newIP := !s.isKnownLoginIP(user.ID, clientIP)
	if err := s.createSession(token, user.ID, c.ClientIP(), c.Request.UserAgent(), "", maxAge); err != nil {
		slog.Error("Failed to create session during login", "user_id", user.ID, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to create session")
		return
	}

	csrfToken := middleware.DeriveCSRFToken(token, csrfSecret)
	middleware.SetCookieWithSameSite(c, "forgec2_csrf", csrfToken, 0, "/", middleware.CookieSecure, false, http.SameSiteLaxMode)

	s.clearLoginLockout(clientIP)
	s.loginLockout.resetAccount(username)
	if err := s.db.Model(&db.User{}).Where("id = ?", user.ID).Update("force_logout_at", nil).Error; err != nil {
		slog.Error("Failed to clear force_logout_at", "user_id", user.ID, "err", err)
	}

	if s.pluginManager != nil {
		// Copy values out: gin.Context must not be touched from another
		// goroutine (data race on request state, masked as 500s by Recovery).
		hookUserID, hookUsername, hookRole, hookIP := user.ID, user.Username, user.Role, clientIP
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.pluginManager.ExecuteHook(s.ctx, plugin.Event{
				Type:      plugin.EventUserLogin,
				Timestamp: time.Now(),
				UserID:    hookUserID,
				Payload: map[string]interface{}{
					"username": hookUsername,
					"role":     hookRole,
					"ip":       hookIP,
				},
			}); err != nil {
				slog.Warn("Hook errors on user_login event", "user_id", hookUserID, "err", err)
			}
		}()
	}

	s.LogAuditRecord(c, "login", "auth", username, "Login successful", true, nil)
	// First login ever from this address is worth an audit row even when
	// everything else checks out (session-theft / credential-sharing signal).
	if newIP {
		s.LogAuditRecord(c, "login_new_ip", "auth", username, "First login from "+clientIP, true, nil)
		slog.Warn("First login from new IP", "username", username, "ip", clientIP)
	}
	// Concurrency cap: evict oldest beyond auth.session_max_concurrent
	// (0 = unlimited). Runs after insert so the fresh login survives.
	s.configMu.RLock()
	maxConcurrent := s.cfg.Auth.SessionMaxConcurrent
	s.configMu.RUnlock()
	if evicted := s.enforceSessionCap(user.ID, maxConcurrent); evicted > 0 {
		s.LogAuditRecord(c, "session_evict", "auth", username, fmt.Sprintf("Evicted %d oldest session(s) over the concurrency cap", evicted), true, nil)
		slog.Warn("Evicted excess sessions", "username", username, "evicted", evicted)
	}
	slog.Info("Login successful, session cookie set",
		"username", username,
		"role", user.Role,
		"ip", c.ClientIP(),
		"max_age", maxAge,
		"secure", middleware.CookieSecure)

	c.Redirect(http.StatusFound, "/")
}

func (s *Server) handleLogout(c *gin.Context) {
	user, exists := c.Get("user")
	username := "unknown"
	if exists {
		if u, ok := user.(string); ok {
			username = u
		}
	}
	if s.pluginManager != nil {
		uid, _ := c.Get("user_id")
		userID, _ := uid.(uint)
		// Copy values out: gin.Context must not be touched from another
		// goroutine (data race on request state, masked as 500s by Recovery).
		hookUserID, hookUsername, hookIP := userID, username, c.ClientIP()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.pluginManager.ExecuteHook(s.ctx, plugin.Event{
				Type:      plugin.EventUserLogout,
				Timestamp: time.Now(),
				UserID:    hookUserID,
				Payload: map[string]interface{}{
					"username": hookUsername,
					"ip":       hookIP,
				},
			}); err != nil {
				slog.Warn("Hook errors on user_logout event", "username", hookUsername, "err", err)
			}
		}()
	}

	s.LogAuditRecord(c, "logout", "auth", username, "User logged out", true, nil)
	slog.Info("User logged out", "username", username, "ip", c.ClientIP())
	if tokenStr, err := c.Cookie("forgec2_session"); err == nil && tokenStr != "" {
		s.revokeSession(tokenStr)
	}
	middleware.SetCookieWithSameSite(c, "forgec2_session", "", -1, "/", middleware.CookieSecure, true, http.SameSiteLaxMode)
	c.Redirect(http.StatusFound, "/login")
}

func (s *Server) handleGetCurrentUser(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		respondError(c, http.StatusUnauthorized, "not authenticated")
		return
	}

	var user db.User
	if err := s.db.First(&user, userID).Error; err != nil {
		respondError(c, http.StatusUnauthorized, "user not found")
		return
	}

	if !user.IsActive {
		respondError(c, http.StatusForbidden, "account is disabled")
		return
	}

	data := gin.H{
		"id":          user.ID,
		"username":    user.Username,
		"role":        user.Role,
		"permissions": s.permissionsForRole(user.Role),
	}
	// Expose session expiry (ms epoch) for client-side timeout warnings.
	if exp, ok := c.Get("session_exp"); ok {
		if t, ok := exp.(time.Time); ok {
			data["session_exp"] = t.UnixMilli()
		}
	}
	respond(c, gin.H{

		"data": data,
	})
}

// permissionsForRole resolves the effective permission set for a user role.
// Admin overrides everything (all permissions); built-in roles read
// db.RolePermissionsMap; custom roles read the DB-backed custom_roles row —
// mirroring middleware.RoleHasPermissionDB so /api/me agrees with the
// per-route enforcement.
func (s *Server) permissionsForRole(role string) []string {
	if role == db.RoleAdmin {
		return db.GetAllPermissions()
	}
	if _, ok := db.RolePermissionsMap[role]; ok {
		return db.GetPermissionsForRole(role)
	}
	var customRole db.CustomRole
	if err := s.db.Where("name = ?", role).First(&customRole).Error; err != nil || customRole.Permissions == "" {
		return nil
	}
	var perms []string
	if err := json.Unmarshal([]byte(customRole.Permissions), &perms); err != nil {
		return nil
	}
	return perms
}

// handleExtendSession re-issues the session JWT and replaces the cookie,
// sliding the expiry forward. remember-me semantics are preserved from the
// original session row (long-lived sessions stay long-lived). Force-logout
// and per-session revocation checks keep working because the new token
// carries a fresh IssuedAt/jti and the updated row is tracked in user_sessions.
func (s *Server) handleExtendSession(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		respondError(c, http.StatusUnauthorized, "not authenticated")
		return
	}

	var user db.User
	if err := s.db.First(&user, userID).Error; err != nil {
		respondError(c, http.StatusUnauthorized, "user not found")
		return
	}
	if !user.IsActive {
		respondError(c, http.StatusForbidden, "account is disabled")
		return
	}

	tokenStr, err := c.Cookie("forgec2_session")
	if err != nil || tokenStr == "" {
		respondError(c, http.StatusUnauthorized, "no session token")
		return
	}

	// Preserve remember-me from the original session window length.
	rememberMe := false
	var sess db.UserSession
	if err := s.db.Where("token_hash = ?", middleware.TokenHash(tokenStr)).First(&sess).Error; err == nil {
		rememberMe = sess.ExpiresAt.Sub(sess.CreatedAt) > 48*time.Hour
	}

	s.configMu.RLock()
	sessionMaxAgeHours := s.cfg.Server.SessionMaxAgeHours
	s.configMu.RUnlock()

	newToken, err := middleware.GenerateToken(user, rememberMe, sessionMaxAgeHours)
	if err != nil {
		slog.Error("Failed to extend session token", "user_id", user.ID, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to extend session")
		return
	}

	sessionHours := sessionMaxAgeHours
	if sessionHours < 1 {
		sessionHours = DefaultSessionHours
	}
	maxAge := sessionHours * SecondsPerHour
	if rememberMe {
		maxAge = RememberMeMaxAgeSec
	}
	middleware.SetCookieWithSameSite(c, "forgec2_session", newToken, maxAge, "/", middleware.CookieSecure, true, http.SameSiteLaxMode)

	// Rotate the session: revoke the old token's row so a copied (pre-rotation)
	// cookie is invalidated immediately, then track the new token in a fresh
	// row. Overwriting token_hash on the old row previously left the old token
	// in JWT-only limbo where the revocation check could never match, so a
	// stolen cookie kept working for the rest of its original lifetime.
	now := time.Now()
	newExpiry := now.Add(time.Duration(maxAge) * time.Second)
	if err := s.db.Model(&db.UserSession{}).
		Where("user_id = ? AND token_hash = ? AND revoked_at <= ?", user.ID, middleware.TokenHash(tokenStr), time.Unix(0, 0)).
		Update("revoked_at", now).Error; err != nil {
		slog.Error("Failed to revoke old session on extend", "user_id", user.ID, "err", err)
	}
	newSess := db.UserSession{
		UserID:            user.ID,
		TokenHash:         middleware.TokenHash(newToken),
		IP:                sess.IP,
		UserAgent:         sess.UserAgent,
		DeviceFingerprint: sess.DeviceFingerprint,
		ExpiresAt:         newExpiry,
		CreatedAt:         now,
	}
	if err := s.db.Create(&newSess).Error; err != nil {
		slog.Error("Failed to insert rotated session row", "user_id", user.ID, "err", err)
	}

	s.LogAuditRecord(c, "session_extend", "auth", user.Username, "Session extended", true, nil)
	slog.Info("Session extended", "username", user.Username, "ip", c.ClientIP(), "max_age", maxAge)

	exp := now.Add(time.Duration(maxAge) * time.Second).UnixMilli()
	respond(c, gin.H{

		"data": gin.H{"session_exp": exp},
	})
}
