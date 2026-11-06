package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/gin-gonic/gin"
)

// The fleet-default beacon timing randomization keys (implant.beacon_randomization
// and implant.max_random_interval_ms) round-trip through the agent-config save
// endpoint. These tests pin the save semantics, especially that an omitted value
// never clobbers a setting an operator chose via config.yaml (the backwards-safety
// contract of the additive handler change), and that a garbage mode cannot write
// a value Validate() would reject.

func newSaveAgentServer(t *testing.T) *Server {
	t.Helper()
	s := mustTenantServer(t)
	// mustTenantServer carries a ZERO config whose crypto keys are unset, so
	// Validate() (run inside the save) rejects it -- the save would return 400
	// before ever exercising the beacon fields. Swap in a fully valid config:
	// DefaultConfig + EnsureStorageKeys (no ConfigPath -> in-memory only), then
	// seed the two fields to known starting values so assertions are stable.
	s.configPath = filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig()
	if err := cfg.EnsureStorageKeys(); err != nil {
		t.Fatalf("ensure storage keys: %v", err)
	}
	cfg.Implant.BeaconRandomization = "linear"
	cfg.Implant.MaxRandomIntervalMs = 0
	s.configMu.Lock()
	s.cfg = cfg
	s.configMu.Unlock()
	return s
}

func postAgentConfig(t *testing.T, s *Server, form url.Values) {
	t.Helper()
	c, w := settingsFormCtx(s, t, "admin", "admin", form, nil)
	s.handleSaveAgentConfig(c)
	if w.Code != http.StatusOK {
		t.Fatalf("save agent config = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestSaveAgentConfigAppliesBeaconRandomization(t *testing.T) {
	s := newSaveAgentServer(t)
	postAgentConfig(t, s, url.Values{
		"beacon_randomization":   {"gaussian"},
		"max_random_interval_ms": {"2500"},
	})
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg.Implant.BeaconRandomization != "gaussian" {
		t.Errorf("beacon_randomization = %q, want gaussian", s.cfg.Implant.BeaconRandomization)
	}
	if s.cfg.Implant.MaxRandomIntervalMs != 2500 {
		t.Errorf("max_random_interval_ms = %d, want 2500", s.cfg.Implant.MaxRandomIntervalMs)
	}
}

// Omitting the fields must NOT reset them: an operator who set gaussian/2500 in
// config.yaml and then saves the agent form (which does not carry these keys)
// keeps the fleet default. This is the additive-handler safety contract.
func TestSaveAgentConfigOmitBeaconRandomizationKeepsCurrent(t *testing.T) {
	s := newSaveAgentServer(t)
	s.configMu.Lock()
	s.cfg.Implant.BeaconRandomization = "gaussian"
	s.cfg.Implant.MaxRandomIntervalMs = 4000
	s.configMu.Unlock()

	postAgentConfig(t, s, url.Values{})

	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg.Implant.BeaconRandomization != "gaussian" {
		t.Errorf("omitted beacon_randomization clobbered the fleet default to %q, want gaussian untouched", s.cfg.Implant.BeaconRandomization)
	}
	if s.cfg.Implant.MaxRandomIntervalMs != 4000 {
		t.Errorf("omitted cap clobbered to %d, want 4000 untouched", s.cfg.Implant.MaxRandomIntervalMs)
	}
}

// An unrecognized mode must be ignored rather than written (garbage would make
// Validate() reject the whole save and lock the operator out of the form).
func TestSaveAgentConfigInvalidBeaconModeIsIgnored(t *testing.T) {
	s := newSaveAgentServer(t)
	postAgentConfig(t, s, url.Values{"beacon_randomization": {"nonsense"}})
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg.Implant.BeaconRandomization != "linear" {
		t.Errorf("garbage mode wrote %q, want prior 'linear' preserved", s.cfg.Implant.BeaconRandomization)
	}
}

// An out-of-range cap is clamped, not stored verbatim.
func TestSaveAgentConfigClampsMaxRandomInterval(t *testing.T) {
	s := newSaveAgentServer(t)
	postAgentConfig(t, s, url.Values{"max_random_interval_ms": {"999999999"}})
	s.configMu.RLock()
	gotHigh := s.cfg.Implant.MaxRandomIntervalMs
	s.configMu.RUnlock()
	if gotHigh != 600000 {
		t.Errorf("cap = %d, want clamped 600000", gotHigh)
	}
	// A negative save clamps to 0 (never a negative delay).
	postAgentConfig(t, s, url.Values{"max_random_interval_ms": {"-50"}})
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg.Implant.MaxRandomIntervalMs != 0 {
		t.Errorf("cap after -50 = %d, want 0", s.cfg.Implant.MaxRandomIntervalMs)
	}
}

// TestSettingsPageExposesBeaconRandomization closes the round-trip: the JSON
// the Settings SPA reads must carry the two fleet-default keys (via the
// PascalCase->snake_case aliasing in renderPageOrJSON), or the UI could show
// and never persist them.
func TestSettingsPageExposesBeaconRandomization(t *testing.T) {
	ginSetTestMode(t)
	s := newSaveAgentServer(t)
	s.configMu.Lock()
	s.cfg.Implant.BeaconRandomization = "adaptive"
	s.cfg.Implant.MaxRandomIntervalMs = 3210
	s.configMu.Unlock()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/settings", nil)
	c.Request.Header.Set("Accept", "application/json")
	c.Set("user", "admin")
	c.Set("user_role", "admin")
	s.handleSettingsPage(c)

	if w.Code != http.StatusOK {
		t.Fatalf("settings page = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var out struct {
		BeaconRandomization string `json:"beacon_randomization"`
		MaxRandomIntervalMs int    `json:"max_random_interval_ms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode settings json: %v", err)
	}
	if out.BeaconRandomization != "adaptive" {
		t.Errorf("GET beacon_randomization = %q, want adaptive", out.BeaconRandomization)
	}
	if out.MaxRandomIntervalMs != 3210 {
		t.Errorf("GET max_random_interval_ms = %d, want 3210", out.MaxRandomIntervalMs)
	}
}
