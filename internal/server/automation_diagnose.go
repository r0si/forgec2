package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// One call that answers "if this event arrived now, what would fire and who would hear about it".
//
// The pieces already exist as separate previews (which automation rules would run, whether a notification would
// reach anyone), and an operator debugging an automation set was expected to compose them by hand. This endpoint
// does the composition: the same event, the same rules, the same routes, one answer - and it runs nothing.
//
// POST /api/automation/diagnose
//   {"event_type":"implant.checkin","agent_host":"web-01","data":{"user":"alice"},
//    "notification":{"title":"beacon checkin","severity":"info"}}

// automationDiagnoseRequest is the event plus an optional notification to evaluate alongside it.
type automationDiagnoseRequest struct {
	automationPreviewEvent
	Notification *struct {
		Title    string `json:"title"`
		Message  string `json:"message"`
		Severity string `json:"severity"`
	} `json:"notification"`
}

// handleAutomationDiagnose composes the automation and notification previews for one event.
func (s *Server) handleAutomationDiagnose(c *gin.Context) {
	var body automationDiagnoseRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"event_type":"...","agent_host":"...","data":{...}}`)
		return
	}
	if strings.TrimSpace(body.EventType) == "" {
		respondError(c, http.StatusBadRequest, "event_type is required; without it every rule would be judged against an empty event")
		return
	}

	event := previewEventFromBody(body.automationPreviewEvent)
	tenantID := s.currentTenantID(c)
	automation := s.diagnoseAutomation(event, tenantID)

	severity := "info"
	title := ""
	if body.Notification != nil {
		if trimmed := strings.ToLower(strings.TrimSpace(body.Notification.Severity)); trimmed != "" {
			severity = trimmed
		}
		title = strings.TrimSpace(body.Notification.Title)
	}
	switch severity {
	case "info", "warning", "critical":
	default:
		respondError(c, http.StatusBadRequest, "invalid notification severity (expected info, warning or critical)")
		return
	}
	notifications, err := s.diagnoseNotificationRoutes(severity)
	if err != nil {
		handleQueryError(c, err, "Failed to evaluate notification routes")
		return
	}

	s.LogAuditRecord(c, "automation_diagnose", "automation", "",
		"diagnosed "+string(event.Type)+": "+strconv.FormatUint(uint64(countsOf(automation, "would_run")), 10)+" rule(s) fire, "+
			strconv.FormatUint(uint64(countsOf(notifications, "would_deliver")), 10)+" route(s) deliver", true, nil)

	resp := gin.H{
		"event_type":    string(event.Type),
		"automation":    automation,
		"notifications": notifications,
	}
	if title != "" {
		resp["notification_title"] = title
	}
	respond(c, resp)
}

// diagnoseAutomation returns the batch verdict for the event. It is the same computation the batch preview
// responds with, factored out so the two cannot disagree.
func (s *Server) diagnoseAutomation(event Event, tenantID uint) gin.H {
	type ruleVerdict struct {
		RuleID       string `json:"rule_id"`
		RuleName     string `json:"rule_name"`
		Enabled      bool   `json:"enabled"`
		WouldRun     bool   `json:"would_run"`
		Matched      bool   `json:"matched"`
		CooldownOpen bool   `json:"cooldown_open"`
		FirstReason  string `json:"first_reason,omitempty"`
	}

	var verdicts []ruleVerdict
	// Suggestions are what the verdict reveals about the CONFIGURATION rather than about this event: a template
	// variable the event cannot fill, or an event that no rule listens to at all.
	var suggestions []diagnoseSuggestion
	counts := map[string]int{"total": 0, "would_run": 0, "blocked_by_conditions": 0, "blocked_by_cooldown": 0, "disabled": 0}
	for _, rule := range s.loadAutomationRules() {
		if rule.TenantID != 0 && rule.TenantID != tenantID {
			continue
		}
		if rule.EventType != "" && rule.EventType != string(event.Type) {
			continue
		}
		verdict := s.previewRuleVerdict(rule, event)
		entry := ruleVerdict{RuleID: rule.ID, RuleName: rule.Name, Enabled: rule.Enabled}
		entry.WouldRun, _ = verdict["would_run"].(bool)
		entry.Matched, _ = verdict["matched"].(bool)
		entry.CooldownOpen, _ = verdict["cooldown_open"].(bool)
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
		suggestions = append(suggestions, templateSuggestions(rule, event)...)
	}

	if counts["total"] == 0 {
		suggestions = append(suggestions, diagnoseSuggestion{
			Severity: "info",
			Code:     "no_rule_listens",
			Detail:   "no rule of this tenant listens to " + string(event.Type) + "; this event triggers nothing",
		})
	}

	sort.SliceStable(verdicts, func(i, j int) bool {
		if verdicts[i].WouldRun != verdicts[j].WouldRun {
			return verdicts[i].WouldRun
		}
		return verdicts[i].RuleName < verdicts[j].RuleName
	})
	if verdicts == nil {
		verdicts = []ruleVerdict{}
	}
	// A nil slice marshals as null, and a client reading `suggestions.length` should not have to special-case
	// it - the same rule the export list and the notification section follow.
	if suggestions == nil {
		suggestions = []diagnoseSuggestion{}
	}
	return gin.H{"summary": counts, "rules": verdicts, "suggestions": suggestions}
}

// diagnoseNotificationRoutes returns the delivery verdict for a severity, using the same rule set the sender
// applies.
func (s *Server) diagnoseNotificationRoutes(severity string) (gin.H, error) {
	var routes []db.NotificationRoute
	if err := s.db.WithContext(s.ctx).Order("name asc").Limit(notificationPreviewMaxRoutes).Find(&routes).Error; err != nil {
		return nil, err
	}
	type routeVerdict struct {
		Name         string `json:"name"`
		Channel      string `json:"channel"`
		WouldDeliver bool   `json:"would_deliver"`
		Reason       string `json:"reason"`
	}
	rank := severityRank(severity)
	verdicts := make([]routeVerdict, 0, len(routes))
	deliveries := 0
	for _, route := range routes {
		verdict := routeVerdict{Name: route.Name, Channel: route.Channel}
		switch {
		case !route.Enabled:
			verdict.Reason = "route is disabled"
		case rank < severityRank(route.MinSeverity):
			verdict.Reason = "severity below the route's minimum"
		default:
			if err := validateNotificationTarget(route); err != nil {
				verdict.Reason = err.Error()
			} else {
				verdict.WouldDeliver = true
				verdict.Reason = "would deliver"
				deliveries++
			}
		}
		verdicts = append(verdicts, verdict)
	}
	if verdicts == nil {
		verdicts = []routeVerdict{}
	}
	suggestions := []diagnoseSuggestion{}
	switch {
	case len(verdicts) == 0:
		suggestions = append(suggestions, diagnoseSuggestion{
			Severity: "warning", Code: "no_notification_route",
			Detail: "no notification routes are configured: this notification would reach nobody",
		})
	case deliveries == 0:
		suggestions = append(suggestions, diagnoseSuggestion{
			Severity: "warning", Code: "no_route_would_deliver",
			Detail: "every configured route would reject this notification (disabled, below its minimum severity, or missing credentials)",
		})
	}
	return gin.H{
		"severity":      severity,
		"would_deliver": deliveries,
		"total_routes":  len(verdicts),
		"routes":        verdicts,
		"suggestions":   suggestions,
	}, nil
}

// countsOf pulls one counter out of a diagnose section for the audit line.
func countsOf(section gin.H, key string) uint {
	summary, ok := section["summary"].(map[string]int)
	if ok {
		return uint(summary[key])
	}
	if value, ok := section[key].(int); ok {
		return uint(value)
	}
	return 0
}

// diagnoseSuggestion is advice about the configuration, derived from what the verdict revealed.
type diagnoseSuggestion struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
}

// templateSuggestions reports action templates whose variables this event cannot fill.
//
// expandTemplate replaces {{hostname}}, {{user}}, {{agent_id}}, {{ip}}, {{os}} and {{event_type}} by plain
// substitution, so a variable the event does not carry silently becomes an empty string: a notification that
// says "host  checked in" and a webhook that posts an empty agent id. Nothing errors, which is why this is worth
// reporting before the rule ever fires.
func templateSuggestions(rule AutomationRule, event Event) []diagnoseSuggestion {
	available := map[string]bool{
		"{{hostname}}":   event.AgentHost != "",
		"{{user}}":       event.User != "",
		"{{agent_id}}":   event.AgentID != "",
		"{{ip}}":         event.AgentIP != "",
		"{{os}}":         event.AgentOS != "",
		"{{event_type}}": event.Type != "",
	}
	var out []diagnoseSuggestion
	seen := make(map[string]bool, 4)
	for _, action := range rule.Actions {
		template := previewActionTemplate(action)
		if template == "" {
			continue
		}
		for variable, filled := range available {
			if filled || seen[variable] {
				continue
			}
			if !strings.Contains(template, variable) {
				continue
			}
			seen[variable] = true
			detail := "rule " + rule.Name + " uses " + variable +
				" but this event carries no value for it: it would render empty"
			// The distinction that traps operators: expandTemplate reads the event's own fields, NOT the data
			// map, so providing data.user does not fill {{user}}. Say so rather than leaving them to find out.
			if variable == "{{user}}" && event.Data["data.user"] != nil {
				detail += " ({{user}} comes from the event's user field, not from data.user)"
			}
			out = append(out, diagnoseSuggestion{
				Severity: "warning",
				Code:     "template_variable_unfilled",
				Detail:   detail,
			})
		}
	}
	return out
}
