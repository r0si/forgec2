package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
)

// TestReloadKeepsEnvironmentOverrides pins the fix for a silent, wide-reaching bug:
// Load applied 21 FORGEC2_* variables and the hot-reload path did not, so the first
// config change after startup dropped them. The damage ranged from a reverted
// data_dir (the server starts writing somewhere else) to reverted crypto keys
// (existing credentials and backups stop decrypting) to a reverted
// plugins.require_signed (the trust policy silently weakens).
func TestReloadKeepsEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// The file carries values that CONFLICT with the environment: the env must win
	// on both the initial load and the reload.
	if err := os.WriteFile(path, []byte(`
server:
  data_dir: data
  port: 8000
database:
  path: data/db/forgec2.db
crypto:
  loot_key: ""
plugins:
  require_signed: false
`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FORGEC2_DATA_DIR", filepath.Join(dir, "persistent"))
	t.Setenv("FORGEC2_LOOT_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t.Setenv("FORGEC2_PLUGINS_REQUIRE_SIGNED", "true")

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Server.DataDir != filepath.Join(dir, "persistent") {
		t.Fatalf("initial load ignored FORGEC2_DATA_DIR: %q", loaded.Server.DataDir)
	}

	// Simulate the reloader: build a config from the same file bytes and apply the
	// shared helper, exactly as config_reloader.go now does.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := config.DefaultConfig()
	if err := reloaded.LoadFromData(data); err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	config.ApplyEnvOverrides(reloaded)

	if reloaded.Server.DataDir != loaded.Server.DataDir {
		t.Fatalf("reload changed data_dir: %q -> %q", loaded.Server.DataDir, reloaded.Server.DataDir)
	}
	if reloaded.Crypto.LootKey != loaded.Crypto.LootKey {
		t.Fatal("reload dropped the loot key supplied by the environment; stored credentials would stop decrypting")
	}
	if !reloaded.Plugins.RequireSigned {
		t.Fatal("reload dropped FORGEC2_PLUGINS_REQUIRE_SIGNED; the plugin trust policy silently weakened")
	}
	if reloaded.Database.Path != loaded.Database.Path {
		t.Fatalf("reload changed the database path: %q -> %q", loaded.Database.Path, reloaded.Database.Path)
	}
}

// TestReloadDoesNotMintNewSecrets is the other half: generating a secret on reload
// would invalidate sessions and rendered payloads every time the file was touched,
// which is why minting lives in Load only.
func TestReloadDoesNotMintNewSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  jwt_secret: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No FORGEC2_JWT_SECRET: Load mints one and writes it back.
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Server.JWTSecret == "" {
		t.Fatal("Load did not mint a JWT secret")
	}
	first := loaded.Server.JWTSecret

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := config.DefaultConfig()
	if err := reloaded.LoadFromData(data); err != nil {
		t.Fatal(err)
	}
	config.ApplyEnvOverrides(reloaded)
	if reloaded.Server.JWTSecret != first {
		t.Fatalf("the reload path minted a different JWT secret (%q -> %q)", first, reloaded.Server.JWTSecret)
	}
}
