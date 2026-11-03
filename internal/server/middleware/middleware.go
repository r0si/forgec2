package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter holds rate limiting data
type RateLimiter struct {
	visitors    map[string]*visitor
	mu          sync.Mutex
	limit       int           // requests per window
	window      time.Duration // time window
	maxVisitors int           // max entries before eviction
	// onLimit, when set, runs once per rejected request. The server uses it to
	// increment the beacon rate-limit metric without this package importing
	// Prometheus (or the server package).
	onLimit  func()
	stop     chan struct{}
	stopOnce sync.Once
}

type visitor struct {
	timestamp time.Time
	count     int
}

const defaultMaxVisitors = 100000

// NewRateLimiter creates a new rate limiter with periodic cleanup that stops on context cancellation
func NewRateLimiter(ctx context.Context, limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors:    make(map[string]*visitor),
		limit:       limit,
		window:      window,
		maxVisitors: defaultMaxVisitors,
		stop:        make(chan struct{}),
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Rate limiter cleanup panicked", "recover", r)
			}
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-rl.stop:
				return
			case <-ticker.C:
				rl.mu.Lock()
				now := time.Now()
				for ip, v := range rl.visitors {
					if now.Sub(v.timestamp) > rl.window*2 {
						delete(rl.visitors, ip)
					}
				}
				rl.mu.Unlock()
			}
		}
	}()
	return rl
}

// Stop terminates the cleanup goroutine. Idempotent per instance: each
// RateLimiter owns its stop channel, so stopping one must never leak or
// suppress the cleanup goroutines of other instances.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.stop)
	})
}

// SetLimits updates the request quota and window live (hot-reload). Visitor
// counters are preserved; only future windows use the new budget.
func (rl *RateLimiter) SetLimits(limit int, window time.Duration) {
	if limit <= 0 || window <= 0 {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limit = limit
	rl.window = window
}

// SetOnLimit installs a callback invoked whenever a request is rejected with
// 429. Passing nil removes it. The callback runs outside the limiter lock and
// must not block.
func (rl *RateLimiter) SetOnLimit(fn func()) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.onLimit = fn
}

// Snapshot returns the live quota for status reporting.
func (rl *RateLimiter) Snapshot() (limit int, window time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.limit, rl.window
}

// Limit returns a middleware handler for rate limiting
func (rl *RateLimiter) Limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}

		rl.mu.Lock()
		v, exists := rl.visitors[ip]
		now := time.Now()

		if !exists {
			if len(rl.visitors) >= rl.maxVisitors {
				// Evict the oldest visitor (by timestamp) rather than a
				// random one so active IPs are not displaced.
				var oldestIP string
				var oldestT time.Time
				for k, v := range rl.visitors {
					if oldestIP == "" || v.timestamp.Before(oldestT) {
						oldestIP = k
						oldestT = v.timestamp
					}
				}
				if oldestIP != "" {
					delete(rl.visitors, oldestIP)
				}
			}
			rl.visitors[ip] = &visitor{timestamp: now, count: 1}
			rl.mu.Unlock()
			c.Next()
			return
		}

		// Reset if window has passed
		if now.Sub(v.timestamp) > rl.window {
			v.timestamp = now
			v.count = 1
			rl.mu.Unlock()
			c.Next()
			return
		}

		// Check limit
		if v.count >= rl.limit {
			remaining := rl.window - now.Sub(v.timestamp)
			retryAfter := int((remaining + time.Second - 1) / time.Second)
			if retryAfter < 1 {
				retryAfter = 1
			}
			onLimit := rl.onLimit
			rl.mu.Unlock()
			// Standard Retry-After lets HTTP agents back off instead of
			// retrying immediately; keep retry_after in the JSON body for
			// existing API clients.
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "rate_limit_exceeded",
				"message":     "Too many requests, please try again later",
				"retry_after": retryAfter,
			})
			c.Abort()
			if onLimit != nil {
				onLimit()
			}
			return
		}

		v.count++
		rl.mu.Unlock()
		c.Next()
	}
}

