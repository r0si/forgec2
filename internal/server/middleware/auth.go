package middleware

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const minJWTSecretLen = 32

var jwtSecret atomic.Value

// prevJWTSecret holds the pre-rotation signing key so in-flight operator
// sessions survive a jwt_secret hot-reload. Entries expire after
// jwtGracePeriod; rotation timestamps come from rotation time, not config.
var prevJWTSecret atomic.Value // jwtGraceEntry | nil

type jwtGraceEntry struct {
	key     []byte
	expires time.Time
}

// jwtGracePeriod bounds how long pre-rotation tokens stay valid.
const jwtGracePeriod = 5 * time.Minute

// jwtSigningKeys returns the current key first, then the unexpired previous
// key (if any) for rotation grace.
func jwtSigningKeys() [][]byte {
	keys := [][]byte{}
	if cur, ok := jwtSecret.Load().([]byte); ok && len(cur) > 0 {
		keys = append(keys, cur)
	}
	if prev, ok := prevJWTSecret.Load().(jwtGraceEntry); ok && len(prev.key) > 0 && time.Now().Before(prev.expires) {
		keys = append(keys, prev.key)
	}
	return keys
}

// trustedProxyIPs holds the operator-configured proxy IPs/CIDRs whose
// X-Forwarded-* headers are trusted (set via SetTrustedProxyIPs).
var trustedProxyIPs atomic.Value // []netip.Prefix

// SetTrustedProxyIPs configures the reverse proxies whose X-Forwarded-Proto
// header IsSecureConnection may trust. An empty list disables header trust.
func SetTrustedProxyIPs(proxies []string) {
	prefixes := make([]netip.Prefix, 0, len(proxies))
	for _, p := range proxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if addr, err := netip.ParseAddr(p); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		if pref, err := netip.ParsePrefix(p); err == nil {
			prefixes = append(prefixes, pref.Masked())
		}
	}
	trustedProxyIPs.Store(prefixes)
}

func peerAddr(c *gin.Context) netip.Addr {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(host)); err == nil {
		return addr.Unmap()
	}
	return netip.Addr{}
}

func isTrustedPeer(c *gin.Context) bool {
	prefixes, _ := trustedProxyIPs.Load().([]netip.Prefix)
	if len(prefixes) == 0 {
		return false
	}
	addr := peerAddr(c)
	if !addr.IsValid() {
		return false
	}
	for _, pref := range prefixes {
		if pref.Contains(addr) {
			return true
		}
	}
	return false
}

// apiKeyRateLimiter provides simple per-IP rate limiting for API key auth.
type apiKeyRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*apiKeyLimitEntry
}

type apiKeyLimitEntry struct {
	attempts    int
	windowStart time.Time
}

const (
	apiKeyMaxAttempts = 10
	apiKeyWindowSec   = 60
	apiKeyLockoutSec  = 300
)

func (r *apiKeyRateLimiter) isLocked(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[ip]
	if !ok {
		return false
	}
	if !entry.windowStart.IsZero() && time.Since(entry.windowStart) > time.Duration(apiKeyLockoutSec)*time.Second {
		delete(r.entries, ip)
		return false
	}
	return entry.attempts >= apiKeyMaxAttempts
}

func (r *apiKeyRateLimiter) recordFailure(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[ip]
	if !ok {
		if len(r.entries) >= 10000 {
			r.evictLocked()
		}
		entry = &apiKeyLimitEntry{windowStart: time.Now()}
		r.entries[ip] = entry
	}
	entry.attempts++
	entry.windowStart = time.Now()
}

// evictLocked frees space in the entries map at capacity: first drops entries
// past the lockout window, then evicts the oldest entry (by windowStart)
// instead of a random one so active IPs are not displaced.
func (r *apiKeyRateLimiter) evictLocked() {
	now := time.Now()
	for k, e := range r.entries {
		if now.Sub(e.windowStart) > time.Duration(apiKeyLockoutSec)*time.Second {
			delete(r.entries, k)
		}
	}
	if len(r.entries) < 10000 {
		return
	}
	var oldestK string
	var oldestT time.Time
	for k, e := range r.entries {
		if oldestK == "" || e.windowStart.Before(oldestT) {
			oldestK = k
			oldestT = e.windowStart
		}
	}
	if oldestK != "" {
		delete(r.entries, oldestK)
	}
}

var globalAPIKeyLimiter = &apiKeyRateLimiter{
	entries: make(map[string]*apiKeyLimitEntry),
}

// respondError sends a consistent {success: false, error: msg} envelope.
func respondError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"success": false, "error": msg})
}

