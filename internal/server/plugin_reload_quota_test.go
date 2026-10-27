package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/plugin"
)

// TestReloadAppliesPluginQuotas pins the half of the plugin policy that a reload used
// to drop: the hook only called SetTrust, so editing plugins.max_concurrent (or the
// CPU/write budgets) reported "plugins applied" while the manager kept the ceiling it
// started with.
func TestReloadAppliesPluginQuotas(t *testing.T) {
	s := mustTenantServer(t)
	s.pluginManager = plugin.NewManager(s.db)

	// Start from a small ceiling, then reload with a different one.
	s.cfg.Plugins.MaxConcurrent = 1
	s.cfg.Plugins.MaxCPUSeconds = 1
	s.cfg.Plugins.MaxWriteMB = 1
	if err := s.applyPluginTrust(); err != nil {
		t.Fatalf("startup apply: %v", err)
	}

	s.cfg.Plugins.MaxConcurrent = 4
	s.cfg.Plugins.MaxCPUSeconds = 30
	s.cfg.Plugins.MaxWriteMB = 64
	if err := s.applyPluginTrust(); err != nil {
		t.Fatalf("reload apply: %v", err)
	}

	if got := s.pluginManager.MaxConcurrent(); got != 4 {
		t.Fatalf("max_concurrent after reload = %d, want 4", got)
	}
	if got := s.pluginManager.MaxCPUSeconds(); got != 30 {
		t.Fatalf("max_cpu_seconds after reload = %d, want 30", got)
	}
	if got := s.pluginManager.MaxWriteMB(); got != 64 {
		t.Fatalf("max_write_mb after reload = %d, want 64", got)
	}
}

// TestReloadAppliesPluginTrustPolicy is the other half: require_signed flipped at
// runtime must actually change what the manager enforces.
func TestReloadAppliesPluginTrustPolicy(t *testing.T) {
	s := mustTenantServer(t)
	s.pluginManager = plugin.NewManager(s.db)

	s.cfg.Plugins.RequireSigned = false
	s.cfg.Plugins.TrustedKeys = nil
	if err := s.applyPluginTrust(); err != nil {
		t.Fatal(err)
	}
	if s.pluginManager.RequireSigned() {
		t.Fatal("require_signed was on before it was set")
	}

	s.cfg.Plugins.RequireSigned = true
	if err := s.applyPluginTrust(); err != nil {
		t.Fatal(err)
	}
	if !s.pluginManager.RequireSigned() {
		t.Fatal("a reload that turned require_signed on did not take effect")
	}

	// An invalid key list must be reported, not silently ignored: the operator needs
	// to know the policy did not apply.
	s.cfg.Plugins.TrustedKeys = []string{"not-hex"}
	if err := s.applyPluginTrust(); err == nil {
		t.Fatal("an invalid trusted_keys list was accepted")
	}
}

// TestApplyPluginTrustIsIdempotent keeps the startup path safe to call twice (it is
// the only place that installs the policy now).
func TestApplyPluginTrustIsIdempotent(t *testing.T) {
	s := mustTenantServer(t)
	s.pluginManager = plugin.NewManager(s.db)
	s.cfg.Plugins = config.DefaultConfig().Plugins

	// A config that leaves a quota at 0 keeps the manager build-time default, so the
	// assertion is "repeating the call changes nothing", not "it equals the config".
	if err := s.applyPluginTrust(); err != nil {
		t.Fatal(err)
	}
	first := s.pluginManager.MaxConcurrent()
	for i := 0; i < 3; i++ {
		if err := s.applyPluginTrust(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := s.pluginManager.MaxConcurrent(); got != first {
		t.Fatalf("concurrency drifted on repeated application: %d -> %d", first, got)
	}
}
