package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAutomationRulePreview covers the dry run: "would this rule fire for this event, and if not, why not".
//
// The failure it exists for is a rule that silently does nothing - a condition on a field the event does not
// carry, an open cooldown, a disabled rule, a mismatched event type - none of which is visible from the rule
// list, and the alternative is waiting for a real beacon, which costs an engagement. The one property that
// must hold is that a preview executes NOTHING.
func TestAutomationRulePreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	conditions, _ := json.Marshal([]RuleCondition{
		{Field: "agent.hostname", Operator: "contains", Value: "web"},
		{Field: "data.user", Operator: "equals", Value: "alice"},
	})
	actions, _ := json.Marshal([]RuleAction{
		{Type: ActionNotify, Params: json.RawMessage(`{"message":"host {{hostname}} seen for {{user}}"}`)},
	})

	seed := func(id, name string, tenant uint, enabled bool, cooldownSeconds int, lastTriggered time.Time) db.AutomationRule {
		rule := db.AutomationRule{
			ID: id, Name: name, Enabled: enabled, EventType: string(EventImplantCheckin),
			Conditions: string(conditions), Actions: string(actions), TenantID: tenant,
			CooldownSeconds: cooldownSeconds, LastTriggered: lastTriggered,
		}
		if err := s.db.Create(&rule).Error; err != nil {
			t.Fatalf("seed rule %s: %v", id, err)
		}
		// Create goes through GORM defaults, so a fixture that wants a DISABLED rule must write the column
		// explicitly - the same trap the production save path had to work around.
		if !enabled {
			if err := s.db.Model(&db.AutomationRule{}).Where("id = ?", id).Update("enabled", false).Error; err != nil {
				t.Fatalf("disable rule %s: %v", id, err)
			}
			rule.Enabled = false
		}
		return rule
	}
	seed("matching", "matching rule", 1, true, 0, time.Time{})
	seed("disabled", "disabled rule", 1, false, 0, time.Time{})
	seed("cooling", "cooling rule", 1, true, 3600, time.Now().Add(-time.Minute))
	seed("foreign", "other tenant rule", 2, true, 0, time.Time{})

	call := func(ruleID string, body map[string]interface{}, tenant uint) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/rules/"+ruleID+"/preview", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: ruleID}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handlePreviewAutomationRule(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	matchingEvent := map[string]interface{}{
		"event_type": string(EventImplantCheckin), "agent_id": "a-1", "agent_host": "web-01",
		"user": "alice", "data": map[string]interface{}{"user": "alice"},
	}

	// 1. A matching rule reports would_run, with both conditions resolved.
	code, data := call("matching", matchingEvent, 1)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["would_run"] != true || data["matched"] != true {
		t.Fatalf("matching rule = %v", data)
	}
	conds, _ := data["conditions"].([]interface{})
	if len(conds) != 2 {
		t.Fatalf("conditions = %v, want both", data["conditions"])
	}
	first, _ := conds[0].(map[string]interface{})
	if first["actual"] != "web-01" || first["matched"] != true {
		t.Fatalf("the resolved condition = %v, want actual web-01", first)
	}
	// The action template is expanded, because an unexpanded template hides a broken field reference.
	acts, _ := data["actions"].([]interface{})
	if len(acts) != 1 {
		t.Fatalf("actions = %v", data["actions"])
	}
	action, _ := acts[0].(map[string]interface{})
	if !containsFold(action["template"].(string), "web-01") || !containsFold(action["template"].(string), "alice") {
		t.Fatalf("action template = %v, want the event values expanded", action["template"])
	}

	// 2. A condition that does not match says so, and names both sides.
	code, data = call("matching", map[string]interface{}{
		"event_type": string(EventImplantCheckin), "agent_host": "db-01",
		"data": map[string]interface{}{"user": "bob"},
	}, 1)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["would_run"] != false || data["matched"] != false {
		t.Fatalf("non-matching rule = %v", data)
	}
	reasons, _ := data["reasons"].([]interface{})
	if len(reasons) < 2 {
		t.Fatalf("reasons = %v, want one per failed condition", reasons)
	}

	// 3. A disabled rule explains itself.
	_, data = call("disabled", matchingEvent, 1)
	if data["would_run"] != false {
		t.Fatalf("a disabled rule would run: %v", data)
	}
	found := false
	for _, reason := range data["reasons"].([]interface{}) {
		if containsFold(reason.(string), "disabled") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons = %v, want one naming the disabled state", data["reasons"])
	}

	// 4. An open cooldown blocks a rule whose conditions match - the case operators most often mistake for
	// "the rule is broken".
	_, data = call("cooling", matchingEvent, 1)
	if data["matched"] != true {
		t.Fatalf("the cooling rule's conditions did not match: %v", data)
	}
	if data["would_run"] != false || data["cooldown_open"] != true {
		t.Fatalf("cooling rule = %v", data)
	}
	if remaining, _ := data["cooldown_remaining"].(float64); remaining <= 0 {
		t.Fatalf("cooldown_remaining = %v, want a positive number", data["cooldown_remaining"])
	}

	// 5. A mismatched event type is reported rather than silently ignored.
	_, data = call("matching", map[string]interface{}{
		"event_type": string(EventTaskComplete), "agent_host": "web-01", "data": map[string]interface{}{"user": "alice"},
	}, 1)
	if data["would_run"] != false {
		t.Fatalf("an event of the wrong type ran the rule: %v", data)
	}

	// 6. Nothing was executed: the preview must not create notifications, tasks or audit rows of its own
	// beyond the preview record.
	var notifications int64
	if err := s.db.Model(&db.Notification{}).Count(&notifications).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications != 0 {
		t.Fatalf("a preview created %d notification(s); it must execute nothing", notifications)
	}
	var previews int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "automation_preview").Count(&previews).Error; err != nil {
		t.Fatalf("count previews: %v", err)
	}
	// Five previews before this point: matching, non-matching, disabled, cooling, wrong event type. The
	// two 404 cases below must NOT add a row, which is why they run after this assertion.
	if previews != 5 {
		t.Fatalf("automation_preview rows = %d, want 5", previews)
	}

	// 7. Another tenant's rule is a 404, and so is a rule that does not exist.
	if code, _ := call("foreign", matchingEvent, 1); code != http.StatusNotFound {
		t.Fatalf("cross-tenant preview = %d, want 404", code)
	}
	if code, _ := call("does-not-exist", matchingEvent, 1); code != http.StatusNotFound {
		t.Fatalf("missing rule = %d, want 404", code)
	}
}

// TestSaveAutomationRulePersistsDisabled keeps the bug the preview test found: creating a rule with
// enabled=false must not come back enabled. INSERT applies the column default (true), so without an explicit
// write a "staged, disabled" rule starts firing on its own - the worst kind of success report.
func TestSaveAutomationRulePersistsDisabled(t *testing.T) {
	s := newTasksTestServer(t)

	rule := AutomationRule{
		ID: "staged-rule", Name: "staged", Enabled: false, EventType: string(EventImplantCheckin),
		Conditions: []RuleCondition{{Field: "agent.hostname", Operator: "contains", Value: "web"}},
		Actions:    []RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"staged"}`)}},
	}
	if err := s.saveAutomationRule(rule); err != nil {
		t.Fatalf("save: %v", err)
	}

	var stored db.AutomationRule
	if err := s.db.Where("id = ?", "staged-rule").First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Enabled {
		t.Fatal("a rule saved as disabled came back enabled; it would fire on its own")
	}

	// Enabling it later must also persist, in the other direction.
	rule.Enabled = true
	if err := s.saveAutomationRule(rule); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if err := s.db.Where("id = ?", "staged-rule").First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !stored.Enabled {
		t.Fatal("a rule saved as enabled came back disabled")
	}
}
