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

// TestAutomationRuleTestFire covers the button that answers "do this rule's actions actually work".
//
// It EXECUTES, so the test pins the three guards: confirmation is mandatory, the cooldown holds, and a dispatch failure is
// reported rather than swallowed. The last one matters most: the whole reason this endpoint exists is that an action which
// never lands looked identical to one that worked.
func TestAutomationRuleTestFire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetAutomationTests()
	t.Cleanup(resetAutomationTests)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// A rule that notifies (cannot fail to dispatch) and one whose command targets a missing agent (must report the failure).
	notifyConditions, _ := json.Marshal([]RuleCondition{})
	notifyActions, _ := json.Marshal([]RuleAction{
		{Type: ActionNotify, Params: json.RawMessage(`{"message":"test fire for {{hostname}}"}`)},
	})
	// A macro that does not exist: the dispatcher reports this, whereas a command aimed at a missing agent does not (the task
	// row is simply created). The endpoint's value is reporting what the DISPATCHER knows.
	badActions, _ := json.Marshal([]RuleAction{
		{Type: ActionRunMacro, Params: json.RawMessage(`{"macro_id": 9999}`)},
	})
	for _, rule := range []db.AutomationRule{
		{ID: "notify-rule", Name: "notify rule", Enabled: true, EventType: string(EventImplantCheckin),
			Conditions: string(notifyConditions), Actions: string(notifyActions), TenantID: 1, AgentID: "a-1"},
		{ID: "broken-rule", Name: "broken rule", Enabled: true, EventType: string(EventImplantCheckin),
			Conditions: string(notifyConditions), Actions: string(badActions), TenantID: 1},
	} {
		rule := rule
		if err := s.db.Create(&rule).Error; err != nil {
			t.Fatalf("seed %s: %v", rule.ID, err)
		}
	}
	s.invalidateAutomationCache()

	call := func(id string, body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/rules/x/test", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleTestAutomationRule(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. Without confirmation it refuses: this executes.
	if code, _ := call("notify-rule", map[string]interface{}{}); code != http.StatusBadRequest {
		t.Fatalf("no confirmation = %d, want 400", code)
	}
	if code, _ := call("notify-rule", map[string]interface{}{"confirm": false}); code != http.StatusBadRequest {
		t.Fatalf("confirm=false = %d, want 400", code)
	}
	// Nothing ran, so the audit says nothing either.
	var before int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "automation_rule_test").Count(&before).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if before != 0 {
		t.Fatalf("a refused request was audited: %d", before)
	}

	// 2. With confirmation the actions run, and the answer names the synthetic event.
	code, data := call("notify-rule", map[string]interface{}{"confirm": true})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["executed"] != float64(1) || data["failed"] != float64(0) {
		t.Fatalf("notify rule = %v", data)
	}
	event, _ := data["event"].(map[string]interface{})
	if event["hostname"] != "web-01" {
		t.Fatalf("the synthetic event does not name the agent: %v", event)
	}
	if note, _ := data["note"].(string); note == "" {
		t.Fatal("the answer does not explain the webhook caveat")
	}

	// 3. A dispatch failure is reported with its reason - the point of the endpoint.
	code, data = call("broken-rule", map[string]interface{}{"confirm": true})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["failed"] != float64(1) {
		t.Fatalf("a failing action was reported as success: %v", data)
	}
	errs, _ := data["errors"].([]interface{})
	if len(errs) != 1 {
		t.Fatalf("errors = %v", errs)
	}
	// The failure is also recorded against the rule, which is what the health report reads.
	if stats := ruleActionStatsSnapshot(); stats["broken-rule"].Failed != 1 {
		t.Fatalf("the action counters did not see the failure: %+v", stats["broken-rule"])
	}

	// 4. The cooldown holds, so a test button cannot become a flood of dispatched tasks.
	if code, _ := call("broken-rule", map[string]interface{}{"confirm": true}); code != http.StatusTooManyRequests {
		t.Fatalf("a repeated test-fire = %d, want 429", code)
	}

	// 5. Every executed test is audited.
	var after int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "automation_rule_test").Count(&after).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if after != 2 {
		t.Fatalf("automation_rule_test rows = %d, want 2 (one per executed test)", after)
	}
}
