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

	// 5b. The whole set exports and imports as one document, with a per-rule answer because a partial success is the
	// normal case in a bulk import.
	setCode, setData := callSet(s, http.MethodGet, "/export-set", 1, nil)
	if setCode != http.StatusOK {
		t.Fatalf("set export status = %d", setCode)
	}
	if setData["version"] != float64(1) {
		t.Fatalf("set version = %v", setData["version"])
	}
	setRules, _ := setData["rules"].([]interface{})
	if len(setRules) != 1 {
		t.Fatalf("the set export carries %d rule(s), want this tenant's one", len(setRules))
	}
	if setData["count"] != float64(1) {
		t.Fatalf("count = %v", setData["count"])
	}

	// Import the same set into the first tenant: one conflict (the source rule) and one creation.
	importSet := []interface{}{setRules[0], map[string]interface{}{
		"name": "Second rule", "event_type": string(EventTaskComplete),
		"conditions": []interface{}{},
		"actions":    []interface{}{map[string]interface{}{"type": "notify", "params": map[string]interface{}{"message": "x"}}},
	}}
	batchCode, batchData := callSet(s, http.MethodPost, "/import", 1, map[string]interface{}{"rules": importSet})
	if batchCode != http.StatusOK {
		t.Fatalf("batch import status = %d", batchCode)
	}
	counts, _ := batchData["counts"].(map[string]interface{})
	if counts["conflict"] != float64(1) || counts["created"] != float64(1) || counts["replaced"] != float64(0) {
		t.Fatalf("batch counts = %v, want one conflict (the existing rule) and one creation", counts)
	}
	results, _ := batchData["results"].([]interface{})
	if len(results) != 2 {
		t.Fatalf("batch results = %v", results)
	}
	// The conflict names why, so the operator can decide to re-run with overwrite.
	for _, raw := range results {
		entry, _ := raw.(map[string]interface{})
		if entry["status"] == "conflict" && entry["reason"] == nil {
			t.Fatalf("a conflict was reported without a reason: %v", entry)
		}
	}

	// 4b. A reference that will not resolve here is reported at import time, while the operator is looking at the answer.
	orphan := map[string]interface{}{
		"name": "orphan target", "event_type": string(EventImplantCheckin),
		"conditions": []interface{}{},
		"actions": []interface{}{
			map[string]interface{}{"type": "run_command", "params": map[string]interface{}{"command": "whoami", "agent_id": "not-here"}},
			map[string]interface{}{"type": "run_macro", "params": map[string]interface{}{"macro_id": "9999"}},
		},
	}
	code, data = call(http.MethodPost, "/import", 1, map[string]interface{}{"rule": orphan})
	if code != http.StatusOK {
		t.Fatalf("import with unresolvable references = %d (%v)", code, data)
	}
	warnings, _ := data["warnings"].([]interface{})
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one for the agent and one for the macro", data["warnings"])
	}
	joined := ""
	for _, raw := range warnings {
		joined += raw.(string) + "\n"
	}
	if !containsFold(joined, "not-here") || !containsFold(joined, "9999") {
		t.Fatalf("the warnings do not name the references: %q", joined)
	}
	// The import still SUCCEEDS: a rule may legitimately arrive before the agent it targets.
	var orphanRow db.AutomationRule
	if err := s.db.First(&orphanRow, "name = ?", "orphan target").Error; err != nil {
		t.Fatalf("the rule was not imported: %v", err)
	}

	// 5c. A dry run answers the same question as the import and writes nothing: no rule, no import audit row.
	before, _ := countRules(t, s)
	dry := map[string]interface{}{
		"name": "dry run rule", "event_type": string(EventTaskComplete), "conditions": []interface{}{},
		"actions": []interface{}{map[string]interface{}{"type": "notify", "params": map[string]interface{}{"message": "x"}}},
	}
	code, data = call(http.MethodPost, "/import", 1, map[string]interface{}{"rule": dry, "dry_run": true})
	if code != http.StatusOK {
		t.Fatalf("dry run = %d (%v)", code, data)
	}
	if data["dry_run"] != true {
		t.Fatalf("the dry run did not say so: %v", data)
	}
	if data["rule_id"] == "" {
		t.Fatalf("the dry run did not say which id it would use: %v", data)
	}
	after, _ := countRules(t, s)
	if after != before {
		t.Fatalf("a dry run wrote %d rule(s)", after-before)
	}
	// And the same document imported for real then behaves normally.
	if code, _ := call(http.MethodPost, "/import", 1, map[string]interface{}{"rule": dry}); code != http.StatusOK {
		t.Fatalf("the real import after a preview = %d", code)
	}
	afterReal, _ := countRules(t, s)
	if afterReal != before+1 {
		t.Fatalf("the real import added %d rule(s)", afterReal-before)
	}

	// 6. Both directions are audited.
	// Four single imports (tenant-2, the batch's "Second rule", the orphan-target rule, and the dry-run rule imported
	// for real), one overwrite, one set summary. The DRY RUN writes no import row - that is the point of it.
	for action, want := range map[string]int64{
		"automation_rule_import":           4,
		"automation_rule_import_overwrite": 1,
		"automation_rule_import_set":       1,
		"automation_rule_import_preview":   0,
	} {
		var n int64
		if err := s.db.Model(&db.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", action, err)
		}
		if n != want {
			t.Fatalf("%s rows = %d, want %d", action, n, want)
		}
	}
}

// callSet drives the set-level handlers, which have no path parameter.
func callSet(s *Server, method, path string, tenant uint, body interface{}) (int, map[string]interface{}) {
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
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, tenant)
	if method == http.MethodGet {
		s.handleExportAutomationRules(c)
	} else {
		s.handleImportAutomationRule(c)
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Data
}

// countRules is how many automation rules exist in total, for the dry-run assertion.
func countRules(t *testing.T, s *Server) (int64, error) {
	t.Helper()
	var n int64
	err := s.db.Model(&db.AutomationRule{}).Count(&n).Error
	return n, err
}
