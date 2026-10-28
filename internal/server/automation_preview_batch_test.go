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

// TestAutomationPreviewBatch covers the other direction from the single-rule preview: given an event, which
// rules would fire. The single preview answers "why did THIS rule not fire"; while designing an automation set
// the question is "what will happen when this event arrives", including the interaction between rules.
func TestAutomationPreviewBatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	conditions, _ := json.Marshal([]RuleCondition{
		{Field: "agent.hostname", Operator: "contains", Value: "web"},
	})
	actions, _ := json.Marshal([]RuleAction{
		{Type: ActionNotify, Params: json.RawMessage(`{"message":"seen {{hostname}}"}`)},
	})

	seed := func(id, name, eventType string, tenant uint, enabled bool, cooldownSeconds int, lastTriggered time.Time) {
		rule := db.AutomationRule{
			ID: id, Name: name, Enabled: enabled, EventType: eventType,
			Conditions: string(conditions), Actions: string(actions), TenantID: tenant,
			CooldownSeconds: cooldownSeconds, LastTriggered: lastTriggered,
		}
		if err := s.db.Create(&rule).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if !enabled {
			if err := s.db.Model(&db.AutomationRule{}).Where("id = ?", id).Update("enabled", false).Error; err != nil {
				t.Fatalf("disable %s: %v", id, err)
			}
		}
	}

	seed("fires", "a-fires", string(EventImplantCheckin), 1, true, 0, time.Time{})
	seed("cooling", "b-cooling", string(EventImplantCheckin), 1, true, 3600, time.Now().Add(-time.Minute))
	seed("disabled", "c-disabled", string(EventImplantCheckin), 1, false, 0, time.Time{})
	seed("other-event", "d-other-event", string(EventTaskComplete), 1, true, 0, time.Time{})
	seed("foreign", "e-foreign", string(EventImplantCheckin), 2, true, 0, time.Time{})
	// Invalidate the cached snapshot so the batch sees the rows just seeded.
	s.invalidateAutomationCache()

	call := func(body map[string]interface{}, tenant uint) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/preview", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handlePreviewAutomationBatch(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	code, data := call(map[string]interface{}{
		"event_type": string(EventImplantCheckin), "agent_host": "web-01",
		"data": map[string]interface{}{"user": "alice"},
	}, 1)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	// Only rules for this event and this tenant are reported: the task.complete rule and the other tenant's
	// rule would drown the answer with "not firing" noise.
	rules, _ := data["rules"].([]interface{})
	if len(rules) != 3 {
		t.Fatalf("rules = %v, want the three implant.checkin rules of this tenant", rules)
	}

	summary, _ := data["summary"].(map[string]interface{})
	for label, want := range map[string]float64{
		"total": 3, "would_run": 1, "blocked_by_cooldown": 1, "disabled": 1, "blocked_by_conditions": 0,
	} {
		if got := summary[label]; got != want {
			t.Fatalf("summary[%s] = %v, want %v (%v)", label, got, want, summary)
		}
	}

	// The firing rule comes first, so the answer opens with what happens.
	first, _ := rules[0].(map[string]interface{})
	if first["rule_name"] != "a-fires" || first["would_run"] != true {
		t.Fatalf("first rule = %v, want a-fires first", first)
	}
	// The cooling rule matched but is blocked, and carries the reason an operator needs.
	var cooling map[string]interface{}
	for _, raw := range rules {
		rule, _ := raw.(map[string]interface{})
		if rule["rule_name"] == "b-cooling" {
			cooling = rule
		}
	}
	if cooling == nil {
		t.Fatal("the cooling rule is missing from the report")
	}
	if cooling["matched"] != true || cooling["would_run"] != false || cooling["cooldown_open"] != true {
		t.Fatalf("cooling rule = %v, want matched but blocked by cooldown", cooling)
	}

	// A missing event type is refused: judging every rule against an empty event would be meaningless.
	if got, _ := call(map[string]interface{}{"agent_host": "web-01"}, 1); got != http.StatusBadRequest {
		t.Fatalf("missing event_type = %d, want 400", got)
	}

	// Another tenant sees only its own rule.
	code, data = call(map[string]interface{}{"event_type": string(EventImplantCheckin), "agent_host": "web-01"}, 2)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	otherRules, _ := data["rules"].([]interface{})
	if len(otherRules) != 1 {
		t.Fatalf("tenant 2 rules = %v, want only its own", otherRules)
	}

	// Nothing was executed by the preview.
	var notifications int64
	if err := s.db.Model(&db.Notification{}).Count(&notifications).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications != 0 {
		t.Fatalf("a batch preview created %d notification(s)", notifications)
	}
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "automation_preview_batch").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 2 {
		t.Fatalf("automation_preview_batch rows = %d, want 2", audited)
	}
}
