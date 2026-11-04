package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newContractDB migrates the full schema into an in-memory sqlite so
// contract tests can invoke real handlers (their DB queries succeed against
// empty tables). AutoMigrate is idempotent.
func newContractDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// SQLite ":memory:" is per-connection; a single open connection keeps the
	// schema and rows shared across all queries in the test (and avoids the
	// classic "no such table" when the pool opens a second in-memory DB —
	// e.g. the build-queue tests race the worker's log INSERT against the
	// test's SELECT, which is exactly how this flaked on CI).
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("get underlying db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
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
		&db.UserSession{}, &db.AgentStatusEvent{},
		&db.OpsecRule{}, &db.PasswordHistory{}, &db.APIKey{}, &db.Script{},
	)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

// assertValidJSON fails the test unless the body is valid non-empty JSON.
func assertValidJSON(t *testing.T, body []byte, label string) {
	t.Helper()
	if len(body) == 0 {
		t.Fatalf("%s: empty body", label)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("%s: invalid json: %v; body=%s", label, err, string(body))
	}
}

// TestContract_Envelope_Dashboard asserts the dashboard chart handlers
// return valid JSON with the expected data keys.
func TestContract_Envelope_Dashboard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{db: newContractDB(t)}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/activity-heatmap?range=24h", nil)
	s.handleDashboardActivityHeatmap(c)
	assertValidJSON(t, w.Body.Bytes(), "activity-heatmap")

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/os-distribution", nil)
	s.handleDashboardOSDistribution(c2)
	assertValidJSON(t, w2.Body.Bytes(), "os-distribution")

	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/task-status", nil)
	s.handleDashboardTaskStatus(c3)
	assertValidJSON(t, w3.Body.Bytes(), "task-status")

	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	c4.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/credential-types", nil)
	s.handleDashboardCredentialTypes(c4)
	assertValidJSON(t, w4.Body.Bytes(), "credential-types")

	w5 := httptest.NewRecorder()
	c5, _ := gin.CreateTestContext(w5)
	c5.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/agent-geo", nil)
	s.handleDashboardAgentGeo(c5)
	assertValidJSON(t, w5.Body.Bytes(), "agent-geo")

	w6 := httptest.NewRecorder()
	c6, _ := gin.CreateTestContext(w6)
	c6.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/task-gantt?range=24h", nil)
	s.handleDashboardTaskGantt(c6)
	assertValidJSON(t, w6.Body.Bytes(), "task-gantt")

	w7 := httptest.NewRecorder()
	c7, _ := gin.CreateTestContext(w7)
	c7.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/attack-path", nil)
	s.handleDashboardAttackPath(c7)
	assertValidJSON(t, w7.Body.Bytes(), "attack-path")

	w8 := httptest.NewRecorder()
	c8, _ := gin.CreateTestContext(w8)
	c8.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/listener-traffic?range=24h", nil)
	s.handleDashboardListenerTraffic(c8)
	assertValidJSON(t, w8.Body.Bytes(), "listener-traffic")
}

// TestContract_Envelope_ConvertedRoutes asserts the handlers migrated to
// registerBoth (opsec / timeline / automation / webhooks / autotag) return
// valid JSON. timeline-export is a CSV download and is exempt.
func TestContract_Envelope_ConvertedRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{db: newContractDB(t)}

	type endpoint struct {
		name    string
		handler func(*gin.Context)
		path    string
		isCSV   bool
	}
	cases := []endpoint{
		{"timeline-data", s.handleTimelineData, "/api/timeline/data", false},
		{"timeline-export", s.handleTimelineExport, "/api/timeline/export", true},
		{"automation-rules", s.handleListAutomationRules, "/api/automation/rules", false},
		{"webhooks", s.handleListWebhooks, "/api/webhooks", false},
		{"autotag-rules", s.handleAutoTagRules, "/api/autotag/rules", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, tc.path, nil)
			tc.handler(c)

			if tc.isCSV {
				if w.Code != http.StatusOK {
					t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
				}
				if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
					t.Fatalf("expected text/csv Content-Type, got %q", ct)
				}
				return
			}

			assertValidJSON(t, w.Body.Bytes(), tc.name)
		})
	}
}

// TestContract_Envelope_More extends the envelope guardrail to additional
// resource handlers. Every endpoint added here is LOCKED: a future shape
// regression fails CI. Add new envelope endpoints to this list as they are
// migrated.
func TestContract_Envelope_More(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{db: newContractDB(t)}

	cases := []struct {
		name    string
		handler func(*gin.Context)
	}{
		{"campaigns", s.handleCampaignsList},
		{"bloodhound-list", s.handleBloodHoundList},
		{"bloodhound-status", s.handleBloodHoundStatus},
		{"collab-agents", s.handleCollabAgents},
		{"cloud-results", s.handleCloudResults},
		{"bof-list", s.handleBOFList},
		{"alert-rules", s.handleGetAlertRules},
		{"autotag-apply", s.handleAutoTagApply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, "/api/"+tc.name, nil)
			tc.handler(c)

			assertValidJSON(t, w.Body.Bytes(), tc.name)
		})
	}
}
