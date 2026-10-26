package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
)

// TestRunServesThenShutsDown boots the real server on a free loopback port,
// proves /health answers, and shuts it down: Run must return instead of
// leaving the process parked.
func TestRunServesThenShutsDown(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real listener")
	}
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = freeTCPPort(t)
	cfg.Server.TLSEnabled = false
	cfg.Server.JWTSecret = "test-secret-for-run-boot-32chars!"
	cfg.Server.UpdateCheckEnabled = false
	// Keep the boot path from generating/persisting an ExtC2 token.
	cfg.RateLimit.ExtC2.APIToken = "run-test-token"
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)

	s := New(cfg, database)
	s.SetupRoutes()
	s.configPath = filepath.Join(t.TempDir(), "config.yaml")

	runErr := make(chan error, 1)
	go func() { runErr <- s.Run() }()

	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Server.Port)
	var healthy bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, healthURL, nil)
		if err != nil {
			t.Fatalf("build health request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		select {
		case err := <-runErr:
			t.Fatalf("Run returned before serving: %v", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("server never became healthy on the configured port")
	}

	// A second Run-equivalent bind on the same port must be refused before
	// touching the listener (the guard returns a clear "already in use" error).
	cfg2 := config.DefaultConfig()
	cfg2.Server.Host = "127.0.0.1"
	cfg2.Server.Port = cfg.Server.Port
	cfg2.Server.JWTSecret = "test-secret-for-run-boot-32chars!"
	cfg2.RateLimit.ExtC2.APIToken = "run-test-token"
	setServerTestKeys(cfg2)
	s2 := New(cfg2, database)
	s2.SetupRoutes()
	s2.configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := s2.Run(); err == nil {
		t.Fatal("a second server on the same port was allowed to boot")
	}
	s2.Shutdown()

	s.Shutdown()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Run returned %v after a graceful shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}

	// The port must be free again, so a restart can bind it immediately.
	if !waitPortFree(t, cfg.Server.Host, cfg.Server.Port, 10*time.Second) {
		t.Fatalf("port %d still held after shutdown", cfg.Server.Port)
	}
}

func waitPortFree(t *testing.T, host string, port int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if isPortAvailable(host, port) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
