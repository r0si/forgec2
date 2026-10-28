package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Batch preview: "which rules would fire for this event".
//
// The single-rule preview answers "why did THIS rule not fire". The question an operator asks while designing
// an automation set is the other direction: given an event, which rules will run, which are blocked by a
// cooldown, and which are disabled - before the event ever happens. Without it, a set is validated one rule at
// a time and the interaction between rules (two rules on the same event, one cooling down) is invisible.
//
// It shares previewRuleVerdict with the single-rule endpoint, so the two cannot disagree, and it executes
// nothing.
//
// POST /api/automation/preview

// handlePreviewAutomationBatch evaluates every rule of the caller's tenant against one synthetic event.
func (s *Server) handlePreviewAutomationBatch(c *gin.Context) {
	var body automationPreviewEvent
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"event_type":"...","agent_host":"...","data":{...}}`)
		return
	}
	if strings.TrimSpace(body.EventType) == "" {
		respondError(c, http.StatusBadRequest, "event_type is required; without it every rule would be judged against an empty event")
		return
	}

	event := previewEventFromBody(body)
	tenantID := s.currentTenantID(c)

	// loadAutomationRules is the cached snapshot the live event path uses, so the preview judges exactly the
	// rules that would be judged for real - not a fresh read that could differ from them.
	type ruleVerdict struct {
		RuleID            string `json:"rule_id"`
		RuleName          string `json:"rule_name"`
		Enabled           bool   `json:"enabled"`
		WouldRun          bool   `json:"would_run"`
		Matched           bool   `json:"matched"`
		CooldownOpen      bool   `json:"cooldown_open"`
		CooldownRemaining int    `json:"cooldown_remaining"`
		FirstReason       string `json:"first_reason,omitempty"`
		EventType         string `json:"event_type"`
	}

	var verdicts []ruleVerdict
	counts := map[string]int{"total": 0, "would_run": 0, "blocked_by_conditions": 0, "blocked_by_cooldown": 0, "disabled": 0}
	for _, rule := range s.loadAutomationRules() {
		// Same containment the evaluator applies: a rule belongs to a tenant, and 0 is the legacy global set.
		if rule.TenantID != 0 && rule.TenantID != tenantID {
			continue
		}
		// A rule for a different event can never run for this one; listing it as "not firing" would drown
		// the answer in noise, so only rules that listen to this event are reported.
		if rule.EventType != "" && rule.EventType != string(event.Type) {
			continue
		}
		verdict := s.previewRuleVerdict(rule, event)
		entry := ruleVerdict{
			RuleID:    rule.ID,
			RuleName:  rule.Name,
			Enabled:   rule.Enabled,
			EventType: rule.EventType,
		}
		entry.WouldRun, _ = verdict["would_run"].(bool)
		entry.Matched, _ = verdict["matched"].(bool)
		entry.CooldownOpen, _ = verdict["cooldown_open"].(bool)
		if remaining, ok := verdict["cooldown_remaining"].(int); ok {
			entry.CooldownRemaining = remaining
		}
		if reasons, ok := verdict["reasons"].([]string); ok && len(reasons) > 0 {
			entry.FirstReason = reasons[0]
		}

		counts["total"]++
		switch {
		case entry.WouldRun:
			counts["would_run"]++
		case !entry.Enabled:
			counts["disabled"]++
		case entry.CooldownOpen:
			counts["blocked_by_cooldown"]++
		default:
			counts["blocked_by_conditions"]++
		}
		verdicts = append(verdicts, entry)
	}

	// Firing rules first, then by name: the answer to "what happens" should open with what happens.
	sort.SliceStable(verdicts, func(i, j int) bool {
		if verdicts[i].WouldRun != verdicts[j].WouldRun {
			return verdicts[i].WouldRun
		}
		return verdicts[i].RuleName < verdicts[j].RuleName
	})
	if verdicts == nil {
		verdicts = []ruleVerdict{}
	}

	s.LogAuditRecord(c, "automation_preview_batch", "automation", "",
		"previewed "+string(event.Type)+": "+strconv.Itoa(counts["would_run"])+" rule(s) would fire", true, nil)

	respond(c, gin.H{
		"event_type": string(event.Type),
		"summary":    counts,
		"rules":      verdicts,
	})
}

// previewEventFromBody builds the event the preview evaluates against, sharing the key namespacing with the
// single-rule path.
func previewEventFromBody(body automationPreviewEvent) Event {
	return Event{
		Type:      EventType(strings.TrimSpace(body.EventType)),
		AgentID:   strings.TrimSpace(body.AgentID),
		AgentHost: strings.TrimSpace(body.AgentHost),
		AgentIP:   strings.TrimSpace(body.AgentIP),
		AgentOS:   strings.TrimSpace(body.AgentOS),
		User:      strings.TrimSpace(body.User),
		Data:      flattenPreviewEventData(body.Data),
	}
}
