package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// spaFS wraps http.FileSystem to serve index.html for SPA client-side routes.
type spaFS struct {
	inner http.FileSystem
}

func (s *spaFS) Open(name string) (http.File, error) {
	f, err := s.inner.Open(name)
	if err != nil {
		return s.inner.Open("index.html")
	}
	// Next.js static export emits both a `<route>/` directory (RSC chunks) and a
	// sibling `<route>.html` page. embed.FS.Open("<route>") resolves to the
	// directory, so serve the sibling .html page, not the root index.html —
	// otherwise /login and /dashboard boot the root Home page, which redirects to
	// /dashboard -> 401 -> /login, an infinite redirect loop.
	if fi, err := f.Stat(); err == nil && fi.IsDir() && name != "" && name != "/" {
		f.Close()
		if hf, herr := s.inner.Open(name + ".html"); herr == nil {
			return hf, nil
		}
		return s.inner.Open("index.html")
	}
	return f, nil
}

// SetStaticFS configures the server to serve embedded frontend static files.
// Must be called BEFORE SetupRoutes() so the middleware runs before route handlers.
func (s *Server) SetStaticFS(staticFS fs.FS) {
	s.staticFS = staticFS
	s.staticMeta = buildStaticAssetMeta(staticFS)

	// http.FileServer with SPA fallback
	fsrv := http.FileServer(&spaFS{http.FS(staticFS)})

	s.router.Use(func(c *gin.Context) {
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			c.Next()
			return
		}
		path := c.Request.URL.Path

		// Never intercept API, WS, beacon, health, extc2, admin, or JSON (XHR) requests.
		// NOTE /generate_204 and /th are beacon endpoints: the SPA middleware
		// previously swallowed every unmatched GET (malleable profile URIs
		// included), feeding implants HTML instead of task replies and killing
		// all GET-based C2 channels (P1).
		if strings.HasPrefix(path, "/api") ||
			strings.HasPrefix(path, "/ws") ||
			strings.HasPrefix(path, "/extc2") ||
			strings.HasPrefix(path, "/admin/") ||
			strings.HasPrefix(path, "/debug/") ||
			strings.HasPrefix(path, "/rd/") ||
			strings.Contains(c.GetHeader("Accept"), "application/json") ||
			c.GetHeader("X-Requested-With") == "XMLHttpRequest" ||
			c.GetHeader("X-CSRF-Token") != "" ||
			path == "/th" || path == "/generate_204" || path == "/collect" || path == "/health" ||
			path == "/ready" || path == "/metrics" ||
			strings.HasPrefix(path, "/payloads/") ||
			strings.HasPrefix(path, "/stage/") ||
			strings.HasPrefix(path, "/screenshots/") {
			c.Next()
			return
		}

		// Malleable-profile safety net (P1): with a malleable profile active,
		// any GET that is NOT a real SPA asset and NOT browser navigation
		// (Accept: text/html) can only be a profile-defined beacon URI — it
		// must fall through to the router so the NoRoute beacon handler sees
		// it. Previously every unmatched GET was fed index.html, silently
		// killing ALL GET-based C2 channels while malleable mode was on.
		if s.malleableEnabled() && !spaAssetExists(staticFS, path) &&
			!strings.Contains(c.GetHeader("Accept"), "text/html") {
			c.Next()
			return
		}

		// Unauthenticated root/dashboard access: redirect to /login so the
		// user sees the login form immediately instead of a blank SPA shell.
		if path == "/" || path == "/dashboard" {
			if tokenStr, err := c.Cookie("forgec2_session"); err != nil || tokenStr == "" {
				c.Redirect(http.StatusFound, "/login")
				c.Abort()
				return
			}
		}

		// Normalize trailing slash for SPA client routes so http.FileServer
		// never emits a 301. Browsers cache 301s aggressively, and the
		// /login <-> /login/ pair becomes an infinite redirect loop client-side
		// that never reaches the server. Done before the cache/ETag lookup so
		// both see the same canonical path.
		if len(path) > 1 && strings.HasSuffix(path, "/") {
			path = path[:len(path)-1]
			c.Request.URL.Path = path
		}

		// Cache policy for the embedded files. Only Vite's content-addressed
		// /assets/ files may be immutable. Everything else (index.html, the
		// per-route HTML shells, the vendored /js and /css files) keeps its
		// name across upgrades, so it is served no-cache with a content-hash
		// ETag: revalidation is a cheap 304, and embed.FS has no mtime to
		// offer as a Last-Modified fallback.
		//
		// middleware.NoCache() ran before this middleware and stamped the API
		// policy (Pragma/Expires) onto every response; drop those so nothing
		// contradicts the per-file policy below.
		name, meta, resolved := s.resolveStaticAsset(path)
		c.Writer.Header().Del("Pragma")
		c.Writer.Header().Del("Expires")
		if resolved && strings.HasPrefix(name, "assets/") && name == strings.TrimPrefix(path, "/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Header("Cache-Control", "no-cache")
		}
		if resolved {
			c.Header("ETag", meta.etag)
			if inm := c.GetHeader("If-None-Match"); inm != "" && etagMatches(inm, meta.etag) {
				c.Status(http.StatusNotModified)
				c.Abort()
				return
			}
		}

		// gzip only for a plain GET of a text body big enough to pay for the
		// framing and accepted by the client (a Range request needs identity
		// bytes for Content-Range; a HEAD has no body). Beacon/malleable and
		// API paths never reach this middleware, so no C2 channel is touched.
		compressible := resolved && meta.compressible && meta.size >= minCompressBytes
		if compressible {
			// Announce negotiability even on the identity variant so shared
			// caches never hand a gzipped body to a client that refused it.
			c.Header("Vary", "Accept-Encoding")
		}
		var gw *gzipResponseWriter
		if compressible && c.Request.Method == http.MethodGet && c.GetHeader("Range") == "" &&
			acceptsGzip(c.GetHeader("Accept-Encoding")) {
			gw = newGzipResponseWriter(c.Writer)
		}
		body := http.ResponseWriter(c.Writer)
		if gw != nil {
			body = gw
		}

		fsrv.ServeHTTP(body, c.Request)
		if gw != nil {
			gw.Close()
		}
		c.Abort()
	})

	slog.Info("Embedded frontend enabled")
}

