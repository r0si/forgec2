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

// TestObservabilityWarningsAreAuditedOncePerEpisode covers the audit trail behind the readiness warnings.
//
// A probe is polled continuously, so an audit row per poll would bury the trail in its own alarm. What an investigator needs
// is when a route or rule STARTED looking broken and when it recovered - one row at each transition, silence in between.
func TestObservabilityWarningsAreAuditedOncePerEpisode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetRuleActionStats()
	resetObservabilityWarnings()
	t.Cleanup(func() {
		resetNotificationStats()
		resetRuleActionStats()
		resetObservabilityWarnings()
	})

	poll := func() {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleReadyCheck(c)
	}
	count := func(action string) int64 {
		var n int64
		if err := s.db.Model(&db.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", action, err)
		}
		return n
	}

	// 1. A healthy server writes nothing, however often it is polled.
	poll()
	poll()
	poll()
	if got := count("observability_warning"); got != 0 {
		t.Fatalf("a healthy server wrote %d warning row(s)", got)
	}

	// 2. The first failure is recorded once, and polling does not repeat it.
	recordNotificationDelivery(11, errors.New("post: 500"))
	recordRuleActionOutcome("rule-x", false, errors.New("run_command: no target agent"))
	poll()
	poll()
	poll()
	if got := count("observability_warning"); got != 2 {
		t.Fatalf("warning rows = %d, want one per warning kind", got)
	}

	// 3. A change in magnitude is a new episode worth a row: two failing routes instead of one.
	recordNotificationDelivery(12, errors.New("post: 502"))
	poll()
	if got := count("observability_warning"); got != 3 {
		t.Fatalf("a rising count did not produce a row: %d", got)
	}

	// 4. Recovery is recorded, and returning to zero stops further rows.
	resetNotificationStats()
	resetRuleActionStats()
	poll()
	poll()
	if got := count("observability_recovered"); got != 2 {
		t.Fatalf("recovery rows = %d, want one per warning kind", got)
	}
	if got := count("observability_warning"); got != 3 {
		t.Fatalf("a recovered system kept warning: %d rows", got)
	}

	// 5. A second episode is a second episode: the memory was reset by the recovery, not switched off for good.
	recordNotificationDelivery(11, errors.New("post: 500"))
	poll()
	if got := count("observability_warning"); got != 4 {
		t.Fatalf("a new episode after recovery was not recorded: %d rows", got)
	}
}
