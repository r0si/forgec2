package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnknownConfigKeysFindsTypos pins the reason this exists: a misspelled control
// name used to be dropped in silence, so the operator believed the control was on.
func TestUnknownConfigKeysFindsTypos(t *testing.T) {
	doc := []byte(`
server:
  port: 8000
  prot: 8443
crypto:
  require_release_signatur: true
totally_unknown_section:
  a: 1
`)
	keys := UnknownConfigKeys(doc)
	want := []string{"crypto.require_release_signatur", "server.prot", "totally_unknown_section"}
	if len(keys) != len(want) {
		t.Fatalf("unknown keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("unknown keys = %v, want %v", keys, want)
		}
	}
}

// TestUnknownConfigKeysAcceptsRealConfig keeps the warning meaningful: a correct
// document must produce nothing, or the message becomes noise everyone ignores.
func TestUnknownConfigKeysAcceptsRealConfig(t *testing.T) {
	doc := []byte(`
server:
  port: 8000
  host: 127.0.0.1
  tls_enabled: false
crypto:
  require_release_signature: false
`)
	if keys := UnknownConfigKeys(doc); len(keys) != 0 {
		t.Fatalf("a valid config reported unknown keys: %v", keys)
	}
}

// TestShippedExampleConfigHasNoUnknownKeys is the user-facing half: an operator who
// copies config.example.yaml must not be told they wrote something wrong.
func TestShippedExampleConfigHasNoUnknownKeys(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if keys := UnknownConfigKeys(data); len(keys) != 0 {
		t.Fatalf("config.example.yaml carries keys this build does not know: %v", keys)
	}
}

// TestLoadWarnsWithoutFailing keeps the compatibility contract: a document with an
// unknown key still loads (a newer build's config must not brick a rollback), and
// the known keys in the same file are applied.
func TestLoadWarnsWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := "server:\n  port: 8123\n  prot: 8443\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load refused a config with an unknown key: %v", err)
	}
	if cfg.Server.Port != 8123 {
		t.Fatalf("known key not applied: port = %d", cfg.Server.Port)
	}
}

// TestUnknownConfigKeysHandlesDeepNesting bounds the recursion: a pathological
// document must not make the loader walk forever.
func TestUnknownConfigKeysHandlesDeepNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString("a:\n")
	indent := "  "
	for i := 0; i < 40; i++ {
		b.WriteString(strings.Repeat(indent, i+1))
		b.WriteString("x:\n")
	}
	keys := UnknownConfigKeys([]byte(b.String()))
	if len(keys) == 0 {
		t.Fatal("a deeply nested unknown key was not reported")
	}
}