// ErrorHandler middleware for unified error handling
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Check if there are any errors
		if len(c.Errors) > 0 {
			lastError := c.Errors.Last()

			// Log the real error server-side; never leak internal details to clients
			slog.Error("Handler error", "request_id", c.GetString("request_id"), "path", c.Request.URL.Path, "method", c.Request.Method, "error", lastError.Error())

			// Determine status code
			statusCode := http.StatusInternalServerError
			if c.Writer.Status() != http.StatusOK && c.Writer.Status() != http.StatusInternalServerError {
				statusCode = c.Writer.Status()
			}

			// Return JSON error response with generic message
			c.JSON(statusCode, gin.H{
				"success": false,
				"error":   "internal_error",
				"message": "An internal error occurred",
			})
		}
	}
}

// SecurityHeaders adds security headers to responses.
// NOTE: tlsEnabled is a startup snapshot — TLS listener binding itself
// requires a restart (server.tls is static), so there is nothing to sync.
func SecurityHeaders(tlsEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Cross-Origin-Opener-Policy", "same-origin")
		c.Header("Cross-Origin-Resource-Policy", "same-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; img-src 'self' data: blob:; font-src 'self' data: https://fonts.gstatic.com; connect-src 'self' ws: wss:; frame-ancestors 'none'")
		if tlsEnabled {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// RequestLogger logs all incoming requests with their completion status and
// the request ID generated by RequestID so individual requests can be traced
// across the codebase by grepping the request_id field.
// URL secrets (stage/phishing/oauth tokens in the path or query) are redacted
// so log aggregation never becomes a credential store.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("Request",
			"request_id", c.GetString("request_id"),
			"method", c.Request.Method,
			"path", redactRequestPath(c.Request.URL),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
		)
	}
}

// secretPathPrefixes are URL path prefixes whose remaining segments are
// secrets (or may embed them) and must never appear verbatim in logs.
var secretPathPrefixes = []string{
	"/stage/",
	"/phishing/l/",
	"/phishing/oauth/",
	"/payloads/",
}

// secretQueryKeys are query parameter names that carry one-time tokens or
// secrets and must be redacted in request logs.
var secretQueryKeys = map[string]bool{
	"token":    true,
	"secret":   true,
	"key":      true,
	"api_key":  true,
	"apikey":   true,
	"password": true,
	"sig":      true,
	"code":     true,
	"state":    true,
}

// redactRequestPath returns a log-safe rendering of u: secret path prefixes
// collapse to the prefix + "[redacted]", and secret query values become "*".
func redactRequestPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	path := u.Path
	for _, p := range secretPathPrefixes {
		if strings.HasPrefix(path, p) {
			path = p + "[redacted]"
			break
		}
	}
	if u.RawQuery == "" {
		return path
	}
	q := u.Query()
	redacted := false
	for k := range q {
		if secretQueryKeys[strings.ToLower(k)] {
			q.Set(k, "*")
			redacted = true
		}
	}
	if !redacted {
		return path + "?" + u.RawQuery
	}
	return path + "?" + q.Encode()
}

// NoCache adds cache-control headers to prevent caching
func NoCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Next()
	}
}

// RequestBodyLimit wraps the request body with http.MaxBytesReader to reject
// oversized payloads before they are buffered into memory. File upload routes
// (multipart) should skip this middleware since they have their own size checks.
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// CORS returns a middleware that sets CORS headers based on the canonical
// OriginAllowed policy shared with WebSocket upgrades.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	snapshot := append([]string(nil), allowedOrigins...)
	return CORSWithOrigins(func() []string { return snapshot })
}

