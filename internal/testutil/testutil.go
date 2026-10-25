package testutil

import (
	"encoding/json"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func SetupTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// SQLite ":memory:" is per-connection; a single open connection keeps the
	// schema and rows shared across all queries in the test (and avoids the
	// classic "no such table" when the pool opens a second in-memory DB).
	if sqlDB, err := database.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	err = database.AutoMigrate(
		&db.Implant{}, &db.Task{}, &db.AuditLog{}, &db.Listener{},
		&db.TokenEntry{}, &db.SocksSession{}, &db.User{},
		&db.CredentialEntry{}, &db.CredentialUsage{}, &db.CloudCred{},
		&db.BuildLog{}, &db.ScanResult{}, &db.NetworkHost{}, &db.CommandTemplate{},
		&db.BOFFile{}, &db.ServerConfig{}, &db.WebhookConfig{},
		&db.Plugin{}, &db.PluginReview{}, &db.PluginDependency{}, &db.PluginUpdateStatus{},
		&db.AutomationRule{}, &db.AlertRule{}, &db.Alert{}, &db.SystemMetric{},
		&db.GeneratedReport{}, &db.MeshPeer{},
		&db.BloodHoundResult{}, &db.Campaign{},
		&db.OpsecHistory{}, &db.CircuitBreakerConfig{},
		&db.CircuitBreakerEvent{}, &db.CustomRole{},
		&db.PhishingTemplate{}, &db.PhishingCampaign{}, &db.PhishingEvent{},
		&db.AgentTag{}, &db.AgentTagAssignment{}, &db.AutoTagRule{}, &db.Notification{},
		&db.AgentGroup{}, &db.AgentGroupAssignment{}, &db.Workflow{}, &db.WorkflowStep{},
		&db.ExecutionLog{}, &db.ChatMessage{},
		&db.StagerToken{}, &db.Redirector{}, &db.AgentLock{},
		&db.AIChatSession{}, &db.AIChatMessage{}, &db.ExtC2Channel{},
		&db.UserSession{}, &db.OpsecRule{},
		&db.PasswordHistory{}, &db.APIKey{}, &db.Script{},
		&db.RegSecret{}, &db.KillSwitch{}, &db.Tenant{}, &db.AgentStatusEvent{},
		&db.SIEMRule{}, &db.CommandMacro{}, &db.MacroRun{},
	)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Bootstrap the "default" tenant like production (ensureDefaultTenant at
	// server boot) so defaultTenantID() resolves for tenant-scoping assertions.
	var def db.Tenant
	if err := database.Where("name = ?", db.DefaultTenantName).FirstOrCreate(&def, db.Tenant{Name: db.DefaultTenantName}).Error; err != nil {
		t.Fatalf("bootstrap default tenant: %v", err)
	}
	return database
}

type TestServer struct {
	DB *gorm.DB
	G  *gin.Engine
}

func NewGinTestServer(t *testing.T, db *gorm.DB) *TestServer {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return &TestServer{DB: db}
}

func AssertValidJSON(t *testing.T, body []byte, label string) {
	t.Helper()
	if len(body) == 0 {
		t.Fatalf("%s: empty body", label)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("%s: invalid json: %v; body=%s", label, err, string(body))
	}
}

// AssertKeyExists fails unless the response body carries key. Success bodies are
// wrapped as {"success": true, "data": <payload>}, so the payload is checked
// first and a non-envelope body is checked as-is.
func AssertKeyExists(t *testing.T, body []byte, label, key string) {
	t.Helper()
	var env struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("%s: invalid json: %v; body=%s", label, err, string(body))
	}
	obj := env.Data
	if !env.Success || obj == nil {
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("%s: invalid json: %v; body=%s", label, err, string(body))
		}
		obj = raw
	}
	if _, ok := obj[key]; !ok {
		t.Fatalf("%s: missing key %q; body=%s", label, key, string(body))
	}
}
