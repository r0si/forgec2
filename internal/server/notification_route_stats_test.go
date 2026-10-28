package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestNotificationRouteStats covers the delivery counters.
//
// A route that has been failing for a week looks exactly like one that is fine: the fan-out logs each failure and moves on.
// These counters answer "is this route still working", which the log cannot without being read line by line.
func TestNotificationRouteStats(t *testing.T) {
	resetNotificationStats()
	t.Cleanup(resetNotificationStats)

	// 1. The classifier, which is what turns two counters into an operational state.
	cases := []struct {
		name string
		stat notificationRouteStat
		want string
	}{
		{"never used", notificationRouteStat{}, "unused"},
		{"only successes", notificationRouteStat{Delivered: 5}, "delivering"},
		{"only failures", notificationRouteStat{Failed: 3}, "failing"},
		{"mostly failing", notificationRouteStat{Delivered: 1, Failed: 3}, "degraded"},
		{"mostly fine", notificationRouteStat{Delivered: 9, Failed: 1}, "delivering"},
	}
	for _, test := range cases {
		if got := notificationRouteHealth(test.stat); got != test.want {
			t.Fatalf("%s: state %q, want %q", test.name, got, test.want)
		}
	}

	// 2. Recording folds outcomes into the counters, and the last error is kept (trimmed) for the table.
	recordNotificationDelivery(7, nil)
	recordNotificationDelivery(7, nil)
	recordNotificationDelivery(7, errors.New("post: 500 internal server error"))
	snapshot := notificationRouteStatsSnapshot()
	if snapshot[7].Delivered != 2 || snapshot[7].Failed != 1 {
		t.Fatalf("counters = %+v", snapshot[7])
	}
	if snapshot[7].LastError == "" || snapshot[7].LastAttempt.IsZero() {
		t.Fatalf("the failure was counted but not explained: %+v", snapshot[7])
	}

	// 3. The view redacts a webhook URL: a delivery error quotes the target, and that target is a credential.
	recordNotificationDelivery(8, errors.New("Post \"https://discord.com/api/webhooks/123/secret-token\": dial tcp: timeout"))
	view := notificationRouteStatsView()
	lastError, _ := view["8"]["last_error"].(string)
	if lastError == "" {
		t.Fatalf("no error reported for route 8: %+v", view["8"])
	}
	if containsFold(lastError, "secret-token") {
		t.Fatalf("the webhook path leaked into the stats: %q", lastError)
	}

	// 3b. Two routes failing for the same reason share a signature, so a list can group them; the identifiers that differ
	// between them (a message id, a chat id) are collapsed the same way task failures are.
	recordNotificationDelivery(9, errors.New(`Post "https://discord.com/api/webhooks/111/aaa": 401 Unauthorized`))
	recordNotificationDelivery(10, errors.New(`Post "https://discord.com/api/webhooks/222/bbb": 401 Unauthorized`))
	view = notificationRouteStatsView()
	sigNine, _ := view["9"]["last_error_signature"].(string)
	sigTen, _ := view["10"]["last_error_signature"].(string)
	if sigNine == "" || sigNine != sigTen {
		t.Fatalf("the same failure produced different signatures:\n  %q\n  %q", sigNine, sigTen)
	}
	// And the signature does not leak the webhook id or path.
	for _, leak := range []string{"111", "222", "aaa", "bbb"} {
		if containsFold(sigNine, leak) {
			t.Fatalf("the signature still carries %q: %q", leak, sigNine)
		}
	}

	// 4. The list endpoint carries the counters, so the operator sees them where the routes are.
	s := mustTenantServer(t)
	route := db.NotificationRoute{Name: "live", Channel: "webhook", Target: "https://example.com/hook", MinSeverity: "info", Enabled: true}
	if err := s.db.Create(&route).Error; err != nil {
		t.Fatalf("seed route: %v", err)
	}
	recordNotificationDelivery(route.ID, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/notification-routes", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleListNotificationRoutes(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	body := w.Body.String()
	for _, needle := range []string{`"stats"`, `"delivered"`, `"state"`, "process start"} {
		if !containsFold(body, needle) {
			t.Fatalf("the list response does not carry the counters (%q missing): %s", needle, body)
		}
	}
}
