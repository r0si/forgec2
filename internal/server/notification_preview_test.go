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

// TestNotificationPreview covers the delivery dry run.
//
// DispatchNotification persists first and fans out in goroutines, so a route that filters the notification out,
// a disabled route and a route with an unusable target all look identical from the console: a notification that
// exists and reached nobody. The preview applies the same rules the sender does and says which of them applies.
func TestNotificationPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	seed := func(name, channel, target, secret, minSeverity string, enabled bool) {
		route := db.NotificationRoute{
			Name: name, Channel: channel, Target: target, Secret: secret,
			MinSeverity: minSeverity, Enabled: enabled,
		}
		if err := s.db.Create(&route).Error; err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if !enabled {
			// Same GORM default trap as automation rules: INSERT applies `default:true`.
			if err := s.db.Model(&db.NotificationRoute{}).Where("id = ?", route.ID).
				Update("enabled", false).Error; err != nil {
				t.Fatalf("disable %s: %v", name, err)
			}
		}
	}

	seed("discord-ok", "discord", "https://discord.com/api/webhooks/1/abc", "", "info", true)
	seed("disabled", "discord", "https://discord.com/api/webhooks/2/def", "", "info", false)
	seed("critical-only", "webhook", "https://example.com/hook", "", "critical", true)
	seed("telegram-no-token", "telegram", "12345", "", "info", true)
	seed("webhook-bad-url", "webhook", "not-a-url", "", "info", true)

	call := func(body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/notification-routes/preview", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handlePreviewNotification(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	code, data := call(map[string]interface{}{"title": "beacon down", "message": "a-1 stopped", "severity": "warning"})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	routes, _ := data["routes"].([]interface{})
	if len(routes) != 5 {
		t.Fatalf("routes = %d, want all five", len(routes))
	}
	byName := map[string]map[string]interface{}{}
	for _, raw := range routes {
		route, _ := raw.(map[string]interface{})
		byName[route["name"].(string)] = route
	}

	// Only the healthy route delivers.
	if got := byName["discord-ok"]; got["would_deliver"] != true {
		t.Fatalf("healthy route = %v", got)
	}
	// Each failure names its own reason, so the operator can fix the right thing.
	if got := byName["disabled"]; got["would_deliver"] != false || !containsFold(got["reason"].(string), "disabled") {
		t.Fatalf("disabled route = %v", got)
	}
	if got := byName["critical-only"]; got["would_deliver"] != false || !containsFold(got["reason"].(string), "below") {
		t.Fatalf("severity-filtered route = %v", got)
	}
	if got := byName["telegram-no-token"]; got["would_deliver"] != false || !containsFold(got["reason"].(string), "bot token") {
		t.Fatalf("telegram route = %v", got)
	}
	if got := byName["webhook-bad-url"]; got["would_deliver"] != false {
		t.Fatalf("invalid-URL route = %v", got)
	}

	if data["would_deliver"] != float64(1) || data["total_routes"] != float64(5) {
		t.Fatalf("summary = would_deliver %v of %v", data["would_deliver"], data["total_routes"])
	}

	// A critical notification also reaches the critical-only route.
	_, data = call(map[string]interface{}{"title": "boom", "severity": "critical"})
	if data["would_deliver"] != float64(2) {
		t.Fatalf("critical delivery count = %v, want 2", data["would_deliver"])
	}

	// Bad input is refused rather than previewed as info.
	if got, _ := call(map[string]interface{}{"title": "x", "severity": "catastrophic"}); got != http.StatusBadRequest {
		t.Fatalf("invalid severity = %d, want 400", got)
	}
	if got, _ := call(map[string]interface{}{"message": "no title"}); got != http.StatusBadRequest {
		t.Fatalf("missing title = %d, want 400", got)
	}

	// Nothing was persisted or delivered: the preview must not create a notification of its own.
	var notifications int64
	if err := s.db.Model(&db.Notification{}).Count(&notifications).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications != 0 {
		t.Fatalf("a preview persisted %d notification(s)", notifications)
	}
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "notification_preview").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	// Two successful previews (warning, critical). The rejected requests are NOT audited as previews: a
	// 400 is a malformed request, not a delivery answer, and counting them would inflate the trail.
	if audited != 2 {
		t.Fatalf("notification_preview rows = %d, want 2", audited)
	}
}
