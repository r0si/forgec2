package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Automation rule health: the checks an operator should not have to do by reading every rule.
//
// The previews answer "what happens for this event". This answers the static questions about the SET, which
// no per-event question can reach:
//
//   - a rule with NO conditions on a mutating action fires on EVERY event of its type - the most common way an
//     automation set surprises its author;
//   - an action targeting an agent id, script or macro that no longer exists will log a failure every time it
//     fires, forever, and nothing in the rule list says so;
//   - two rules with the same event and the same conditions run the same actions twice, which is invisible
//     until the duplicate side effect shows up.
//
// Every finding is derived from stored data, so this is a read-only report: no rule is modified, no action runs.

// automationFinding is one problem with one rule.
type automationFinding struct {
	RuleID   string `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Severity string `json:"severity"` // warning | error
	Code     string `json:"code"`     // stable identifier for tooling
	Detail   string `json:"detail"`
}

// handleAutomationHealth reports static problems in the caller's automation set.
func (s *Server) handleAutomationHealth(c *gin.Context) {
	tenantID := s.currentTenantID(c)
	rules := s.loadAutomationRules()

	// Agents, scripts and macros are looked up once, so a big rule set costs a bounded number of queries
	// rather than one per action.
	agentIDs := s.existingAgentIDs(c)
	scriptIDs := s.existingScriptIDs(c)
	macroIDs := s.existingMacroIDs(c)

	findings := make([]automationFinding, 0, 8)
	seen := make(map[string][]AutomationRule, len(rules))

	for _, rule := range rules {
		if rule.TenantID != 0 && rule.TenantID != tenantID {
			continue
		}
		add := func(severity, code, detail string) {
			findings = append(findings, automationFinding{
				RuleID: rule.ID, RuleName: rule.Name, Severity: severity, Code: code, Detail: detail,
			})
		}

		// 1. No conditions + an action that changes something = fires on everything.
		if len(rule.Conditions) == 0 {
			if mutating, ok := firstMutatingAction(rule.Actions); ok {
				add("error", "unconditional_mutating_action", fmt.Sprintf(
					"no conditions, but the rule runs a %q action: it fires on every %q event", mutating, rule.EventType))
			}
		}

		// 2. Action targets that no longer exist.
		for _, action := range rule.Actions {
			for _, target := range actionTargets(action) {
				switch target.kind {
				case "agent":
					if _, ok := agentIDs[target.id]; !ok && target.id != "" {
						add("error", "missing_agent", fmt.Sprintf("targets agent %q, which does not exist", target.id))
					}
				case "script":
					if _, ok := scriptIDs[target.id]; !ok && target.id != "" {
						add("error", "missing_script", fmt.Sprintf("targets script %q, which does not exist", target.id))
					}
				case "macro":
					if _, ok := macroIDs[target.id]; !ok && target.id != "" {
						add("error", "missing_macro", fmt.Sprintf("targets macro %q, which does not exist", target.id))
					}
				}
			}
		}

		// 3. Duplicate rule bodies, grouped by the identity that determines what happens.
		conditionsJSON, _ := json.Marshal(rule.Conditions)
		actionsJSON, _ := json.Marshal(rule.Actions)
		// NUL joins the parts: JSON is allowed to contain any printable separator, so a "|" here would let two
		// different rules collide on the same key.
		key := rule.EventType + "\x00" + string(conditionsJSON) + "\x00" + string(actionsJSON)
		seen[key] = append(seen[key], rule)
	}

	for _, group := range seen {
		if len(group) < 2 {
			continue
		}
		names := make([]string, 0, len(group))
		for _, rule := range group {
			names = append(names, rule.Name)
		}
		sort.Strings(names)
		for _, rule := range group {
			findings = append(findings, automationFinding{
				RuleID: rule.ID, RuleName: rule.Name, Severity: "warning", Code: "duplicate_rule",
				Detail: fmt.Sprintf("same event, conditions and actions as %s: the actions would run more than once",
					strings.Join(otherNames(names, rule.Name), ", ")),
			})
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity == "error"
		}
		if findings[i].RuleName != findings[j].RuleName {
			return findings[i].RuleName < findings[j].RuleName
		}
		return findings[i].Code < findings[j].Code
	})
	if findings == nil {
		findings = []automationFinding{}
	}

	counts := map[string]int{"rules": 0, "errors": 0, "warnings": 0}
	for _, rule := range rules {
		if rule.TenantID == 0 || rule.TenantID == tenantID {
			counts["rules"]++
		}
	}
	for _, finding := range findings {
		if finding.Severity == "error" {
			counts["errors"]++
		} else {
			counts["warnings"]++
		}
	}

	respond(c, gin.H{"summary": counts, "findings": findings})
}

// mutatingActionTypes are the actions whose firing changes state. "notify" is deliberately excluded: a rule
// with no conditions that only notifies is noisy, not dangerous.
var mutatingActionTypes = map[string]bool{
	ActionRunCommand: true,
	ActionRunScript:  true,
	ActionCreateTask: true,
	ActionSetSleep:   true,
	ActionRunMacro:   true,
	ActionWebhook:    true,
}

func firstMutatingAction(actions []RuleAction) (string, bool) {
	for _, action := range actions {
		if mutatingActionTypes[action.Type] {
			return action.Type, true
		}
	}
	return "", false
}

// actionTarget is one resource an action names.
type actionTarget struct {
	kind string // agent | script | macro
	id   string
}

func actionTargets(action RuleAction) []actionTarget {
	var params struct {
		AgentID  string `json:"agent_id"`
		ScriptID string `json:"script_id"`
		MacroID  string `json:"macro_id"`
	}
	if len(action.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(action.Params, &params); err != nil {
		return nil
	}
	targets := make([]actionTarget, 0, 3)
	if params.AgentID != "" {
		targets = append(targets, actionTarget{kind: "agent", id: params.AgentID})
	}
	if params.ScriptID != "" {
		targets = append(targets, actionTarget{kind: "script", id: params.ScriptID})
	}
	if params.MacroID != "" {
		targets = append(targets, actionTarget{kind: "macro", id: params.MacroID})
	}
	return targets
}

// healthTargetIDs collects the ids a rule action may legitimately reference.
//
// The query deliberately includes tenant 0 as well as the caller tenant: rows created before multi-tenancy, and
// macros/scripts that are global by design, are real targets. Reporting them missing would make the report cry
// wolf, which is the fastest way to get a health check ignored.
func (s *Server) healthTargetIDs(c *gin.Context, model interface{}, hasTenant bool) map[string]struct{} {
	out := make(map[string]struct{}, 128)
	query := s.db.WithContext(s.ctx).Model(model)
	if hasTenant {
		query = query.Where("tenant_id = ? OR tenant_id = 0", s.currentTenantID(c))
	}
	var ids []string
	if err := query.Pluck("id", &ids).Error; err == nil {
		for _, id := range ids {
			out[id] = struct{}{}
		}
	}
	return out
}

func (s *Server) existingAgentIDs(c *gin.Context) map[string]struct{} {
	return s.healthTargetIDs(c, &db.Implant{}, true)
}

// Scripts are scoped by creator rather than tenant, so they need their own query: a script is visible when
// its creator belongs to the caller tenant.
func (s *Server) existingScriptIDs(c *gin.Context) map[string]struct{} {
	out := make(map[string]struct{}, 64)
	query, ok := s.creatorScope(c, &db.Script{}, "created_by")
	if !ok {
		return out
	}
	var ids []string
	if err := query.Pluck("id", &ids).Error; err == nil {
		for _, id := range ids {
			out[id] = struct{}{}
		}
	}
	return out
}

func (s *Server) existingMacroIDs(c *gin.Context) map[string]struct{} {
	return s.healthTargetIDs(c, &db.CommandMacro{}, true)
}

// otherNames returns the group's names minus this rule, for the finding text.
func otherNames(names []string, self string) []string {
	out := make([]string, 0, len(names))
	skipped := false
	for _, name := range names {
		if name == self && !skipped {
			skipped = true
			continue
		}
		out = append(out, name)
	}
	return out
}