// CookieSecure controls the Secure flag on all cookies (set by InitJWTSecret from config)
var CookieSecure bool

// CookieDomain controls the Domain attribute on all cookies (set by InitJWTSecret from config)
var CookieDomain string

// RequireTLSForAuth when true, prevents session cookies from being issued
// unless the connection is TLS (X-Forwarded-Proto: https or direct TLS).
var RequireTLSForAuth bool

const (
	JWTExpiry     = 24 * time.Hour
	JWTLongExpiry = 7 * 24 * time.Hour // "remember me"
	userCacheTTL  = 5 * time.Minute
)

type authCacheEntry struct {
	user      db.User
	expiresAt time.Time
}

// TenantIDContextKey carries the authenticated user's tenant from the auth middleware to
// the request handlers, so tenant scoping does not need its own users lookup on every
// request. The value is a uint; handlers treat a missing value as "look it up".
const TenantIDContextKey = "tenant_id"

// globalUserCache is shared by every AuthRequired instance (web UI, REST
// API, misc routes) so a single invalidation covers all of them. Entries
// are short-TTL replicas of the users table: disable/force-logout paths
// must call InvalidateUserCache, otherwise a disabled user keeps the cached
// record (and their sessions) until TTL expiry.
var (
	globalUserCacheMu sync.RWMutex
	globalUserCache   = make(map[uint]authCacheEntry)
)

// InvalidateUserCache drops a user's cached record so the next request
// re-reads the users table. Call it whenever a user's active state,
// role, or ForceLogoutAt changes (disable, force-logout, delete).
func InvalidateUserCache(userID uint) {
	globalUserCacheMu.Lock()
	delete(globalUserCache, userID)
	globalUserCacheMu.Unlock()
}

// Claims for JWT
type Claims struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// InitJWTSecret initializes the JWT secret and cookie secure flag from config.
// If the secret is shorter than 32 characters, it auto-generates a secure random
// secret and logs a warning.
func InitJWTSecret(cfg *config.Config, configPath string) error {
	secret := cfg.Server.JWTSecret
	if secret == "" {
		slog.Warn("JWT secret is empty, auto-generating a secure random secret")
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("JWT secret is empty and cannot generate random secret: %w", err)
		}
		cfg.Server.JWTSecret = hex.EncodeToString(b)
		if configPath != "" {
			if err := cfg.Save(configPath); err != nil {
				slog.Error("Failed to save auto-generated JWT secret to config", "error", err)
			}
		}
		secret = cfg.Server.JWTSecret
	}
	if len(secret) < minJWTSecretLen {
		slog.Warn("JWT secret is too short, auto-generating a secure random secret",
			"length", len(secret), "min_required", minJWTSecretLen)
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("JWT secret is too short and cannot generate random secret: %w", err)
		}
		cfg.Server.JWTSecret = hex.EncodeToString(b)
		if configPath != "" {
			if err := cfg.Save(configPath); err != nil {
				slog.Error("Failed to save auto-generated JWT secret to config", "error", err)
			}
		}
		secret = cfg.Server.JWTSecret
	}
	jwtRotateSecret(secret)
	CookieSecure = cfg.Server.TLSEnabled
	CookieDomain = cfg.Server.CookieDomain
	RequireTLSForAuth = cfg.Server.RequireTLSForAuth
	if !CookieSecure {
		if RequireTLSForAuth {
			slog.Warn("Cookie Secure flag disabled but RequireTLSForAuth=true — session cookies will NOT be set on non-TLS connections")
		} else {
			slog.Warn("Cookie Secure flag is disabled (TLS not enabled) — session cookies will be sent over plain HTTP")
		}
	}
	return nil
}

// jwtRotateSecret installs a new signing key, keeping the previous one for
// jwtGracePeriod so hot-reload rotation does not kick every operator out.
// Idempotent for identical values (file watcher double-fires, restarts).
func jwtRotateSecret(secret string) {
	if cur, ok := jwtSecret.Load().([]byte); ok && string(cur) == secret {
		return
	}
	if cur, ok := jwtSecret.Load().([]byte); ok && len(cur) > 0 {
		prevJWTSecret.Store(jwtGraceEntry{key: cur, expires: time.Now().Add(jwtGracePeriod)})
	}
	jwtSecret.Store([]byte(secret))
}

