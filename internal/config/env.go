package config

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strconv"
)

// ApplyEnvOverrides applies every FORGEC2_* environment override to cfg.
//
// It exists because the hot-reload path used to rebuild the config from the file
// alone: Load applied these variables, the reloader did not, so the first config
// change after startup silently dropped them. The damage ranged from a reverted
// data_dir (the server starts writing somewhere else) to reverted crypto keys
// (existing credentials and backups stop decrypting) to a reverted
// plugins.require_signed (the trust policy silently weakens).
//
// Every caller that builds a Config from file bytes must call this before
// Validate, so "the file changed" never means "the environment stopped applying".
func ApplyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	// Container deployments keep the persistent volume at /data while the default
	// data_dir is relative to the working directory.
	applyDataDirEnv(cfg)

	// Env override for JWT secret (takes precedence over config file).
	if envSecret := os.Getenv("FORGEC2_JWT_SECRET"); envSecret != "" {
		cfg.Server.JWTSecret = envSecret
	}

	// Env override for loot key (takes precedence over config file)
	if envLootKey := os.Getenv("FORGEC2_LOOT_KEY"); envLootKey != "" {
		cfg.Crypto.LootKey = envLootKey
	}

	// Env override for ExtC2 key (takes precedence over config file)
	if envExtC2Key := os.Getenv("FORGEC2_EXTC2_KEY"); envExtC2Key != "" {
		cfg.Crypto.ExtC2Key = envExtC2Key
	}

	// Env override for backup key (takes precedence over config file)
	if envBackupKey := os.Getenv("FORGEC2_BACKUP_KEY"); envBackupKey != "" {
		cfg.Crypto.BackupKey = envBackupKey
	}

	// Env override for the credential key (takes precedence over config file)
	if envTotpKey := os.Getenv("FORGEC2_TOTP_KEY"); envTotpKey != "" {
		cfg.Crypto.TotpKey = envTotpKey
	}

	// Env override for CSRF key (takes precedence over config file)
	if envCsrfKey := os.Getenv("FORGEC2_CSRF_KEY"); envCsrfKey != "" {
		cfg.Crypto.CsrfKey = envCsrfKey
	}

	// Env override for AI API key (takes precedence over config file)
	if envAIKey := os.Getenv("FORGEC2_AI_API_KEY"); envAIKey != "" {
		cfg.AI.APIKey = envAIKey
	}

	// Plugin trust via env: comma-separated hex Ed25519 public keys, a boolean
	// strictness switch, the concurrency ceiling, and the CPU-time budget.
	if envKeys := os.Getenv("FORGEC2_PLUGIN_TRUSTED_KEYS"); envKeys != "" {
		cfg.Plugins.TrustedKeys = splitAndTrim(envKeys)
	}
	if envSigned := os.Getenv("FORGEC2_PLUGINS_REQUIRE_SIGNED"); envSigned != "" {
		if v, err := strconv.ParseBool(envSigned); err == nil {
			cfg.Plugins.RequireSigned = v
		} else {
			slog.Warn("Ignoring FORGEC2_PLUGINS_REQUIRE_SIGNED: not a boolean", "value", envSigned)
		}
	}
	if envConc := os.Getenv("FORGEC2_PLUGINS_MAX_CONCURRENT"); envConc != "" {
		if v, err := strconv.Atoi(envConc); err == nil && v >= 0 {
			cfg.Plugins.MaxConcurrent = v
		} else {
			slog.Warn("Ignoring FORGEC2_PLUGINS_MAX_CONCURRENT: not a non-negative integer", "value", envConc)
		}
	}
	if envCPU := os.Getenv("FORGEC2_PLUGINS_MAX_CPU_SECONDS"); envCPU != "" {
		if v, err := strconv.Atoi(envCPU); err == nil && v >= 0 {
			cfg.Plugins.MaxCPUSeconds = v
		} else {
			slog.Warn("Ignoring FORGEC2_PLUGINS_MAX_CPU_SECONDS: not a non-negative integer", "value", envCPU)
		}
	}
	if envWrite := os.Getenv("FORGEC2_PLUGINS_MAX_WRITE_MB"); envWrite != "" {
		if v, err := strconv.Atoi(envWrite); err == nil && v >= 0 {
			cfg.Plugins.MaxWriteMB = v
		} else {
			slog.Warn("Ignoring FORGEC2_PLUGINS_MAX_WRITE_MB: not a non-negative integer", "value", envWrite)
		}
	}

	// Backup retention / schedule via env.
	if envRetain := os.Getenv("FORGEC2_BACKUP_RETAIN"); envRetain != "" {
		if v, err := strconv.Atoi(envRetain); err == nil && v >= 0 {
			cfg.Backup.Retain = v
		} else {
			slog.Warn("Ignoring FORGEC2_BACKUP_RETAIN: not a non-negative integer", "value", envRetain)
		}
	}
	if envSchedule := os.Getenv("FORGEC2_BACKUP_SCHEDULE"); envSchedule != "" {
		cfg.Backup.Schedule = envSchedule
	}

	// Env overrides for critical settings
	if envDBPath := os.Getenv("FORGEC2_DB_PATH"); envDBPath != "" {
		cfg.Database.Path = envDBPath
	}
	if envDBDriver := os.Getenv("FORGEC2_DB_DRIVER"); envDBDriver != "" {
		cfg.Database.Driver = envDBDriver
	}
	if envDBDSN := os.Getenv("FORGEC2_DB_DSN"); envDBDSN != "" {
		cfg.Database.DSN = envDBDSN
	}
	if envHost := os.Getenv("FORGEC2_HOST"); envHost != "" {
		cfg.Server.Host = envHost
	}
	if envPort := os.Getenv("FORGEC2_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 && p <= 65535 {
			cfg.Server.Port = p
		}
	}
	if envSlackToken := os.Getenv("FORGEC2_SLACK_BOT_TOKEN"); envSlackToken != "" {
		cfg.Integrations.Slack.BotToken = envSlackToken
	}
}

// applySecretEnvForMinting applies just the JWT secret override, so the minting step
// can see whether the environment already supplies one. Applying the FULL override
// set before minting would persist environment values into the file when a generated
// secret triggers a save, which is how a config file quietly acquires secrets that
// were meant to stay in the environment.
func applySecretEnvForMinting(cfg *Config) {
	if envSecret := os.Getenv("FORGEC2_JWT_SECRET"); envSecret != "" {
		cfg.Server.JWTSecret = envSecret
	}
}

// generateMissingSecrets mints a random value for any secret still weak or empty
// and writes the file back, so a fresh deployment never runs on a guessable value.
//
// Only Load calls this: a reload must never mint a new secret, or every config touch
// would invalidate sessions and rendered payloads.
func generateMissingSecrets(cfg *Config, path string) error {
	type secret struct {
		field  *string
		label  string
		advice string
	}
	for _, s := range []secret{
		{&cfg.Server.JWTSecret, "JWT secret", "server.jwt_secret"},
		{&cfg.Server.BeaconKey, "Beacon key", "server.beacon_key"},
		{&cfg.RateLimit.ExtC2.APIToken, "ExtC2 API token", "rate_limit.extc2.api_token"},
	} {
		if *s.field != "" && !isWeakSecret(*s.field) {
			continue
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		*s.field = hex.EncodeToString(key)
		if err := cfg.Save(path); err != nil {
			return err
		}
		slog.Warn(s.label + " auto-generated. To use a custom value, set " + s.advice + " or the matching FORGEC2_* env var")
	}
	return nil
}
