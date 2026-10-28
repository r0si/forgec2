package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Automation rule preview: "would this rule fire for this event, and if not, why not".
//
// The most common automation complaint is a rule that silently does nothing: a condition references a
// field the event does not carry, the cooldown is still open, the rule is disabled, or the event type does
// not match. None of that is visible from the rule list, and the alternative - waiting for a real beacon to
// find out - costs an engagement. This endpoint evaluates ONE rule against a synthetic event and reports the
// verdict with the per-condition detail, executing nothing.
//
// POST /api/automation/rules/:id/preview
//   {"event_type":"agent.checkin","agent_id":"a-1","agent_host":"web-01","data":{"user":"alice"}}

// automationPreviewEvent is the synthetic event body.
type automationPreviewEvent struct {
	EventType string                 `json:"event_type"`
	AgentID   string                 `json:"agent_id"`
	AgentHost string                 `json:"agent_host"`
	AgentIP   string                 `json:"agent_ip"`
	AgentOS   string                 `json:"agent_os"`
	User      string                 `json:"user"`
	Data      map[string]interface{} `json:"data"`
}

// handlePreviewAutomationRule evaluates one rule against a synthetic event without running it.
func (s *Server) handlePreviewAutomationRule(c *gin.Context) {
	ruleID := strings.TrimSpace(c.Param("id"))
	if ruleID == "" {
		respondError(c, http.StatusBadRequest, "rule id is required")
		return
	}

	var stored db.AutomationRule
	// Tenant-scoped: another tenant's rule is a 404, not a preview that discloses its conditions.
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.AutomationRule{}), c).
		Where("id = ?", ruleID).First(&stored).Error; err != nil {
		respondError(c, http.StatusNotFound, "automation rule not found")
		return
	}

	var body automationPreviewEvent
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"event_type":"...","agent_id":"...","agent_host":"...","data":{...}}`)
		return
	}
	rule := storedRuleToAutomationRule(stored)

	// The fields exist because action templates use exactly these variables ({{hostname}}, {{user}}, {{ip}},
	// {{os}}, {{agent_id}}, {{event_type}}): a preview that could not resolve one of them would show a
	// template that looks broken when the real event would expand it.
	event := Event{
		Type:      EventType(strings.TrimSpace(body.EventType)),
		AgentID:   strings.TrimSpace(body.AgentID),
		AgentHost: strings.TrimSpace(body.AgentHost),
		AgentIP:   strings.TrimSpace(body.AgentIP),
		AgentOS:   strings.TrimSpace(body.AgentOS),
		User:      strings.TrimSpace(body.User),
		Data:      body.Data,
	}
	event.Data = flattenPreviewEventData(body.Data)

	verdict := s.previewRuleVerdict(rule, event)
	s.LogAuditRecord(c, "automation_preview", "automation", rule.ID,
		fmt.Sprintf("previewed rule %q for event %q: would_run=%v", rule.Name, event.Type, verdict["would_run"]), true, nil)
	respond(c, verdict)
}

// previewRuleVerdict evaluates one rule against an event and returns the explanation. It is the single
// implementation shared by the single-rule and batch previews, so the two can never disagree about whether a
// rule would fire.
func (s *Server) previewRuleVerdict(rule AutomationRule, event Event) gin.H {
	// Reasons are collected in the order they are checked, so the first entry is the one that decided.
	var reasons []string
	matched := true

	if !rule.Enabled {
		matched = false
		reasons = append(reasons, "rule is disabled")
	}
	if event.Type != "" && rule.EventType != "" && string(event.Type) != rule.EventType {
		matched = false
		reasons = append(reasons, fmt.Sprintf("event type is %q, the rule listens for %q", string(event.Type), rule.EventType))
	}
	if event.Type == "" {
		reasons = append(reasons, "no event_type supplied; conditions were evaluated against the supplied data only")
	}

	type conditionResult struct {
		Field    string `json:"field"`
		Operator string `json:"operator"`
		Value    string `json:"value"`
		Actual   string `json:"actual"`
		Matched  bool   `json:"matched"`
	}
	results := make([]conditionResult, 0, len(rule.Conditions))
	for _, cond := range rule.Conditions {
		actual := previewConditionValue(cond.Field, event)
		ok := s.matchCondition(cond, event)
		if !ok {
			matched = false
			reasons = append(reasons, fmt.Sprintf("%s %s %q did not match %q", cond.Field, cond.Operator, cond.Value, actual))
		}
		results = append(results, conditionResult{
			Field: cond.Field, Operator: cond.Operator, Value: cond.Value, Actual: actual, Matched: ok,
		})
	}

	// Cooldown is reported separately: the conditions can match while the rule still will not run.
	cooldownOpen := false
	cooldownRemaining := 0
	if rule.Cooldown > 0 && !rule.LastTriggered.IsZero() {
		elapsed := time.Since(rule.LastTriggered)
		if elapsed < time.Duration(rule.Cooldown)*time.Second {
			cooldownOpen = true
			cooldownRemaining = int((time.Duration(rule.Cooldown)*time.Second - elapsed).Seconds())
			reasons = append(reasons, fmt.Sprintf("cooldown still open for %ds", cooldownRemaining))
		}
	}

	type actionPreview struct {
		Type     string `json:"type"`
		WouldRun bool   `json:"would_run"`
		Template string `json:"template,omitempty"`
		Note     string `json:"note,omitempty"`
	}
	actions := make([]actionPreview, 0, len(rule.Actions))
	for _, action := range rule.Actions {
		preview := actionPreview{Type: action.Type, WouldRun: matched && !cooldownOpen}
		// Show what the parameters expand to: a template pointing at a field the event lacks is the second
		// most common silent failure, and it is invisible without expansion.
		if raw := previewActionTemplate(action); raw != "" {
			preview.Template = s.expandTemplate(raw, event)
		}
		actions = append(actions, preview)
	}

	wouldRun := matched && !cooldownOpen
	return gin.H{
		"rule_id":            rule.ID,
		"rule_name":          rule.Name,
		"would_run":          wouldRun,
		"matched":            matched,
		"cooldown_open":      cooldownOpen,
		"cooldown_remaining": cooldownRemaining,
		"reasons":            reasonsOrEmpty(reasons),
		"conditions":         results,
		"actions":            actions,
	}
}

// previewConditionValue resolves the value a condition compares against, for display.
func previewConditionValue(field string, event Event) string {
	if field == "agent.hostname" {
		return event.AgentHost
	}
	if v, ok := event.Data[field]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// previewActionTemplate returns the templated part of an action, if it has one, so the preview can show
// what it would expand to.
func previewActionTemplate(action RuleAction) string {
	var params struct {
		Command string `json:"command"`
		URL     string `json:"url"`
		Message string `json:"message"`
	}
	if len(action.Params) == 0 {
		return ""
	}
	if err := json.Unmarshal(action.Params, &params); err != nil {
		return ""
	}
	switch {
	case params.Command != "":
		return params.Command
	case params.URL != "":
		return params.URL
	default:
		return params.Message
	}
}

// reasonsOrEmpty keeps the JSON shape stable.
func reasonsOrEmpty(reasons []string) []string {
	if reasons == nil {
		return []string{}
	}
	return reasons
}

// storedRuleToAutomationRule converts the persisted row into the form the evaluator uses. It mirrors the
// loader so a preview and a real evaluation see the same rule.
func storedRuleToAutomationRule(stored db.AutomationRule) AutomationRule {
	rule := AutomationRule{
		ID:            stored.ID,
		Name:          stored.Name,
		Enabled:       stored.Enabled,
		EventType:     stored.EventType,
		Cooldown:      stored.CooldownSeconds,
		LastTriggered: stored.LastTriggered,
		RunCount:      stored.RunCount,
		Schedule:      stored.Schedule,
		AgentID:       stored.AgentID,
		TaskType:      stored.TaskType,
		Command:       stored.Command,
		Params:        stored.Params,
		LastRun:       stored.LastRun,
		NextRun:       stored.NextRun,
		CreatedBy:     stored.CreatedBy,
		TenantID:      stored.TenantID,
	}
	if err := json.Unmarshal([]byte(stored.Conditions), &rule.Conditions); err != nil {
		rule.Conditions = nil
	}
	if err := json.Unmarshal([]byte(stored.Actions), &rule.Actions); err != nil {
		rule.Actions = nil
	}
	return rule
}

// flattenPreviewEventData maps the body data object into the keys a rule condition actually uses.
//
// A condition addresses event data as "data.user", and matchCondition looks the field up verbatim in the
// event map. A preview body namespaces its values under "data" already ({"data":{"user":"alice"}} binds to
// Data["user"]), so each entry is prefixed once; a key that already contains a dot is passed through
// untouched, which is how a condition on another namespace (agent.os) can be previewed too.
//
// Getting this wrong produced a false negative in the one tool built to explain false negatives: the preview
// said a matching rule would not fire.
func flattenPreviewEventData(raw map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(raw)+4)
	for key, value := range raw {
		if strings.Contains(key, ".") {
			out[key] = value
			continue
		}
		out["data."+key] = value
	}
	return out
}
