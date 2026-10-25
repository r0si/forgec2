package server

import (
	"encoding/json"
	"fmt"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// ── Helpers ─────────────────────────────────────────────────────────────────

// siemCtx builds a tenant-scoped context for the SIEM rule API. Rules are global
// detection policy, so the tenant identity is still set (the correlator has no
// tenant notion) but the assertions below never expect tenant isolation.
func siemCtx(s *Server, t *testing.T, username, role string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, username, 1)
	c.Set("user_role", role)
	return c, w
}

func siemJSONCtx(s *Server, t *testing.T, username, role string, method, body, id string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := siemCtx(s, t, username, role)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(method, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, w
}

func seedSIEMRule(t *testing.T, s *Server, name, action string, window, threshold int, enabled bool) db.SIEMRule {
	t.Helper()
	rule := db.SIEMRule{
		Name: name, Enabled: enabled, Action: action,
		WindowSec: window, Threshold: threshold,
		AlertAction: "siem_alert", AlertDetails: "seeded",
	}
	if err := s.db.Create(&rule).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	// SIEMRule.Enabled carries `gorm:"default:true"`, so a false value is
	// substituted by the column default on insert. Write it back explicitly.
	if !enabled {
		if err := s.db.Model(&db.SIEMRule{}).Where("id = ?", rule.ID).Update("enabled", false).Error; err != nil {
			t.Fatalf("pause seeded rule: %v", err)
		}
		rule.Enabled = false
	}
	return rule
}

// ── Validation ──────────────────────────────────────────────────────────────

// TestValidateSIEMRule pins the rule contract the handlers all funnel through.
func TestValidateSIEMRule(t *testing.T) {
	ptr := func(b bool) *bool { return &b }

	valid := []siemRuleInput{
		{Name: "n", Action: "login_failed", WindowSec: 60, Threshold: 3, AlertAction: "siem_alert"},
		{Name: "n", Action: "implant_checkin", WindowSec: 3600, Threshold: 1, AlertAction: "siem_alert"},
		{Name: "n", Action: "a", WindowSec: 1, Threshold: 1, AlertAction: "a"},
		{Name: "n", Action: "a", WindowSec: 300, Threshold: 5, Enabled: ptr(false), AlertAction: "a"},
	}
	for i, req := range valid {
		if msg := validateSIEMRule(req); msg != "" {
			t.Fatalf("valid rule %d rejected: %s", i, msg)
		}
	}

	invalid := []struct {
		name string
		req  siemRuleInput
		want string
	}{
		{"missing name", siemRuleInput{Action: "a", WindowSec: 60, Threshold: 1}, "name"},
		{"missing action", siemRuleInput{Name: "n", WindowSec: 60, Threshold: 1}, "action"},
		{"zero window", siemRuleInput{Name: "n", Action: "a", WindowSec: 0, Threshold: 1}, "window"},
		{"negative window", siemRuleInput{Name: "n", Action: "a", WindowSec: -5, Threshold: 1}, "window"},
		{"zero threshold", siemRuleInput{Name: "n", Action: "a", WindowSec: 60, Threshold: 0}, "threshold"},
		{"negative threshold", siemRuleInput{Name: "n", Action: "a", WindowSec: 60, Threshold: -1}, "threshold"},
		{"missing alert action", siemRuleInput{Name: "n", Action: "a", WindowSec: 60, Threshold: 2}, "alert_action"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateSIEMRule(tc.req)
			if msg == "" {
				t.Fatal("an invalid rule was accepted")
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("message = %q, want it to name %q", msg, tc.want)
			}
		})
	}
}

// ── Role gate ───────────────────────────────────────────────────────────────

// TestSIEMRuleMutationsRequireOperator proves the four mutating endpoints
// re-check the role in the handler; the file had no gate at all, so any role
// holding settings.write could rewrite or silence the fleet's detection rules.
func TestSIEMRuleMutationsRequireOperator(t *testing.T) {
	s := mustTenantServer(t)
	seedSIEMRule(t, s, "gate", "login_failed", 300, 5, true)

	var rule db.SIEMRule
	if err := s.db.Where("name = ?", "gate").First(&rule).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}

	cases := []struct {
		name   string
		method string
		id     string
		body   string
		invoke func(*Server, *gin.Context)
	}{
		{"create", http.MethodPost, "", `{"name":"x","action":"a","window_sec":60,"threshold":2,"alert_action":"siem_alert"}`, (*Server).handleCreateSIEMRule},
		{"update", http.MethodPut, "1", `{"name":"x","action":"a","window_sec":60,"threshold":2,"alert_action":"siem_alert"}`, (*Server).handleUpdateSIEMRule},
		{"toggle", http.MethodPost, "1", `{}`, (*Server).handleToggleSIEMRule},
		{"delete", http.MethodDelete, "1", "", (*Server).handleDeleteSIEMRule},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := siemJSONCtx(s, t, "siem-viewer", "viewer", tc.method, tc.body, tc.id)
			tc.invoke(s, c)
			if w.Code != http.StatusForbidden {
				t.Fatalf("viewer status = %d, want 403 (%s)", w.Code, w.Body.String())
			}
		})
	}

	// Nothing changed.
	var survivor db.SIEMRule
	if err := s.db.Where("name = ?", "gate").First(&survivor).Error; err != nil {
		t.Fatalf("the seeded rule was destroyed by a refused request: %v", err)
	}
	if !survivor.Enabled || survivor.Action != "login_failed" {
		t.Fatalf("a viewer mutated the rule: %+v", survivor)
	}
	var count int64
	if err := s.db.Model(&db.SIEMRule{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d rules exist, want 1", count)
	}
}

