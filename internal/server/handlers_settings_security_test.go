package server

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

func TestSettingsWebhooksRoundTrip(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	// Nothing stored yet: an empty list, not a null body.
	c, w := missingAPICtx(http.MethodGet, "/api/settings/webhooks", "", nil)
	s.handleGetSettingsWebhooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("empty get = %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Notifications []any `json:"notifications"`
	}
	unmarshalData(t, w.Body.Bytes(), &got)
	if len(got.Notifications) != 0 {
		t.Fatalf("empty notifications = %+v", got.Notifications)
	}

	// Save a target list and read it back.
	body := `{"notifications":[{"type":"slack","url":"https://hooks.203.0.113.9/x"},{"type":"discord","url":"https://discord.203.0.113.9/y"}]}`
	c, w = missingAPICtx(http.MethodPost, "/api/settings/webhooks", body, nil)
	s.handleSaveSettingsWebhooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	c, w = missingAPICtx(http.MethodGet, "/api/settings/webhooks", "", nil)
	s.handleGetSettingsWebhooks(c)
	unmarshalData(t, w.Body.Bytes(), &got)
	if len(got.Notifications) != 2 {
		t.Fatalf("round-trip = %+v", got.Notifications)
	}

	// Invalid body is a 400.
	c, w = missingAPICtx(http.MethodPost, "/api/settings/webhooks", `{`, nil)
	s.handleSaveSettingsWebhooks(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid body = %d", w.Code)
	}

	// A corrupt stored value must degrade to an empty list, not a 500.
	if err := s.db.Save(&db.ServerConfig{Key: "notification_targets", Value: "{not json"}).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodGet, "/api/settings/webhooks", "", nil)
	s.handleGetSettingsWebhooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("corrupt get = %d %s", w.Code, w.Body.String())
	}
	unmarshalData(t, w.Body.Bytes(), &got)
	if len(got.Notifications) != 0 {
		t.Fatalf("corrupt value returned %+v", got.Notifications)
	}
}

func TestTestSettingsWebhookBlocksSSRF(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	// Loopback/metadata/private targets must be refused by the URL validator
	// before any request is made, with a clear reason.
	for _, target := range []string{
		"http://127.0.0.1:9/hook",
		"http://10.0.0.1/hook",
		"http://169.254.169.254/latest/meta-data/",
		"ftp://203.0.113.9/hook",
	} {
		c, w := missingAPICtx(http.MethodPost, "/api/settings/webhooks/test",
			`{"type":"webhook","url":"`+target+`"}`, nil)
		s.handleTestSettingsWebhook(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "blocked") {
			t.Fatalf("target %q = %d %s", target, w.Code, w.Body.String())
		}
	}

	// A missing URL is a validation error, not a request.
	c, w := missingAPICtx(http.MethodPost, "/api/settings/webhooks/test", `{"type":"webhook"}`, nil)
	s.handleTestSettingsWebhook(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "URL is required") {
		t.Fatalf("missing url = %d %s", w.Code, w.Body.String())
	}

	// Invalid JSON body.
	c, w = missingAPICtx(http.MethodPost, "/api/settings/webhooks/test", `{`, nil)
	s.handleTestSettingsWebhook(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid body = %d", w.Code)
	}
}

func TestAuthConfiguredAndAuditPage(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	if s.authConfigured() {
		t.Fatal("authConfigured true without a password hash")
	}
	s.cfg.Auth.PasswordHash = "$2a$10$fakehash"
	if !s.authConfigured() {
		t.Fatal("authConfigured false with a password hash")
	}

	c, w := missingAPICtx(http.MethodGet, "/audit", "", nil)
	s.handleAuditLogPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("audit page = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "admin") {
		t.Fatalf("audit page did not list the active admin: %.120s", w.Body.String())
	}
}

