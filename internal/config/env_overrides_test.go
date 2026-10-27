package config

import "testing"

// TestApplyEnvOverridesCoversEveryVariable pins the supported surface one variable at
// a time. It exists because this function is now shared by the loader and the
// hot-reload path: a variable added to one and not the other is exactly the bug that
// made a reload drop data_dir, the crypto keys and the plugin trust policy.
func TestApplyEnvOverridesCoversEveryVariable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORGEC2_DATA_DIR", dir)
	t.Setenv("FORGEC2_JWT_SECRET", "jwt-from-env")
	t.Setenv("FORGEC2_LOOT_KEY", "loot-from-env")
	t.Setenv("FORGEC2_EXTC2_KEY", "extc2-from-env")
	t.Setenv("FORGEC2_BACKUP_KEY", "backup-from-env")
	t.Setenv("FORGEC2_TOTP_KEY", "totp-from-env")
	t.Setenv("FORGEC2_CSRF_KEY", "csrf-from-env")
	t.Setenv("FORGEC2_AI_API_KEY", "ai-from-env")
	t.Setenv("FORGEC2_PLUGIN_TRUSTED_KEYS", " aa , bb ,, ")
	t.Setenv("FORGEC2_PLUGINS_REQUIRE_SIGNED", "true")
	t.Setenv("FORGEC2_PLUGINS_MAX_CONCURRENT", "7")
	t.Setenv("FORGEC2_PLUGINS_MAX_CPU_SECONDS", "45")
	t.Setenv("FORGEC2_PLUGINS_MAX_WRITE_MB", "512")
	t.Setenv("FORGEC2_BACKUP_RETAIN", "9")
	t.Setenv("FORGEC2_BACKUP_SCHEDULE", "0 3 * * *")
	t.Setenv("FORGEC2_DB_PATH", "changed.db")
	t.Setenv("FORGEC2_DB_DRIVER", "postgres")
	t.Setenv("FORGEC2_DB_DSN", "postgres://x")
	t.Setenv("FORGEC2_HOST", "127.0.0.2")
	t.Setenv("FORGEC2_PORT", "9443")
	t.Setenv("FORGEC2_SLACK_BOT_TOKEN", "xoxb-from-env")

	cfg := DefaultConfig()
	ApplyEnvOverrides(cfg)

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"data_dir", cfg.Server.DataDir, dir},
		{"jwt_secret", cfg.Server.JWTSecret, "jwt-from-env"},
		{"loot_key", cfg.Crypto.LootKey, "loot-from-env"},
		{"extc2_key", cfg.Crypto.ExtC2Key, "extc2-from-env"},
		{"backup_key", cfg.Crypto.BackupKey, "backup-from-env"},
		{"totp_key", cfg.Crypto.TotpKey, "totp-from-env"},
		{"csrf_key", cfg.Crypto.CsrfKey, "csrf-from-env"},
		{"ai_api_key", cfg.AI.APIKey, "ai-from-env"},
		{"plugin_keys", len(cfg.Plugins.TrustedKeys), 2},
		{"plugin_key_0", cfg.Plugins.TrustedKeys[0], "aa"},
		{"require_signed", cfg.Plugins.RequireSigned, true},
		{"max_concurrent", cfg.Plugins.MaxConcurrent, 7},
		{"max_cpu_seconds", cfg.Plugins.MaxCPUSeconds, 45},
		{"max_write_mb", cfg.Plugins.MaxWriteMB, 512},
		{"backup_retain", cfg.Backup.Retain, 9},
		{"backup_schedule", cfg.Backup.Schedule, "0 3 * * *"},
		{"db_path", cfg.Database.Path, "changed.db"},
		{"db_driver", cfg.Database.Driver, "postgres"},
		{"db_dsn", cfg.Database.DSN, "postgres://x"},
		{"host", cfg.Server.Host, "127.0.0.2"},
		{"port", cfg.Server.Port, 9443},
		{"slack_token", cfg.Integrations.Slack.BotToken, "xoxb-from-env"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestApplyEnvOverridesRejectsInvalidValues keeps a typo in the environment from
// poisoning the config: invalid values leave the default in place.
func TestApplyEnvOverridesRejectsInvalidValues(t *testing.T) {
	t.Setenv("FORGEC2_PLUGINS_REQUIRE_SIGNED", "maybe")
	t.Setenv("FORGEC2_PLUGINS_MAX_CONCURRENT", "-3")
	t.Setenv("FORGEC2_BACKUP_RETAIN", "-1")
	t.Setenv("FORGEC2_PORT", "0")

	cfg := DefaultConfig()
	before := cfg.Server.Port
	ApplyEnvOverrides(cfg)

	if cfg.Plugins.RequireSigned {
		t.Error("an unparsable boolean was applied")
	}
	if cfg.Plugins.MaxConcurrent != 0 {
		t.Errorf("a negative concurrency was applied: %d", cfg.Plugins.MaxConcurrent)
	}
	if cfg.Backup.Retain != 0 {
		t.Errorf("a negative retain was applied: %d", cfg.Backup.Retain)
	}
	if cfg.Server.Port != before {
		t.Errorf("port 0 was applied: %d", cfg.Server.Port)
	}
}
