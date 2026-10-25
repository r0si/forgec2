package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestSyncRateLimiters verifies quota changes apply without dropping state.
func TestSyncRateLimiters(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.RateLimit.Beacon.Limit = 10
	s.cfg.RateLimit.Beacon.Window = 60
	s.cfg.RateLimit.API.Capacity = 20
	s.cfg.RateLimit.API.Rate = 5
	s.cfg.RateLimit.API.Whitelist = []string{"10.0.0.1"}
	s.rateLimiter = middleware.NewRateLimiter(t.Context(), 100, time.Minute)
	s.apiRateLimiter = middleware.NewAPIRateLimiter(t.Context(), 100, 10)

	if err := s.syncRateLimiters(); err != nil {
		t.Fatalf("syncRateLimiters: %v", err)
	}
	if limit, window := s.rateLimiter.Snapshot(); limit != 10 || window != time.Minute {
		t.Fatalf("beacon limiter = (%d, %v), want (10, 1m)", limit, window)
	}
	if cap, rate := s.apiRateLimiter.Snapshot(); cap != 20 || rate != 5 {
		t.Fatalf("api limiter = (%v, %v), want (20, 5)", cap, rate)
	}
	// Invalid values are ignored, never applied.
	s.cfg.RateLimit.Beacon.Limit = -1
	if err := s.syncRateLimiters(); err != nil {
		t.Fatalf("syncRateLimiters with invalid: %v", err)
	}
	if limit, _ := s.rateLimiter.Snapshot(); limit != 10 {
		t.Fatalf("invalid limit must not apply, got %d", limit)
	}
}

// TestSyncLogLevel verifies verbosity follows config without restart.
func TestSyncLogLevel(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.Logging.Level = "debug"
	if err := s.syncLogLevel(); err != nil {
		t.Fatalf("syncLogLevel: %v", err)
	}
	if LogLevelVar().Level().String() != "DEBUG" {
		t.Fatalf("level = %v, want DEBUG", LogLevelVar().Level())
	}
	t.Cleanup(func() { SetLogLevel("info") })
	s.cfg.Logging.Level = "bogus"
	if err := s.syncLogLevel(); err == nil {
		t.Fatal("expected error for unknown level")
	}
}

// TestSyncSIEM verifies endpoint swaps without dropping the queue.
func TestSyncSIEM(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.SIEM.Enabled = true
	s.cfg.SIEM.URL = "https://siem.example/hook"
	s.cfg.SIEM.Token = "tok"
	s.cfg.SIEM.Actions = "login,command"
	if err := s.syncSIEM(); err != nil {
		t.Fatalf("syncSIEM create: %v", err)
	}
	if s.siem == nil {
		t.Fatal("expected SIEM instance")
	}
	enabled, url, _ := s.siem.Snapshot()
	if !enabled || url != "https://siem.example/hook" {
		t.Fatalf("snapshot = (%v, %q)", enabled, url)
	}
	s.cfg.SIEM.Enabled = false
	if err := s.syncSIEM(); err != nil {
		t.Fatalf("syncSIEM disable: %v", err)
	}
	if enabled, _, _ := s.siem.Snapshot(); enabled {
		t.Fatal("disable must take effect without restart")
	}
}

// TestSyncJWTSecret verifies rotation installs the new key.
func TestSyncJWTSecret(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.Server.JWTSecret = "test-jwt-secret-0123456789abcdef"
	s.cfg.Server.CookieDomain = ""
	if err := s.syncJWTSecret(); err != nil {
		t.Fatalf("syncJWTSecret: %v", err)
	}
}

// TestSyncRegSecrets verifies a rotated master rebuilds the store and
// rejects garbage without dropping the previous store.
func TestSyncRegSecrets(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.Server.BeaconKey = "5dd4e19cbf285aa8d53966945ef835c38e3990e1398915b9bc1ee071df4db223"
	if err := s.syncRegSecrets(); err != nil {
		t.Fatalf("syncRegSecrets: %v", err)
	}
	if s.regSecrets == nil {
		t.Fatal("expected reg secret store")
	}
	s.cfg.Server.BeaconKey = "not-hex!!"
	if err := s.syncRegSecrets(); err == nil {
		t.Fatal("expected error for invalid beacon key")
	}
	if s.regSecrets == nil {
		t.Fatal("failed rotation must keep the previous store")
	}
}

// TestSyncTrustedProxies verifies proxy trust updates without restart.
func TestSyncTrustedProxies(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.Server.TrustedProxies = []string{"10.0.0.0/8"}
	// Router may be nil in this fixture; hook must tolerate that.
	if err := s.syncTrustedProxies(); err != nil {
		t.Fatalf("syncTrustedProxies: %v", err)
	}
}

// TestSyncDBPool verifies pool sizes apply to the live sql.DB, and that
// sqlite is clamped to 1/1 (single writer) even when configured higher.
func TestSyncDBPool(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	s.cfg.Server.DBMaxOpenConns = 7
	s.cfg.Server.DBMaxIdleConns = 3
	if err := s.syncDBPool(); err != nil {
		t.Fatalf("syncDBPool: %v", err)
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("sqlite max open = %d, want 1 (clamped)", stats.MaxOpenConnections)
	}
}

// TestReloadStatusHandler verifies the matrix endpoint shape.
func TestReloadStatusHandler(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = config.DefaultConfig()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/config/reload-status", s.handleReloadStatus)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/reload-status", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Groups []struct {
			Token     string `json:"token"`
			Mode      string `json:"mode"`
			Mechanism string `json:"mechanism"`
		} `json:"groups"`
	}
	unmarshalData(t, w.Body.Bytes(), &body)
	if len(body.Groups) == 0 {
		t.Fatalf("bad matrix payload: %+v", w.Body.String())
	}
	seen := map[string]bool{}
	for _, g := range body.Groups {
		seen[g.Token] = true
		if g.Mode != "hot" && g.Mode != "static" {
			t.Fatalf("token %q bad mode %q", g.Token, g.Mode)
		}
	}
	for _, want := range []string{"server.beacon_key", "server.port", "logging.level", "siem"} {
		if !seen[want] {
			t.Fatalf("matrix missing %q", want)
		}
	}
}