// parseClaimsWithGrace parses HMAC claims trying the current key first,
// then the unexpired rotation-grace key, so hot-reload rotation does not
// kick operators out mid-session.
func parseClaimsWithGrace(tokenStr string, claims *Claims) (*jwt.Token, error) {
	keys := jwtSigningKeys()
	if len(keys) == 0 {
		return nil, fmt.Errorf("no JWT signing key configured")
	}
	var lastErr error
	for _, key := range keys {
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return key, nil
		})
		if err == nil && token.Valid {
			return token, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("invalid token")
	}
	return nil, lastErr
}

// Gin's SetCookie does not support SameSite, so we use http.SetCookie directly.
func SetCookieWithSameSite(c *gin.Context, name, value string, maxAge int, path string, secure, httpOnly bool, sameSite http.SameSite) {
	domain := CookieDomain
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		Secure:   secure,
		HttpOnly: httpOnly,
		SameSite: sameSite,
	})
}

func isWebSocketUpgrade(c *gin.Context) bool {
	return strings.EqualFold(c.GetHeader("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(c.GetHeader("Connection")), "upgrade")
}

// IsSecureConnection returns true if the request arrived over TLS, either
// directly or via a trusted reverse proxy that sets X-Forwarded-Proto: https.
// The X-Forwarded-Proto header is only honored when the direct peer is a
// configured trusted proxy; otherwise any client could spoof it on a plaintext
// connection.
func IsSecureConnection(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	if !isTrustedPeer(c) {
		return false
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

// wantsJSONAuth prefers a JSON 401 over HTML redirect for API clients and API paths.
// Browser navigations (text/html Accept, page paths) still redirect to /login.
func wantsJSONAuth(c *gin.Context) bool {
	if isWebSocketUpgrade(c) {
		return true
	}
	accept := c.GetHeader("Accept")
	if strings.Contains(accept, "application/json") {
		return true
	}
	if strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		return true
	}
	if c.GetHeader("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	if c.GetHeader("X-CSRF-Token") != "" {
		return true
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api") ||
		strings.HasPrefix(path, "/ws") ||
		strings.HasPrefix(path, "/extc2") ||
		strings.HasPrefix(path, "/admin/") ||
		strings.HasPrefix(path, "/debug/") {
		return true
	}
	return false
}

func authFail(c *gin.Context, logMsg string, args ...any) {
	if len(args) > 0 {
		slog.Warn(logMsg, args...)
	} else {
		slog.Debug(logMsg)
	}
	if wantsJSONAuth(c) {
		respondError(c, http.StatusUnauthorized, "unauthorized")
	} else {
		c.Redirect(http.StatusFound, "/login")
	}
	c.Abort()
}

// clearSessionCookie removes the forgec2_session cookie (used on auth failures).
func clearSessionCookie(c *gin.Context) {
	SetCookieWithSameSite(c, "forgec2_session", "", -1, "/", CookieSecure, true, http.SameSiteLaxMode)
}

// AuthRequired middleware for web UI - validates JWT + DB user active
func AuthRequired(database *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// API key authentication (X-API-Key header)
		if apiKey := c.GetHeader("X-API-Key"); apiKey != "" {
			clientIP := c.ClientIP()
			if globalAPIKeyLimiter.isLocked(clientIP) {
				authFail(c, "Auth failed: API key rate limit exceeded", "path", c.Request.URL.Path, "ip", clientIP)
				return
			}
			h := sha256.Sum256([]byte(apiKey))
			hash := hex.EncodeToString(h[:])
			var ak db.APIKey
			if database.Where("key_hash = ? AND active = ?", hash, true).First(&ak).Error == nil {
				if ak.ExpiresAt.IsZero() || time.Now().Before(ak.ExpiresAt) {
					var user db.User
					if database.Where("id = ? AND is_active = ?", ak.UserID, true).First(&user).Error == nil {
						// Scoped keys are source-bound: a stolen key is only
						// useful from the owner's networks.
						if !db.KeyCIDRAllows(ak.AllowedCIDRs, clientIP) {
							authFail(c, "Auth failed: API key not allowed from this IP", "path", c.Request.URL.Path, "ip", clientIP)
							return
						}
						database.Model(&ak).Update("last_used", time.Now())
						c.Set("user_id", user.ID)
						c.Set("user", user.Username)
						c.Set("user_role", user.Role)
						// The tenant travels with the authenticated row so the server does
						// not have to re-SELECT users on every scoped request. The handler
						// side falls back to a lookup when this is absent.
						c.Set(TenantIDContextKey, user.TenantID)
						// nil = legacy full-access key; non-nil = capability
						// set enforced by RequirePermission/RequireRole.
						c.Set("api_key_scopes", db.KeyScopeSet(ak.Scopes))
						c.Set("api_key_id", ak.ID)
						// API-key requests carry no session cookie; CSRFProtect
						// skips validation for them (the bearer header itself
						// is not attachable cross-origin).
						c.Set("auth_via_api_key", true)
						c.Next()
						return
					}
				}
			}
			globalAPIKeyLimiter.recordFailure(clientIP)
			authFail(c, "Auth failed: invalid API key", "path", c.Request.URL.Path, "ip", clientIP)
			return
		}

		tokenStr, err := c.Cookie("forgec2_session")
		if err != nil {
			authFail(c, "Auth failed: no session token", "path", c.Request.URL.Path, "ip", c.ClientIP())
			return
		}
		if tokenStr == "" {
			authFail(c, "Auth failed: no session token", "path", c.Request.URL.Path, "ip", c.ClientIP())
			return
		}

		token, err := parseClaimsWithGrace(tokenStr, &Claims{})
		if err != nil || !token.Valid {
			clearSessionCookie(c)
			authFail(c, "Auth failed: invalid token", "path", c.Request.URL.Path, "ip", c.ClientIP(), "err", err)
			return
		}

		claims, ok := token.Claims.(*Claims)
		if !ok {
			clearSessionCookie(c)
			authFail(c, "Auth failed: invalid claims", "path", c.Request.URL.Path)
			return
		}

		// Verify user still exists and is active (with short TTL shared
		// cache — see InvalidateUserCache: admin disable/force-logout
		// paths evict the entry so the change takes effect immediately).
		var user db.User
		globalUserCacheMu.RLock()
		entry, cached := globalUserCache[claims.UserID]
		globalUserCacheMu.RUnlock()
		if cached && time.Now().Before(entry.expiresAt) {
			user = entry.user
		} else {
			if database.Where("id = ? AND is_active = ?", claims.UserID, true).First(&user).Error != nil {
				clearSessionCookie(c)
				authFail(c, "Auth failed: user not found or inactive", "user_id", claims.UserID, "username", claims.Username)
				return
			}
			globalUserCacheMu.Lock()
			globalUserCache[claims.UserID] = authCacheEntry{user: user, expiresAt: time.Now().Add(userCacheTTL)}
			// Amortized sweep: drop expired entries once the cache grows
			// past a threshold so it cannot grow unbounded over long uptimes.
			if len(globalUserCache) > 2048 {
				now := time.Now()
				for id, e := range globalUserCache {
					if now.After(e.expiresAt) {
						delete(globalUserCache, id)
					}
				}
			}
			globalUserCacheMu.Unlock()
		}

		c.Set(TenantIDContextKey, user.TenantID)

		// Force-logout check: if user's ForceLogoutAt > token IssuedAt, session was invalidated
		if !user.ForceLogoutAt.IsZero() && claims.IssuedAt != nil {
			if user.ForceLogoutAt.After(claims.IssuedAt.Time) {
				clearSessionCookie(c)
				if wantsJSONAuth(c) {
					respondError(c, http.StatusUnauthorized, "session_expired")
				} else {
					c.Redirect(http.StatusFound, "/login?error=session_expired")
				}
				c.Abort()
				return
			}
		}

		// Per-session revocation check (fail closed: a DB error must not
		// resurrect logged-out sessions; 503 for both JSON and browser
		// clients to avoid a redirect loop when the DB is down).
		revoked, err := isSessionRevoked(database, tokenStr)
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, "auth_unavailable")
			c.Abort()
			return
		}
		if revoked {
			clearSessionCookie(c)
			if wantsJSONAuth(c) {
				respondError(c, http.StatusUnauthorized, "session_revoked")
			} else {
				c.Redirect(http.StatusFound, "/login?error=session_revoked")
			}
			c.Abort()
			return
		}
		// Set user info in context
		c.Set("user_id", user.ID)
		c.Set("user", user.Username)
		c.Set("user_role", user.Role)
		if claims.ExpiresAt != nil {
			c.Set("session_exp", claims.ExpiresAt.Time)
		}
		c.Next()
	}
}

// GenerateToken creates a JWT for the session
func GenerateToken(user db.User, rememberMe bool, sessionMaxAgeHours int) (string, error) {
	expiry := time.Duration(sessionMaxAgeHours) * time.Hour
	if expiry <= 0 || expiry > JWTLongExpiry {
		expiry = JWTExpiry
	}
	if rememberMe {
		expiry = JWTLongExpiry
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}
	claims := &Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        hex.EncodeToString(jti),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret.Load().([]byte))
}

