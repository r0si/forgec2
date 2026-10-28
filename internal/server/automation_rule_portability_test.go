package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAutomationRulePortability covers exporting one rule and importing it into a second tenant.
//
// The feature exists because a rule set is configuration an operator rebuilds every exercise; retyping conditions is where a
// mistyped `contains` lives on as a rule that simply never matches. The tests pin what travels (meaning) and what does not
// (ids, timestamps, ownership), and that a name collision is refused rather than silently replacing a hand-built rule.
func TestAutomationRulePortability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	conditions, _ := json.Marshal([]RuleCondition{{Field: "agent.hostname", Operator: "contains", Value: "web"}})
	actions, _ := json.Marshal([]RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"seen {{hostname}}"}`)}})
	source := db.AutomationRule{
		ID: "src-1", Name: "Web check-in", Enabled: true, EventType: string(EventImplantCheckin),
		Conditions: string(conditions), Actions: string(actions), CooldownSeconds: 120, TenantID: 1,
	}
	if err := s.db.Create(&source).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	call := func(method, path string, tenant uint, body interface{}) (int, map[string]interface{}) {
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, path, reader)
		c.Request.Header.Set("Content-Type", "application/json")
		if path == "/export" {
			c.Params = gin.Params{{Key: "id", Value: "src-1"}}
		}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		if method == http.MethodGet {
			s.handleExportAutomationRule(c)
		} else {
			s.handleImportAutomationRule(c)
		}
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. Export carries the meaning and nothing deployment-specific.
	code, data := call(http.MethodGet, "/export", 1, nil)
	if code != http.StatusOK {
		t.Fatalf("export status = %d", code)
	}
	exported, _ := data["rule"].(map[string]interface{})
	if exported["name"] != "Web check-in" || exported["event_type"] != string(EventImplantCheckin) {
		t.Fatalf("exported rule = %v", exported)
	}
	if exported["cooldown_seconds"] != float64(120) {
		t.Fatalf("the cooldown did not travel: %v", exported["cooldown_seconds"])
	}
	for _, banned := range []string{"id", "created_at", "last_triggered", "run_count", "tenant_id"} {
		if _, present := exported[banned]; present {
			t.Fatalf("the export carries deployment state (%q): %v", banned, exported)
		}
	}
	conds, _ := exported["conditions"].([]interface{})
	acts, _ := exported["actions"].([]interface{})
	if len(conds) != 1 || len(acts) != 1 {
		t.Fatalf("conditions/actions did not travel: %v / %v", conds, acts)
	}

	// 2. Another tenant cannot export it.
	if code, _ := call(http.MethodGet, "/export", 2, nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant export = %d, want 404", code)
	}

	// 3. Importing into the second tenant creates a rule with a derived id, enabled, with the timing intact.
	code, data = call(http.MethodPost, "/import", 2, map[string]interface{}{"rule": exported})
	if code != http.StatusOK {
		t.Fatalf("import status = %d (%v)", code, data)
	}
	if data["overwrote"] != false {
		t.Fatalf("a first import reported an overwrite: %v", data)
	}
	importedID, _ := data["rule_id"].(string)
	var imported db.AutomationRule
	if err := s.db.First(&imported, "id = ?", importedID).Error; err != nil {
		t.Fatalf("read imported rule: %v", err)
	}
	if imported.TenantID != 2 || !imported.Enabled || imported.CooldownSeconds != 120 {
		t.Fatalf("imported rule = %+v", imported)
	}
	if imported.Name != "Web check-in" {
		t.Fatalf("imported name = %q", imported.Name)
	}

	// 4. Importing the same document again is refused rather than creating a twin.
	if code, _ := call(http.MethodPost, "/import", 2, map[string]interface{}{"rule": exported}); code != http.StatusConflict {
		t.Fatalf("duplicate import = %d, want 409", code)
	}
	// ... and with overwrite it replaces the same row instead of adding one.
	code, data = call(http.MethodPost, "/import", 2, map[string]interface{}{"rule": exported, "overwrite": true})
	if code != http.StatusOK || data["overwrote"] != true {
		t.Fatalf("overwrite import = %d (%v)", code, data)
	}
	if data["rule_id"] != importedID {
		t.Fatalf("overwrite created a new row (%v != %v)", data["rule_id"], importedID)
	}
	var count int64
	if err := s.db.Model(&db.AutomationRule{}).Where("name = ?", "Web check-in").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("rules named %q = %d, want one per tenant", "Web check-in", count)
	}

	// 5. Malformed documents are refused with a reason.
	for _, body := range []map[string]interface{}{
		{"rule": map[string]interface{}{"event_type": "implant.checkin", "actions": []interface{}{map[string]interface{}{"type": "notify"}}}},
		{"rule": map[string]interface{}{"name": "no event", "actions": []interface{}{map[string]interface{}{"type": "notify"}}}},
		{"rule": map[string]interface{}{"name": "no actions", "event_type": "implant.checkin"}},
	} {
		if code, _ := call(http.MethodPost, "/import", 1, body); code != http.StatusBadRequest {
			t.Fatalf("%v = %d, want 400", body, code)
		}
	}

	// 6. Both directions are audited.
	for _, action := range []string{"automation_rule_import", "automation_rule_import_overwrite"} {
		var n int64
		if err := s.db.Model(&db.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", action, err)
		}
		if n != 1 {
			t.Fatalf("%s rows = %d, want 1", action, n)
		}
	}
}