func TestHandleDownloadConfigRedactsEverySecret(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	s.configPath = t.TempDir() + string(os.PathSeparator) + "config.yaml"

	// Every secret the endpoint is supposed to mask.
	secrets := map[string]string{
		"jwt":     "jwt-secret-value-0001",
		"beacon":  "beacon-key-value-0002",
		"ssh":     "ssh-password-0003",
		"auth":    "bcrypt-hash-0004",
		"ai":      "sk-ai-key-0005",
		"slackb":  "xoxb-slack-bot-0006",
		"slacka":  "xapp-slack-app-0007",
		"slacks":  "slack-signing-0008",
		"cryptok": "crypto-key-0009",
		"loot":    "loot-key-0010",
		"totp":    "totp-key-0011",
		"csrf":    "csrf-key-0012",
		"extc2":   "extc2-key-0013",
		"backup":  "backup-key-0014",
		"apitok":  "extc2-api-token-0015",
	}
	s.cfg.Server.JWTSecret = secrets["jwt"]
	s.cfg.Server.BeaconKey = secrets["beacon"]
	s.cfg.Server.SSHPassword = secrets["ssh"]
	s.cfg.Auth.PasswordHash = secrets["auth"]
	s.cfg.AI.APIKey = secrets["ai"]
	s.cfg.Integrations.Slack.BotToken = secrets["slackb"]
	s.cfg.Integrations.Slack.AppToken = secrets["slacka"]
	s.cfg.Integrations.Slack.SigningSecret = secrets["slacks"]
	s.cfg.Crypto.Key = secrets["cryptok"]
	s.cfg.Crypto.LootKey = secrets["loot"]
	s.cfg.Crypto.TotpKey = secrets["totp"]
	s.cfg.Crypto.CsrfKey = secrets["csrf"]
	s.cfg.Crypto.ExtC2Key = secrets["extc2"]
	s.cfg.Crypto.BackupKey = secrets["backup"]
	s.cfg.RateLimit.ExtC2.APIToken = secrets["apitok"]

	var file strings.Builder
	file.WriteString("server:\n")
	for _, name := range []string{"jwt", "beacon", "ssh", "auth", "ai", "slackb", "slacka", "slacks",
		"cryptok", "loot", "totp", "csrf", "extc2", "backup", "apitok"} {
		file.WriteString("  " + name + ": " + secrets[name] + "\n")
	}
	file.WriteString("  port: 8000\n")
	if err := os.WriteFile(s.configPath, []byte(file.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	c, w := missingAPICtx(http.MethodGet, "/api/settings/config/download", "", nil)
	s.handleDownloadConfig(c)
	if w.Code != http.StatusOK {
		t.Fatalf("download = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for name, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("secret %q leaked into the config download", name)
		}
	}
	if !strings.Contains(body, "port: 8000") {
		t.Fatal("non-secret config lines were dropped")
	}
	if !strings.Contains(body, "****") {
		t.Fatal("no redaction marker in the downloaded config")
	}

	// A non-admin caller is refused outright.
	c2, w2 := missingAPICtx(http.MethodGet, "/api/settings/config/download", "", nil)
	c2.Set("user_role", "user")
	s.handleDownloadConfig(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("non-admin download = %d", w2.Code)
	}

	// A missing config file is an error response, not a leaked stack.
	s.configPath = t.TempDir() + string(os.PathSeparator) + "absent.yaml"
	c3, w3 := missingAPICtx(http.MethodGet, "/api/settings/config/download", "", nil)
	s.handleDownloadConfig(c3)
	if w3.Code != http.StatusInternalServerError {
		t.Fatalf("missing config = %d", w3.Code)
	}
}

func TestHandleDBVacuumOnSQLite(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	s.db.Create(&db.AuditLog{User: "admin", Action: "login", Success: true})

	c, w := missingAPICtx(http.MethodPost, "/api/settings/maintenance/vacuum", "", nil)
	s.handleDBVacuum(c)
	if w.Code != http.StatusOK {
		t.Fatalf("vacuum = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Message string `json:"message"`
		Size    int64  `json:"size"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Size <= 0 {
		t.Fatalf("vacuum reported size %d", out.Size)
	}
	// The database still answers after a VACUUM.
	if _, err := s.db.DB(); err != nil {
		t.Fatalf("db unusable after vacuum: %v", err)
	}
}
