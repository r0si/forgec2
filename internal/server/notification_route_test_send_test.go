package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestNotificationRouteTestSend covers the hardening of the test-delivery endpoint.
//
// The original answered "notification delivery failed" and nothing else, sent through disabled routes, had no cooldown and
// wrote no audit row - so the one button whose purpose is "tell me whether this route works" could not tell an operator
// whether the URL was wrong, the token was revoked or egress was blocked.
func TestNotificationRouteTestSend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// Reset the package-level cooldown between runs of this test in the same process.
	notificationTestMu.Lock()
	notificationTestAt = make(map[uint]time.Time)
	notificationTestMu.Unlock()

	// A receiver that answers 500: the delivery must be reported as failed AND say why.
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer receiver.Close()

	live := db.NotificationRoute{
		Name: "live", Channel: "webhook", Target: receiver.URL + "/hook/secret-token",
		MinSeverity: "info", Enabled: true,
	}
	disabled := db.NotificationRoute{
		Name: "off", Channel: "webhook", Target: "https://example.com/hook", MinSeverity: "info", Enabled: false,
	}
	for _, route := range []*db.NotificationRoute{&live, &disabled} {
		if err := s.db.Create(route).Error; err != nil {
			t.Fatalf("seed route: %v", err)
		}
	}
	if err := s.db.Model(&db.NotificationRoute{}).Where("id = ?", disabled.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable route: %v", err)
	}

	var lastResponseBody string
	call := func(id uint) (int, map[string]interface{}) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/notification-routes/x/test", nil)
		c.Params = gin.Params{{Key: "id", Value: itoaUint(id)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleTestNotificationRoute(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		lastResponseBody = w.Body.String()
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. A failing receiver is a 502 - a client that only reads the status must not call it success - and the body
	// carries the REASON, which is the one thing this button exists to tell an operator.
	code, data := call(live.ID)
	if code != http.StatusBadGateway {
		t.Fatalf("a failed delivery = %d, want 502", code)
	}
	message := ""
	if data != nil {
		message, _ = data["error"].(string)
	}
	if message == "" {
		// respondError puts the message at the top level, so read the raw body as a fallback.
		message = lastResponseBody
	}
	if !strings.Contains(message, "failed") {
		t.Fatalf("the failure does not say why: %q", message)
	}
	// The webhook path is a credential and must not appear anywhere in the response.
	if strings.Contains(message, "secret-token") {
		t.Fatalf("the credential in the webhook path was echoed: %q", message)
	}

	// 2. A second test inside the cooldown is refused with Retry-After.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/notification-routes/x/test", nil)
	c.Params = gin.Params{{Key: "id", Value: itoaUint(live.ID)}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTestNotificationRoute(c)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("a repeated test = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("the 429 does not say when to retry")
	}

	// 3. A disabled route is refused rather than tested: it is never used in production.
	code, _ = call(disabled.ID)
	if code != http.StatusConflict {
		t.Fatalf("a disabled route = %d, want 409", code)
	}

	// 4. Every attempt that reaches the sender is audited, with its outcome.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "notification_route_test").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("notification_route_test rows = %d, want 1 (the failed attempt)", audited)
	}
}