// CheckPassword compares hash
func CheckPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// BcryptCost is the bcrypt hash cost used for password hashing.
// Set via SetBcryptCost at startup from config.
var BcryptCost = 12

// SetBcryptCost sets the bcrypt cost for password hashing (clamped to [4, 31]).
func SetBcryptCost(cost int) {
	if cost >= 4 && cost <= 31 {
		BcryptCost = cost
	}
}

// HashPassword for storage
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	return string(hash), err
}

// RequireRole returns middleware that restricts access to specified roles
// apiKeyScopes returns the capability set for API-key callers: nil means
// either a session caller or a legacy full-access key (unconstrained).
func apiKeyScopes(c *gin.Context) (map[string]bool, bool) {
	if !c.GetBool("auth_via_api_key") {
		return nil, false
	}
	if v, ok := c.Get("api_key_scopes"); ok {
		if set, ok := v.(map[string]bool); ok {
			return set, true
		}
	}
	return nil, true
}

func RequireRole(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Capability keys never satisfy identity-role gates, even when the
		// owner is an admin: role routes (pprof, debug) are off-limits to
		// scoped keys by construction. Legacy full keys pass through.
		if set, isKey := apiKeyScopes(c); isKey && set != nil {
			respondError(c, http.StatusForbidden, "scoped API key denied")
			c.Abort()
			return
		}
		role, exists := c.Get("user_role")
		if !exists {
			respondError(c, http.StatusForbidden, "access denied")
			c.Abort()
			return
		}
		roleStr, ok := role.(string)
		if !ok {
			respondError(c, http.StatusForbidden, "access denied")
			c.Abort()
			return
		}
		for _, allowed := range allowedRoles {
			if roleStr == allowed {
				c.Next()
				return
			}
		}
		// "admin" overrides all role restrictions
		if roleStr == "admin" {
			c.Next()
			return
		}
		respondError(c, http.StatusForbidden, "insufficient permissions")
		c.Abort()
	}
}

