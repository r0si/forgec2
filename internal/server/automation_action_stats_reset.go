package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Clearing a rule's action counters.
//
// Same reason as the notification-route counters: a rule whose action failed for a week and was then fixed would keep an
// error-level "action_failing" finding forever, and an indicator that can only get worse is one operators learn to
// ignore. Clearing is audited, because "the failures disappeared" is exactly the change someone will want explained.
//
// POST /api/automation/action-stats/reset         (every rule)
// POST /api/automation/action-stats/reset?id=xyz  (one rule)

// handleResetAutomationActionStats clears the in-memory action counters.
func (s *Server) handleResetAutomationActionStats(c *gin.Context) {
	rawID := c.Query("id")
	if rawID == "" {
		before := ruleActionStatsSnapshot()
		resetRuleActionStats()
		s.LogAuditRecord(c, "automation_action_stats_reset", "automation", "",
			fmt.Sprintf("cleared action counters for %d rule(s)", len(before)), true, nil)
		respond(c, gin.H{"cleared": len(before), "scope": "all"})
		return
	}

	// The rule must exist, for the same reason as the route counter: a typo must not look like a successful reset.
	var rule db.AutomationRule
	if err := s.db.WithContext(s.ctx).First(&rule, "id = ?", rawID).Error; err != nil {
		respondError(c, http.StatusNotFound, "rule not found")
		return
	}
	if _, err := strconv.Atoi(rawID); err == nil && len(rawID) < 3 {
		// A numeric id is a notification-style mistake here; rules are identified by their string id.
		respondError(c, http.StatusBadRequest, "rule ids are strings, not numbers")
		return
	}

	ruleActionStatsMu.Lock()
	_, had := ruleActionStats[rawID]
	delete(ruleActionStats, rawID)
	ruleActionStatsMu.Unlock()

	s.LogAuditRecord(c, "automation_action_stats_reset", "automation", rawID,
		"cleared action counters for rule "+rule.Name, true, nil)
	respond(c, gin.H{"cleared": boolToInt(had), "scope": "rule", "rule_id": rawID})
}