// ── Lifecycle ───────────────────────────────────────────────────────────────

// TestSIEMRuleLifecycle proves create → list → update → toggle → delete work for
// an operator, and that each mutation is audited.
func TestSIEMRuleLifecycle(t *testing.T) {
	s := mustTenantServer(t)

	c, w := siemJSONCtx(s, t, "siem-op", "operator", http.MethodPost,
		`{"name":"password spray","action":"login_failed","window_sec":300,"threshold":5,"alert_action":"siem_alert"}`, "")
	s.handleCreateSIEMRule(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	var created db.SIEMRule
	if err := s.db.Where("name = ?", "password spray").First(&created).Error; err != nil {
		t.Fatalf("created rule not persisted: %v", err)
	}
	if !created.Enabled || created.Threshold != 5 || created.WindowSec != 300 {
		t.Fatalf("stored rule = %+v", created)
	}

	c2, w2 := siemCtx(s, t, "siem-op", "operator")
	c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	s.handleListSIEMRules(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("list: %d", w2.Code)
	}
	var listing struct {
		Rules []db.SIEMRule `json:"rules"`
	}
	unmarshalData(t, w2.Body.Bytes(), &listing)
	if len(listing.Rules) != 1 || listing.Rules[0].Name != "password spray" {
		t.Fatalf("listing = %+v", listing.Rules)
	}

	id := strconvOf(created.ID)
	c3, w3 := siemJSONCtx(s, t, "siem-op", "operator", http.MethodPut,
		`{"name":"spray tuned","action":"login_failed","window_sec":600,"threshold":9,"enabled":false,"alert_action":"siem_alert"}`, id)
	s.handleUpdateSIEMRule(c3)
	if w3.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w3.Code, w3.Body.String())
	}
	var updated db.SIEMRule
	if err := s.db.Where("id = ?", created.ID).First(&updated).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if updated.Name != "spray tuned" || updated.Threshold != 9 || updated.WindowSec != 600 || updated.Enabled {
		t.Fatalf("stored rule after update = %+v", updated)
	}

	c4, w4 := siemJSONCtx(s, t, "siem-op", "operator", http.MethodPost, `{}`, id)
	s.handleToggleSIEMRule(c4)
	if w4.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", w4.Code, w4.Body.String())
	}
	if !strings.Contains(w4.Body.String(), `"enabled":true`) {
		t.Fatalf("body = %s, want the toggle re-enabled", w4.Body.String())
	}

	c5, w5 := siemJSONCtx(s, t, "siem-op", "operator", http.MethodDelete, "", id)
	s.handleDeleteSIEMRule(c5)
	if w5.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w5.Code, w5.Body.String())
	}
	var count int64
	if err := s.db.Model(&db.SIEMRule{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d rules survived the delete", count)
	}

	for _, action := range []string{"siem_rule_create", "siem_rule_update", "siem_rule_toggle", "siem_rule_delete"} {
		var n int64
		if err := s.db.Model(&db.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", action, err)
		}
		if n != 1 {
			t.Fatalf("%s audit rows = %d, want 1", action, n)
		}
	}
}

// TestSIEMRuleHandlersGhostID404 proves every id-taking endpoint answers 404 for
// an unknown id instead of reporting success.
func TestSIEMRuleHandlersGhostID404(t *testing.T) {
	s := mustTenantServer(t)

	cases := []struct {
		name   string
		method string
		body   string
		invoke func(*Server, *gin.Context)
	}{
		{"update", http.MethodPut, `{"name":"x","action":"a","window_sec":60,"threshold":2,"alert_action":"siem_alert"}`, (*Server).handleUpdateSIEMRule},
		{"toggle", http.MethodPost, `{}`, (*Server).handleToggleSIEMRule},
		{"delete", http.MethodDelete, "", (*Server).handleDeleteSIEMRule},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := siemJSONCtx(s, t, "siem-op", "operator", tc.method, tc.body, "999999")
			tc.invoke(s, c)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body.String())
			}
		})
	}
}