// malleableEnabled reports whether a malleable C2 profile is active (its GET
// URIs must never be shadowed by the SPA fallback). Nil-safe for tests.
// Runs on every static-asset request, so it takes the same read lock the
// malleable save uses — an unlocked read here raced the settings write.
func (s *Server) malleableEnabled() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg.Malleable.Enabled
}

// spaAssetExists reports whether the raw static FS holds a real file at path
// (directories and the index.html fallback of spaFS do not count).
func spaAssetExists(staticFS fs.FS, path string) bool {
	if path == "" || path == "/" {
		return false
	}
	f, err := staticFS.Open(strings.TrimPrefix(path, "/"))
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	return err == nil && !fi.IsDir()
}

// newHTTPServer creates an http.Server with standard timeout configuration.
func (s *Server) newHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadTimeout:       HTTPReadTimeout,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      HTTPWriteTimeout,
		IdleTimeout:       HTTPIdleTimeout,
	}
}

// configureTLS sets up the TLS configuration on the given http.Server,
// including JARM/JA3 fingerprint randomization and optional mTLS.
// The server certificate is served through the hot-reloadable loader:
// regenerate/upload take effect for new handshakes without a restart.
//
// This is the server-wide posture used by the main listener; extra listeners
// go through configureTLSWith with their own per-listener overrides.
func (s *Server) configureTLS(srv *http.Server) error {
	return s.configureTLSWith(srv, listenerTLSOverrides{})
}

