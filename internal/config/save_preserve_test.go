package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSavePreservesUnknownKeys pins the destructive case: a console save used to
// rewrite the file from the in-memory struct, so an option added by a NEWER build
// was deleted the first time an operator changed the port - and after the upgrade it
// was silently back at its default.
func TestSavePreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := `
server:
  port: 8000
crypto:
  require_release_signature: false
future_section:
  enabled: true
  nested:
    depth: 3
crypto_future_option: "keep-me"
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// An operator changes a KNOWN value through the console.
	cfg.Server.Port = 9123
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(saved, &doc); err != nil {
		t.Fatalf("saved config is not valid YAML: %v\n%s", err, saved)
	}

	// The known change landed.
	server, _ := doc["server"].(map[string]interface{})
	if got, _ := server["port"].(int); got != 9123 {
		t.Fatalf("known value not saved: port = %v", server["port"])
	}

	// The unknown keys survived, including the nested ones.
	future, ok := doc["future_section"].(map[string]interface{})
	if !ok {
		t.Fatalf("future_section was dropped on save:\n%s", saved)
	}
	if enabled, _ := future["enabled"].(bool); !enabled {
		t.Fatalf("future_section.enabled changed: %v", future["enabled"])
	}
	nested, ok := future["nested"].(map[string]interface{})
	if !ok || nested["depth"] != 3 {
		t.Fatalf("future_section.nested was dropped or mangled: %v", future["nested"])
	}
	if got := doc["crypto_future_option"]; got != "keep-me" {
		t.Fatalf("top-level unknown key was dropped: %v", got)
	}

	// Nothing the loader would now complain about.
	if unknown := UnknownConfigKeys(saved); len(unknown) != 2 {
		t.Fatalf("expected exactly the two unknown keys, got %v", unknown)
	}
}

// TestSaveDoesNotResurrectStaleKnownValues pins the other direction of the merge:
// only UNKNOWN keys are carried over, so a value the operator cleared in the console
// cannot come back from the old file.
func TestSaveDoesNotResurrectStaleKnownValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8000\n  host: old-host\nunknown_keep: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// The operator clears a known value.
	cfg.Server.Host = ""
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "old-host") {
		t.Fatalf("a stale known value came back from the old file:\n%s", saved)
	}
	if !strings.Contains(string(saved), "unknown_keep") {
		t.Fatalf("the unknown key was dropped:\n%s", saved)
	}
}

// TestSaveWithoutExistingFile covers the first write (and an unreadable file): the
// merge step must not turn a normal save into an error.
func TestSaveWithoutExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.yaml")
	cfg := DefaultConfig()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no file written: %v", err)
	}
}