// TestSIEMRuleCreateRejectsInvalidBody proves an invalid rule never reaches the
// database.
func TestSIEMRuleCreateRejectsInvalidBody(t *testing.T) {
	s := mustTenantServer(t)

	for _, body := range []string{
		`{}`,
		`{"name":"x"}`,
		`{"action":"a"}`,
		`{"name":"x","action":"a","alert_action":"siem_alert"}`,
		`{"name":"x","action":"a","window_sec":0,"threshold":2}`,
		`{"name":"x","action":"a","window_sec":60,"threshold":0}`,
		`{"name":`,
		`not json`,
	} {
		c, w := siemJSONCtx(s, t, "siem-op", "operator", http.MethodPost, body, "")
		s.handleCreateSIEMRule(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (%s)", body, w.Code, w.Body.String())
		}
	}
	var count int64
	if err := s.db.Model(&db.SIEMRule{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("an invalid create stored %d rules", count)
	}
}

// TestSIEMRuleCreateHonoursExplicitEnabled proves an explicit false is respected
// while an omitted value defaults to enabled.
func TestSIEMRuleCreateHonoursExplicitEnabled(t *testing.T) {
	s := mustTenantServer(t)

	c, w := siemJSONCtx(s, t, "siem-op", "operator", http.MethodPost,
		`{"name":"disabled","action":"a","window_sec":60,"threshold":2,"enabled":false,"alert_action":"siem_alert"}`, "")
	s.handleCreateSIEMRule(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create disabled: %d %s", w.Code, w.Body.String())
	}
	var disabled db.SIEMRule
	if err := s.db.Where("name = ?", "disabled").First(&disabled).Error; err != nil {
		t.Fatalf("reload disabled: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("explicit enabled=false was ignored")
	}

	c2, w2 := siemJSONCtx(s, t, "siem-op", "operator", http.MethodPost,
		`{"name":"defaulted","action":"a","window_sec":60,"threshold":2,"alert_action":"siem_alert"}`, "")
	s.handleCreateSIEMRule(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("create default: %d %s", w2.Code, w2.Body.String())
	}
	// A fresh variable: a reused struct carries its primary key, which GORM
	// folds into the next First's WHERE clause.
	var defaulted db.SIEMRule
	if err := s.db.Where("name = ?", "defaulted").First(&defaulted).Error; err != nil {
		t.Fatalf("reload defaulted: %v", err)
	}
	if !defaulted.Enabled {
		t.Fatal("an omitted enabled must default to true")
	}
}

// ── Correlation engine ──────────────────────────────────────────────────────

// TestReloadSIEMRules proves a rules reload is safe with no SIEM subsystem and
// that reloading applies the enabled custom rules while dropping paused ones.
func TestReloadSIEMRules(t *testing.T) {
	s := mustTenantServer(t)

	// A nil subsystem must not panic.
	s.reloadSIEMRules()

	custom := seedSIEMRule(t, s, "custom", "login_failed", 600, 3, true)
	paused := seedSIEMRule(t, s, "paused", "login_failed", 600, 3, false)

	sw := NewSIEMWebhook(s, "https://siem.example.com/hook", "token", "siem_alert")
	s.siem = sw
	s.reloadSIEMRules()

	var sawCustom, sawPaused bool
	for _, r := range sw.correlator.rules {
		if r.Name == paused.Name {
			sawPaused = true
		}
		if r.Name == custom.Name {
			sawCustom = true
			if r.Threshold != 3 || r.Window.Seconds() != 600 || r.AlertAction != "siem_alert" {
				t.Fatalf("rule = %+v", r)
			}
		}
	}
	if sawPaused {
		t.Fatal("a paused rule was loaded into the correlator")
	}
	if !sawCustom {
		t.Fatal("an enabled custom rule was not loaded into the correlator")
	}
}

// TestSIEMWebhookReloadRulesFallsBackToDefault pins the documented fallback: a
// database that cannot answer leaves the correlator with the built-in rules
// rather than an empty ruleset (which would blind all detection).
func TestSIEMWebhookReloadRulesFallsBackToDefault(t *testing.T) {
	s := mustTenantServer(t)
	sw := NewSIEMWebhook(s, "https://siem.example.com/hook", "token", "siem_alert")

	defaults := sw.correlator.rules
	if len(defaults) == 0 {
		t.Fatal("the SIEM webhook has no built-in correlation rules")
	}

	// An enabled custom rule replaces the defaults entirely.
	custom := seedSIEMRule(t, s, "override-default", "login_failed", 600, 3, true)
	sw.ReloadRules()
	loaded := sw.correlator.rules
	if len(loaded) != 1 || loaded[0].Name != custom.Name {
		t.Fatalf("loaded rules = %+v, want only the custom one", loaded)
	}

	// A database that cannot answer falls back to the defaults, not to silence
	// and not to the custom rules it could not read.
	if err := s.db.Exec("DROP TABLE siem_rules").Error; err != nil {
		t.Fatalf("drop siem_rules: %v", err)
	}
	sw.ReloadRules()
	after := sw.correlator.rules
	if len(after) != len(defaults) {
		t.Fatalf("after a failed load %d rules are active, want the %d built-in defaults",
			len(after), len(defaults))
	}
	for _, r := range after {
		if r.Name == custom.Name {
			t.Fatal("a rule that could not be read stayed active")
		}
	}
}

// ── Small local helpers ─────────────────────────────────────────────────────

func jsonUnmarshal(data []byte, dest interface{}) error {
	return json.Unmarshal(data, dest)
}

func strconvOf(id uint) string {
	return fmt.Sprint(id)
}
