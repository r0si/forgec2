package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Test-firing a rule: run its actions NOW, against a synthetic event.
//
// The previews answer "would this fire". The notification route test answers "does this channel deliver". Nothing answered the
// question in between: **do this rule's actions actually work** - and the operator otherwise finds out when the event they
// cared about finally arrives.
//
// It EXECUTES, so it is deliberate in three ways: the body must say `confirm: true`, it is rate-limited per rule, and it is
// audited on every call. What it cannot promise is delivery: a webhook action is sent in a goroutine, so its result arrives
// after the response and is counted by the route's own counters - the note in the answer says so rather than implying
// otherwise.
//
// POST /api/automation/rules/:id/test   {"confirm": true}

// automationTestCooldownWindow bounds how often one rule may be test-fired.
const automationTestCooldownWindow = 10 * time.Second

var (
	automationTestMu sync.Mutex
	automationTestAt = map[string]time.Time{}
)

// reserveAutomationTest reports whether a test may run now, and how long to wait when it may not.
func reserveAutomationTest(ruleID string) (time.Duration, bool) {
	automationTestMu.Lock()
	defer automationTestMu.Unlock()
	now := time.Now()
	if last, ok := automationTestAt[ruleID]; ok {
		if elapsed := now.Sub(last); elapsed < automationTestCooldownWindow {
			return automationTestCooldownWindow - elapsed, false
		}
	}
	automationTestAt[ruleID] = now
	return 0, true
}

// resetAutomationTests clears the cooldown map. Tests use it.
func resetAutomationTests() {
	automationTestMu.Lock()
	defer automationTestMu.Unlock()
	automationTestAt = map[string]time.Time{}
}

// handleTestAutomationRule executes one rule's actions against a synthetic event.
func (s *Server) handleTestAutomationRule(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !body.Confirm {
		// The confirmation is not ceremony: this runs the actions, and several of them dispatch tasks to agents.
		respondError(c, http.StatusBadRequest,
			`this executes the rule's actions; send {"confirm": true} to proceed`)
		return
	}

	var rule AutomationRule
	found := false
	tenantID := s.currentTenantID(c)
	for _, candidate := range s.loadAutomationRules() {
		if candidate.ID != id {
			continue
		}
		if candidate.TenantID != 0 && candidate.TenantID != tenantID {
			break
		}
		rule, found = candidate, true
		break
	}
	if !found {
		respondError(c, http.StatusNotFound, "rule not found")
		return
	}

	if wait, ok := reserveAutomationTest(rule.ID); !ok {
		c.Header("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		respondError(c, http.StatusTooManyRequests,
			fmt.Sprintf("this rule was tested moments ago; try again in %d seconds", int(wait.Seconds())+1))
		return
	}

	// The synthetic event is as close to a real one as the rule can describe: its event type, and the agent it targets (if
	// it names one) so a template or a command can expand to something recognisable.
	evt := Event{Type: EventType(rule.EventType), Data: map[string]interface{}{"source": "rule_test"}}
	if rule.AgentID != "" {
		var agent db.Implant
		if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
			Select("id, hostname, ip, os, username").
			Where("id = ?", rule.AgentID).First(&agent).Error; err == nil {
			evt.AgentID = agent.ID
			evt.AgentHost = agent.Hostname
			evt.AgentIP = agent.IP
			evt.AgentOS = agent.OS
			evt.User = agent.Username
		}
	}

	executed, failed := 0, 0
	errs := make([]string, 0, len(rule.Actions))
	for _, action := range rule.Actions {
		executed++
		err := s.executeAction(rule, action, evt)
		// Counted like a real firing: a test that fails is exactly the evidence the health report needs, and a test that
		// succeeds after a fix is what clears the badge.
		recordRuleActionOutcome(rule.ID, err == nil, err)
		if err != nil {
			failed++
			errs = append(errs, fmt.Sprintf("%s: %s", action.Type, err.Error()))
		}
	}

	s.LogAuditRecord(c, "automation_rule_test", "automation", rule.ID,
		fmt.Sprintf("test-fired rule %q: %d action(s), %d failed", rule.Name, executed, failed), failed == 0, nil)

	respond(c, gin.H{
		"rule_id":  rule.ID,
		"executed": executed,
		"failed":   failed,
		"errors":   errs,
		"event": gin.H{
			"event_type": rule.EventType,
			"agent_id":   evt.AgentID,
			"hostname":   evt.AgentHost,
		},
		"note": "webhook actions are sent asynchronously: their delivery result arrives after this response and is counted " +
			"on the route (GET /api/notification-routes)",
	})
}
