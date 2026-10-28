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

// TestResetAutomationActionStats covers clearing a rule's action counters.
//
// A rule whose action failed for a week and was then fixed would keep an error-level action_failing finding forever, and an
// indicator that can only get worse is one operators learn to ignore. Clearing is audited for the same reason as the route
// counters: "the failures disappeared" is a change someone will want explained.
func TestResetAutomationActionStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetRuleActionStats()
	t.Cleanup(resetRuleActionStats)

	for _, id := range []string{"rule-a", "rule-b"} {
		rule := db.AutomationRule{ID: id, Name: id, Enabled: true, EventType: string(EventImplantCheckin), TenantID: 1}
		if err := s.db.Create(&rule).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	recordRuleActionOutcome("rule-a", false, errString("boom"))
	recordRuleActionOutcome("rule-b", true, nil)

	call := func(query string) (int, map[string]interface{}) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/action-stats/reset"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleResetAutomationActionStats(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. Clearing one rule leaves the other alone.
	code, data := call("?id=rule-a")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["cleared"] != float64(1) || data["scope"] != "rule" {
		t.Fatalf("response = %v", data)
	}
	snapshot := ruleActionStatsSnapshot()
	if _, present := snapshot["rule-a"]; present {
		t.Fatal("the rule's counters survived the reset")
	}
	if _, present := snapshot["rule-b"]; !present {
		t.Fatal("clearing one rule also cleared another")
	}

	// 2. An unknown rule is a 404: a typo must not look like a successful reset.
	if code, _ := call("?id=nope"); code != http.StatusNotFound {
		t.Fatalf("unknown rule = %d, want 404", code)
	}

	// 3. Clearing everything reports how many counters went away.
	recordRuleActionOutcome("rule-a", true, nil)
	code, data = call("")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["cleared"] != float64(2) || data["scope"] != "all" {
		t.Fatalf("reset-all = %v", data)
	}

	// 4. Both forms are audited.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "automation_action_stats_reset").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 2 {
		t.Fatalf("automation_action_stats_reset rows = %d, want 2", audited)
	}
}

// errString lets the test build a plain error without importing errors for one call.
type errString string

func (e errString) Error() string { return string(e) }
