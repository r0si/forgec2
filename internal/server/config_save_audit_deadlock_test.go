package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestConfigSaveWithAuditDoesNotDeadlock pins the hazard that cost a 900-second
// suite timeout: validateAndSaveConfigLocked audits while the caller holds
// configMu for WRITE, so anything the audit path takes (it briefly took
// configMu.RLock for the anchor file) deadlocks an RWMutex that is not reentrant.
//
// The deadline is the point of the test: a reintroduction must fail loudly in five
// seconds instead of hanging the whole suite.
func TestConfigSaveWithAuditDoesNotDeadlock(t *testing.T) {
	s := mustTenantServer(t)
	// Use a config that Validate() accepts; the helper's own cfg is a test fixture.
	cfg := config.DefaultConfig()
	setServerTestKeys(cfg)
	s.configMu.Lock()
	s.cfg = cfg
	s.configMu.Unlock()
	s.configPath = filepath.Join(t.TempDir(), "config.yaml")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/settings/save", nil)
	c.Set("user", "admin1")
	c.Set("user_role", "admin")

	type result struct {
		ok   bool
		code int
	}
	done := make(chan result, 1)
	go func() {
		s.configMu.Lock()
		ok := s.validateAndSaveConfigLocked(c, "deadlock_probe")
		s.configMu.Unlock()
		done <- result{ok: ok, code: w.Code}
	}()

	select {
	case got := <-done:
		if !got.ok {
			t.Fatalf("the save was rejected (status %d)", got.code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("saving the config while holding the write lock deadlocked: the audit path must not take configMu")
	}

	// The save is audited, so an operator's intent is traceable.
	var count int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "deadlock_probe").Count(&count)
	if count == 0 {
		t.Fatal("the config save was not audited")
	}
}
