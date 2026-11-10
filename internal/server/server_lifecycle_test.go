package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
)

const testStorageKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// setServerTestKeys fills all REQUIRED independent storage keys so tests can
// build a config that passes startup validation / server construction.
func setServerTestKeys(cfg *config.Config) {
	cfg.Crypto.LootKey = testStorageKeyHex
	cfg.Crypto.ExtC2Key = testStorageKeyHex
	cfg.Crypto.BackupKey = testStorageKeyHex
	cfg.Crypto.TotpKey = testStorageKeyHex
	cfg.Crypto.CsrfKey = testStorageKeyHex
}

func TestNewInitializesServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "test-secret-for-new-test-32char-len!"
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)

	srv := New(cfg, db)
	if srv == nil {
		t.Fatal("New() returned nil server")
	}
	if srv.db == nil {
		t.Error("db is nil")
	}
	if srv.cfg == nil {
		t.Error("cfg is nil")
	}
	if srv.eventManager == nil {
		t.Error("eventManager is nil")
	}
	if srv.loginLockout == nil {
		t.Error("loginLockout is nil")
	}
}

func TestShutdownCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "test-secret-for-shutdown-test-32char!"
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)

	srv := New(cfg, db)
	srv.SetupRoutes()
	srv.SetStaticFS(nil)

	done := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown() did not complete within 10 seconds")
	}
}

func TestShutdownWithUninitializedInFlightTracker(t *testing.T) {
	// A server can reach shutdown with an HTTP server allocated but before
	// middleware initialization completes (for example during a failed reload).
	// Shutdown must remain safe and bounded in that partial state.
	srv := &Server{httpServer: &http.Server{}}
	done := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() did not complete with an uninitialized in-flight tracker")
	}
}

func TestHandleHealthCheckWithDBFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.SetupTestDB(t)
	s := &Server{db: db, startTime: time.Now()}

	t.Run("healthy DB returns 200", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/health", nil)
		s.handleHealthCheck(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("closed DB returns 503", func(t *testing.T) {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get sql.DB: %v", err)
		}
		sqlDB.Close()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/health", nil)
		s.handleHealthCheck(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("nil DB returns 503", func(t *testing.T) {
		nilDBServer := &Server{startTime: time.Now()}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/health", nil)
		nilDBServer.handleHealthCheck(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d; body=%s", w.Code, w.Body.String())
		}
	})
}

// Demo mode is a deployment flag, not a secret: the public /health answer tells
// the frontend to skip the /ws connect/reconnect loop that a read-only demo
// intentionally cannot satisfy.
func TestHandleHealthCheckReportsDemoMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.SetupTestDB(t)
	s := &Server{db: db, startTime: time.Now()}

	read := func(t *testing.T) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/health", nil)
		s.handleHealthCheck(c)
		if w.Code != http.StatusOK {
			t.Fatalf("health = %d; body=%s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("health JSON: %v", err)
		}
		return body
	}

	t.Run("enabled", func(t *testing.T) {
		t.Setenv("FORGEC2_DEMO_MODE", "true")
		if got := read(t)["demo_mode"]; got != true {
			t.Fatalf("demo_mode = %v, want true", got)
		}
	})
	t.Run("one also enables", func(t *testing.T) {
		t.Setenv("FORGEC2_DEMO_MODE", "1")
		if got := read(t)["demo_mode"]; got != true {
			t.Fatalf("demo_mode = %v, want true", got)
		}
	})
	t.Run("disabled by default", func(t *testing.T) {
		t.Setenv("FORGEC2_DEMO_MODE", "0")
		if got := read(t)["demo_mode"]; got != false {
			t.Fatalf("demo_mode = %v, want false", got)
		}
	})
}

func TestHandleReadyCheckWithDBFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.SetupTestDB(t)
	s := &Server{db: db, startTime: time.Now()}

	t.Run("healthy DB returns 200", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/ready", nil)
		s.handleReadyCheck(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("closed DB returns 503", func(t *testing.T) {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get sql.DB: %v", err)
		}
		sqlDB.Close()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/ready", nil)
		s.handleReadyCheck(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("nil DB returns 503", func(t *testing.T) {
		nilDBServer := &Server{startTime: time.Now()}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/ready", nil)
		nilDBServer.handleReadyCheck(c)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d; body=%s", w.Code, w.Body.String())
		}
	})
}