// configureTLSWith is configureTLS plus optional per-listener hardening. The
// overrides can only strengthen the server-wide posture: they may add a
// client-certificate requirement, supply the client CA, or raise the TLS floor
// to 1.3 — never drop the server-wide requirement or leave the listener
// without the requested check. A configuration that cannot be honoured returns
// an error so the caller can fail the listener start instead of serving a
// weaker posture.
func (s *Server) configureTLSWith(srv *http.Server, o listenerTLSOverrides) error { // config-lock: boot, TLS is configured before serving
	floor, ok := minTLSVersionFloor(o.MinTLSVersion)
	if !ok {
		return fmt.Errorf("invalid min_tls_version %q", o.MinTLSVersion)
	}
	tlsConfig := &tls.Config{MinVersion: floor}

	if s.tlsCerts == nil {
		s.tlsCerts = newTLSCertLoader(s.cfg.Server.CertFile, s.cfg.Server.KeyFile)
	}
	if err := s.tlsCerts.loadNow(); err != nil {
		return fmt.Errorf("loading TLS server certificate: %w", err)
	}
	tlsConfig.GetCertificate = s.tlsCerts.GetCertificate

	if s.tlsFingerprint != nil {
		tlsConfig = s.tlsFingerprint.Live(tlsConfig)
	}

	requireClientCert, caPool, err := s.effectiveClientAuth(o)
	if err != nil {
		return err
	}
	if requireClientCert {
		tlsConfig.ClientCAs = caPool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		caUsed := strings.TrimSpace(o.ClientCAFile)
		if caUsed == "" {
			caUsed = strings.TrimSpace(s.cfg.Server.ClientCAFile)
		}
		slog.Info("mTLS enabled for beacon connections", "ca", caUsed)
	} else if s.cfg.Server.OperatorMTLS {
		// Operator mTLS only REQUESTS a client certificate at the TLS layer
		// so cert-less beacons keep working on the shared listener; the
		// operatorPlaneGuard middleware enforces verification on operator
		// routes. NOTE: the CertificateRequest changes the server-side
		// (JARM) fingerprint flight — expected when this is enabled.
		tlsConfig.ClientAuth = tls.RequestClientCert
		slog.Info("Operator mTLS requested (enforced on operator routes)")
	}

	srv.TLSConfig = tlsConfig
	return nil
}

// ActivityMiddleware updates user's LastActivity timestamp on each request (throttled to 60s)
func (s *Server) ActivityMiddleware() gin.HandlerFunc {
	var mu sync.Mutex
	lastUpdated := make(map[uint]time.Time)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(ActivityCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-ActivityCleanupInterval)
				mu.Lock()
				for uid, t := range lastUpdated {
					if t.Before(cutoff) {
						delete(lastUpdated, uid)
					}
				}
				mu.Unlock()
			}
		}
	}()

	return func(c *gin.Context) {
		userID, exists := c.Get("user_id")
		if !exists {
			c.Next()
			return
		}
		uid, ok := userID.(uint)
		if !ok {
			c.Next()
			return
		}
		now := time.Now()
		mu.Lock()
		last := lastUpdated[uid]
		if now.Sub(last) < ActivityUpdateThrottle {
			mu.Unlock()
			c.Next()
			return
		}
		lastUpdated[uid] = now
		mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			defer cancel()
			s.db.WithContext(ctx).Model(&db.User{}).Where("id = ?", uid).Update("last_activity", now)
		}()
		c.Next()
	}
}

// handleHealthCheck provides health/ready endpoints for monitoring
func (s *Server) handleHealthCheck(c *gin.Context) {
	if s == nil || s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_unavailable"})
		return
	}
	sqlDB, err := s.db.DB()
	if err != nil || sqlDB.Ping() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_unavailable"})
		return
	}
	uptime := time.Since(s.startTime)
	// FORGEC2_DEMO_MODE marks a deployment where authenticated operator actions
	// are not available (the public read-only demo). The frontend reads it from
	// this public endpoint and skips the /ws connect/reconnect loop, which would
	// otherwise show a permanent "reconnecting" warning to every visitor.
	demoMode := strings.EqualFold(os.Getenv("FORGEC2_DEMO_MODE"), "true") ||
		os.Getenv("FORGEC2_DEMO_MODE") == "1"
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": ServerVersion,
		// uptime stays a Go duration string for existing consumers;
		// uptime_seconds is the machine-readable twin for alerting.
		"uptime":         uptime.String(),
		"uptime_seconds": uptime.Seconds(),
		"demo_mode":      demoMode,
	})
}
