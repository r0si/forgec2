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

// TestAutomationDiagnose covers the composed answer: one call for "what fires for this event, and who would be
// told". Both halves already existed as separate previews; the value here is that an operator debugging a set no
// longer has to run two endpoints and correlate them by hand - and that the composition executes nothing.
func TestAutomationDiagnose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// One rule that fires, one that is cooling down.
	conditions, _ := json.Marshal([]RuleCondition{{Field: "agent.hostname", Operator: "contains", Value: "web"}})
	actions, _ := json.Marshal([]RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"seen"}`)}})
	for _, rule := range []db.AutomationRule{
		{ID: "fires", Name: "fires", Enabled: true, EventType: string(EventImplantCheckin),
			Conditions: string(conditions), Actions: string(actions), TenantID: 1},
		{ID: "cooling", Name: "cooling", Enabled: true, EventType: string(EventImplantCheckin),
			Conditions: string(conditions), Actions: string(actions), TenantID: 1,
			CooldownSeconds: 3600, LastTriggered: time.Now().Add(-time.Minute)},
	} {
		rule := rule
		if err := s.db.Create(&rule).Error; err != nil {
			t.Fatalf("seed rule: %v", err)
		}
	}
	// One route that would deliver and one that is disabled.
	for i, enabled := range []bool{true, false} {
		route := db.NotificationRoute{
			Name: "route-" + strconvItoa(i), Channel: "discord",
			Target: "https://discord.com/api/webhooks/1/abc", MinSeverity: "info", Enabled: enabled,
		}
		if err := s.db.Create(&route).Error; err != nil {
			t.Fatalf("seed route: %v", err)
		}
		if !enabled {
			if err := s.db.Model(&db.NotificationRoute{}).Where("id = ?", route.ID).
				Update("enabled", false).Error; err != nil {
				t.Fatalf("disable route: %v", err)
			}
		}
	}
	s.invalidateAutomationCache()

	call := func(body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/diagnose", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleAutomationDiagnose(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	code, data := call(map[string]interface{}{
		"event_type": string(EventImplantCheckin), "agent_host": "web-01",
		"data":         map[string]interface{}{"user": "alice"},
		"notification": map[string]interface{}{"title": "checkin", "severity": "info"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	automation, ok := data["automation"].(map[string]interface{})
	if !ok {
		t.Fatalf("no automation section: %v", data)
	}
	summary, _ := automation["summary"].(map[string]interface{})
	if summary["would_run"] != float64(1) || summary["blocked_by_cooldown"] != float64(1) {
		t.Fatalf("automation summary = %v", summary)
	}

	notifications, ok := data["notifications"].(map[string]interface{})
	if !ok {
		t.Fatalf("no notifications section: %v", data)
	}
	if notifications["would_deliver"] != float64(1) || notifications["total_routes"] != float64(2) {
		t.Fatalf("notification section = %v", notifications)
	}

	// A malformed request is refused before either half runs.
	if got, _ := call(map[string]interface{}{"agent_host": "web-01"}); got != http.StatusBadRequest {
		t.Fatalf("missing event_type = %d, want 400", got)
	}
	if got, _ := call(map[string]interface{}{
		"event_type":   string(EventImplantCheckin),
		"notification": map[string]interface{}{"severity": "catastrophic"},
	}); got != http.StatusBadRequest {
		t.Fatalf("invalid severity = %d, want 400", got)
	}

	// Nothing ran: no notifications persisted, no tasks created.
	var notificationsPersisted int64
	if err := s.db.Model(&db.Notification{}).Count(&notificationsPersisted).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notificationsPersisted != 0 {
		t.Fatalf("a diagnosis persisted %d notification(s)", notificationsPersisted)
	}
	var tasks int64
	if err := s.db.Model(&db.Task{}).Count(&tasks).Error; err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if tasks != 0 {
		t.Fatalf("a diagnosis created %d task(s)", tasks)
	}

	// The single successful call is audited.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "automation_diagnose").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("automation_diagnose rows = %d, want 1", audited)
	}
}

// strconvItoa renders a small int for fixture names.
func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	return string(rune('0' + v))
}
