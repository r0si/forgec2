package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestResetNotificationRouteStats covers clearing the delivery counters.
//
// An indicator that can only get worse is one operators learn to ignore: a route that failed for a week and was then fixed
// keeps a red "failing" badge forever otherwise. Clearing is audited, because "the failures disappeared" is exactly the
// change someone will want explained later.
func TestResetNotificationRouteStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	t.Cleanup(resetNotificationStats)

	route := db.NotificationRoute{Name: "live", Channel: "webhook", Target: "https://example.com/hook", MinSeverity: "info", Enabled: true}
	if err := s.db.Create(&route).Error; err != nil {
		t.Fatalf("seed route: %v", err)
	}
	recordNotificationDelivery(route.ID, nil)
	recordNotificationDelivery(route.ID, nil)
	recordNotificationDelivery(21, nil)

	call := func(query string) (int, map[string]interface{}) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/notification-routes/stats/reset"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleResetNotificationRouteStats(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. Clearing one route leaves the others alone.
	code, data := call("?id=" + itoaUint(route.ID))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["cleared"] != float64(1) || data["scope"] != "route" {
		t.Fatalf("reset response = %v", data)
	}
	snapshot := notificationRouteStatsSnapshot()
	if _, present := snapshot[route.ID]; present {
		t.Fatal("the route counters survived the reset")
	}
	if _, present := snapshot[21]; !present {
		t.Fatal("clearing one route also cleared another")
	}

	// 2. An unknown route is a 404, not a silent no-op that makes the operator believe the badge was reset.
	if code, _ := call("?id=9999"); code != http.StatusNotFound {
		t.Fatalf("unknown route = %d, want 404", code)
	}
	// 3. A malformed id is a 400.
	if code, _ := call("?id=abc"); code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want 400", code)
	}

	// 4. Clearing everything reports how many counters it removed.
	recordNotificationDelivery(route.ID, nil)
	code, data = call("")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["cleared"] != float64(2) || data["scope"] != "all" {
		t.Fatalf("reset-all response = %v", data)
	}
	if len(notificationRouteStatsSnapshot()) != 0 {
		t.Fatal("reset-all left counters behind")
	}

	// 5. Both forms are audited.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "notification_stats_reset").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 2 {
		t.Fatalf("notification_stats_reset rows = %d, want 2 (one route, one all)", audited)
	}
}