func RequirePermission(permissions ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Scoped keys are capability-bound: at least one required perm
		// must be in the key's set. Checked BEFORE the admin shortcut so
		// an admin owner's scoped key cannot escape its bounds. Legacy
		// full keys (nil set) fall through to role logic unchanged.
		if set, isKey := apiKeyScopes(c); isKey && set != nil {
			for _, perm := range permissions {
				if set[perm] {
					c.Next()
					return
				}
			}
			respondError(c, http.StatusForbidden, "API key scope denied")
			c.Abort()
			return
		}
		role, exists := c.Get("user_role")
		if !exists {
			respondError(c, http.StatusForbidden, "access denied")
			c.Abort()
			return
		}
		roleStr, ok := role.(string)
		if !ok {
			respondError(c, http.StatusForbidden, "access denied")
			c.Abort()
			return
		}
		if roleStr == "admin" {
			c.Next()
			return
		}
		for _, perm := range permissions {
			if db.RoleHasPermission(roleStr, perm) {
				c.Next()
				return
			}
		}
		respondError(c, http.StatusForbidden, "insufficient permissions")
		c.Abort()
	}
}

func RequireAllPermissions(permissions ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("user_role")
		if !exists {
			respondError(c, http.StatusForbidden, "access denied")
			c.Abort()
			return
		}
		roleStr, ok := role.(string)
		if !ok {
			respondError(c, http.StatusForbidden, "access denied")
			c.Abort()
			return
		}
		if roleStr == "admin" {
			c.Next()
			return
		}
		for _, perm := range permissions {
			if !db.RoleHasPermission(roleStr, perm) {
				respondError(c, http.StatusForbidden, "insufficient permissions")
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// ParseToken validates a JWT token string and returns the claims.
func ParseToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := parseClaimsWithGrace(tokenStr, claims)
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

func TokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// isSessionRevoked reports whether a session token was revoked. A database
// error is returned (never swallowed): callers must fail closed (503), so a
// wedged database cannot resurrect logged-out sessions.
func isSessionRevoked(database *gorm.DB, tokenStr string) (bool, error) {
	hash := TokenHash(tokenStr)
	var one int
	// LIMIT 1 point lookup on indexed token_hash: COUNT would scan all matches.
	if err := database.Table("user_sessions").
		Select("1").
		Where("token_hash = ? AND revoked_at > ?", hash, time.Unix(0, 0)).
		Limit(1).
		Find(&one).Error; err != nil {
		slog.Warn("Session revocation check failed", "err", err)
		return false, err
	}
	return one == 1, nil
}
