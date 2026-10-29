package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestNotificationRoutesCSVExport covers ?format=csv on the route list.
//
// "Which routes are not delivering" is pasted into an incident note, and the counters are the part of the answer that is not in
// the configuration. The file states that its counters start with the process, because a reader who assumes a window would draw
// the wrong conclusion from a low number.
func TestNotificationRoutesCSVExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	t.Cleanup(resetNotificationStats)

	route := db.NotificationRoute{
		Name: "ops-discord", Channel: "discord", Target: "https://discord.com/api/webhooks/1/abc", Enabled: true,
	}
	if err := s.db.Create(&route).Error; err != nil {
		t.Fatalf("seed route: %v", err)
	}
	recordNotificationDelivery(route.ID, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/notification-routes?format=csv", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleListNotificationRoutes(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}

	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want a header and one route", len(rows))
	}
	if got := strings.Join(rows[0], ","); got != "Route ID,Name,Channel,Enabled,Delivered,Failed,State,Last Attempt,Last Error" {
		t.Fatalf("header = %q", got)
	}
	if rows[1][1] != "ops-discord" || rows[1][2] != "discord" || rows[1][3] != "true" || rows[1][4] != "1" {
		t.Fatalf("route row = %v", rows[1])
	}
	// The credential-bearing webhook path must not be in the file: the target is the part of a route that is a secret.
	if strings.Contains(w.Body.String(), "/api/webhooks/1/abc") {
		t.Fatalf("the export leaked the route target: %s", w.Body.String())
	}

	// The counters start with the process, and the file says so rather than implying a window it does not have.
	if scope := w.Header().Get(exportScopeHeader); !strings.Contains(scope, "process start") {
		t.Fatalf("scope header = %q, want the process-start caveat", scope)
	}
}
