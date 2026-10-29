package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestDeployGuideDocumentsReadyWarnings keeps docs/DEPLOY.md in step with what /ready actually reports.
//
// The monitoring section exists to tell an operator what to alert on, and it names the check keys and the two clearing
// endpoints. Renaming a check, adding a third, or moving a reset endpoint would leave an operator alerting on a key that
// no longer exists - a silent miss, because a missing warning looks exactly like a healthy system.
func TestDeployGuideDocumentsReadyWarnings(t *testing.T) {
	raw, err := os.ReadFile("../../docs/DEPLOY.md")
	if err != nil {
		t.Fatalf("the deploy guide is missing: %v", err)
	}
	doc := string(raw)

	section := doc[strings.Index(doc, "## Monitoring what the server says about itself"):]
	if !strings.HasPrefix(section, "## Monitoring") {
		t.Fatal("docs/DEPLOY.md has no monitoring section")
	}

	// 1. Every check the probe writes is named there.
	// Both halves of the contract: the checks that only warn, and the two that answer not-ready. A guide that documents one
	// half teaches a monitor to restart a working server, or to ignore a broken one.
	for _, check := range []string{"notification_delivery", "automation_actions", "auto_retries", "plugins", "audit_chain",
		"`database`", "`listeners`", "`bundle_signing`"} {
		if !strings.Contains(section, check) {
			t.Fatalf("the monitoring section does not name the %s check", check)
		}
	}

	// 2. The clearing endpoints are named exactly as the router registers them.
	for _, path := range []string{
		"/api/notification-routes/stats/reset",
		"/api/automation/action-stats/reset",
	} {
		if !strings.Contains(section, path) {
			t.Fatalf("the monitoring section does not mention %s", path)
		}
	}

	// 3. The two properties that make the warnings usable: they do not change readiness, and the threshold is "never",
	// not "anything".
	if !strings.Contains(section, "unready") {
		t.Fatal("the guide does not say that these warnings leave the server ready")
	}
	if !strings.Contains(section, "never delivered") {
		t.Fatal("the guide does not state the threshold that keeps a blip from raising a warning")
	}

	// 3b. The audit behaviour is documented too: an operator looking for "when did this start" needs to know it is there.
	for _, needle := range []string{"observability_warning", "observability_recovered"} {
		if !strings.Contains(section, needle) {
			t.Fatalf("the monitoring section does not mention %s", needle)
		}
	}

	// 4. And the guidance an operator needs: alert on warnings, not only on the status code.
	if !strings.Contains(section, "warnings") {
		t.Fatal("the guide does not tell an operator to alert on warnings")
	}
}

// TestEveryReadyCheckIsDocumented closes the set: whatever /\u200bready writes into `checks`, the guide names it.
//
// The test above pins the keys it knows about, which is a list somebody maintains; this one reads the probe's own answer, so a
// check added without a line in the guide fails here rather than living in the dark. A check that exists and is undocumented is
// worse than a missing one: the monitor sees a key it cannot interpret and either ignores all of them or alerts on all of them.
func TestEveryReadyCheckIsDocumented(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetRuleActionStats()
	resetAutoRetryCounter()
	t.Cleanup(func() {
		resetNotificationStats()
		resetRuleActionStats()
		resetAutoRetryCounter()
	})

	raw, err := os.ReadFile("../../docs/DEPLOY.md")
	if err != nil {
		t.Fatalf("the deploy guide is missing: %v", err)
	}
	section := string(raw)[strings.Index(string(raw), "## Monitoring what the server says about itself"):]

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleReadyCheck(c)
	// The values are not all strings: `listeners` is a count, which is the shape a monitor wants to compare against a
	// threshold. Decoding into interface{} keeps this guard about the KEY set rather than the value types.
	var payload struct {
		Checks map[string]interface{} `json:"checks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ready: %v", err)
	}
	if len(payload.Checks) < 5 {
		t.Fatalf("the probe reported only %d checks; this guard is not reading them", len(payload.Checks))
	}
	for name := range payload.Checks {
		if !strings.Contains(section, name) {
			t.Fatalf("the monitoring section does not document the %q check", name)
		}
	}
}

// TestReadySnapshotCanBeDownloaded covers ?download=1.
//
// A probe result is read once and pasted into a ticket, and "what did it say at 02:14" is not answerable from a browser tab that
// has since re-polled. The file names its instant, both in the filename and in the scope header.
func TestReadySnapshotCanBeDownloaded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetRuleActionStats()
	resetAutoRetryCounter()
	t.Cleanup(func() {
		resetNotificationStats()
		resetRuleActionStats()
		resetAutoRetryCounter()
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready?download=1", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleReadyCheck(c)

	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="ready_`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if scope := w.Header().Get(exportScopeHeader); !strings.HasPrefix(scope, "probe at ") {
		t.Fatalf("scope header = %q, want the instant of the probe", scope)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if probed, _ := payload["probed_at"].(string); probed == "" {
		t.Fatal("the snapshot does not record when it was taken")
	}
	// Without the parameter the response is the probe a monitor polls: no attachment headers.
	plain := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(plain)
	c2.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handleReadyCheck(c2)
	if cd := plain.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("the poll variant answers with attachment headers: %q", cd)
	}
}

// TestClearingCountersClearsTheReadyWarning closes the loop between the reset endpoints and the probe: the whole point of
// clearing a counter is that a fixed route stops looking broken, and the probe must agree.
func TestClearingCountersClearsTheReadyWarning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetRuleActionStats()
	t.Cleanup(func() {
		resetNotificationStats()
		resetRuleActionStats()
	})

	recordNotificationDelivery(11, errors.New("post: 500"))
	recordRuleActionOutcome("rule-x", false, errors.New("run_command: no target agent"))

	// 1. The probe is warning.
	if warnings := readyWarnings(t, s); len(warnings) != 2 {
		t.Fatalf("warnings = %v, want two", warnings)
	}

	// 2. Clearing both counters clears both warnings.
	for _, target := range []struct {
		path  string
		route string
	}{
		{"/api/notification-routes/stats/reset", "notification_stats_reset"},
		{"/api/automation/action-stats/reset", "automation_action_stats_reset"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, target.path, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		if target.route == "notification_stats_reset" {
			s.handleResetNotificationRouteStats(c)
		} else {
			// The rule exists so the endpoint's existence check passes.
			if err := s.db.Create(&db.AutomationRule{
				ID: "rule-x", Name: "rule-x", Enabled: true, EventType: string(EventImplantCheckin), TenantID: 1,
			}).Error; err != nil {
				t.Fatalf("seed rule: %v", err)
			}
			c.Request = httptest.NewRequest(http.MethodPost, target.path+"?id=rule-x", nil)
			s.handleResetAutomationActionStats(c)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d", target.path, w.Code)
		}
	}

	if warnings := readyWarnings(t, s); len(warnings) != 0 {
		t.Fatalf("clearing the counters left warnings behind: %v", warnings)
	}
}

// readyWarnings returns the warnings array from a readiness probe run.
func readyWarnings(t *testing.T, s *Server) []interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleReadyCheck(c)
	var payload map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ready: %v", err)
	}
	warnings, _ := payload["warnings"].([]interface{})
	return warnings
}