// CORSWithOrigins reads the current allowlist for every request. The server
// uses this variant because server.allowed_origins is hot-reloadable; capturing
// the startup slice made WebSockets update immediately while fetch requests
// kept the old policy until restart.
func CORSWithOrigins(current func() []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Even a request without Origin needs this cache key: otherwise its
		// response may be reused for a later cross-origin browser request.
		c.Writer.Header().Add("Vary", "Origin")
		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			c.Next()
			return
		}

		var allowedOrigins []string
		if current != nil {
			allowedOrigins = current()
		}
		allowed := OriginAllowed(origin, c.Request.Host, allowedOrigins)
		if allowed {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			// X-CSRF-Token is required by CSRFProtect's double-submit check;
			// omitting it here forces cross-origin deployments to fail
			// preflight and tempts operators into Allow-Origin: *.
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-CSRF-Token, X-Request-ID")
			c.Header("Access-Control-Max-Age", "86400")
		}
		// No CORS headers at all for disallowed origins (previously the
		// Allow-Methods/Headers/Max-Age were emitted unconditionally).

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// OriginAllowed is the single browser-origin policy for CORS and WebSockets.
// A console served by this process is always allowed to call back to the same
// authority, including a public IP or hostname. With no explicit allowlist,
// loopback-to-loopback requests may also cross ports for local Vite development;
// a localhost page is not allowed to reach a public Host. Configured entries are
// hostnames (a full http(s) origin is accepted for backward compatibility).
func OriginAllowed(origin, requestHost string, allowedOrigins []string) bool {
	if origin == "" {
		return true
	}
	u, ok := parseBrowserOrigin(origin)
	if !ok {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(u.Host), strings.TrimSpace(requestHost)) {
		return true
	}

	requestHostname := authorityHostname(requestHost)
	if len(allowedOrigins) == 0 {
		return hostIsLoopback(u.Hostname()) && hostIsLoopback(requestHostname)
	}
	for _, configured := range allowedOrigins {
		configured = strings.TrimSpace(configured)
		if strings.Contains(configured, "://") {
			if allowed, ok := parseBrowserOrigin(configured); ok &&
				strings.EqualFold(u.Scheme, allowed.Scheme) && strings.EqualFold(u.Host, allowed.Host) {
				return true
			}
			continue
		}
		if hostname, ok := configuredOriginHostname(configured); ok && strings.EqualFold(u.Hostname(), hostname) {
			return true
		}
	}
	return false
}

func parseBrowserOrigin(origin string) (*url.URL, bool) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, false
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, false
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, false
		}
	}
	return u, true
}

func authorityHostname(authority string) string {
	u, err := url.Parse("//" + strings.TrimSpace(authority))
	if err != nil || u.User != nil || u.Path != "" {
		return ""
	}
	return u.Hostname()
}

func configuredOriginHostname(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	u, err := url.Parse("//" + value)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Port() != "" || strings.HasSuffix(u.Host, ":") ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return u.Hostname(), true
}

// hostIsLoopback reports whether a hostname is a loopback address: the three
// spellings plus any address in 127/8 or ::1.
func hostIsLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return addr.IsLoopback()
	}
	return false
}

type InFlightTracker struct {
	wg sync.WaitGroup
}

// NewInFlightTracker creates a tracker for in-flight HTTP requests.
func NewInFlightTracker() *InFlightTracker {
	return &InFlightTracker{}
}

// Middleware increments the counter on entry and decrements on exit.
func (t *InFlightTracker) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		t.wg.Add(1)
		defer t.wg.Done()
		c.Next()
	}
}

// Wait blocks until all in-flight requests have completed.
func (t *InFlightTracker) Wait() {
	t.wg.Wait()
}

// CacheControl returns a middleware that sets cache-control headers for
// cacheable GET responses. Only caches responses with 200 status and a
// non-empty body. The maxAge parameter controls how long the response
// can be cached (in seconds).
func CacheControl(maxAge int) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if c.Request.Method == "GET" && c.Writer.Status() == 200 && c.Writer.Size() > 0 {
			c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
		}
	}
}

// RequestID generates a unique request ID for each incoming request.
// If the client sends an X-Request-ID header, it is reused; otherwise a
// random 16-byte hex string is generated. The ID is set in the response
// header and stored in the Gin context under "request_id".
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err == nil {
				id = hex.EncodeToString(b)
			} else {
				id = "unknown"
			}
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}
