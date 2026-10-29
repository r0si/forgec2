package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Moving automation rules between deployments.
//
// A rule set is configuration an operator builds up over an exercise and then wants again next time - on another deployment,
// after a rebuild, or shared with a colleague. Without an export they retype conditions and actions, and a mistyped operator
// in a `contains` condition fails silently (the rule simply never matches).
//
// The export is deliberately PORTABLE: no database ids, no timestamps, no ownership. What travels is the rule's meaning -
// name, event type, conditions, actions, cooldown - and what does not travel is anything the destination would have to
// pretend to know.
//
// GET  /api/automation/rules/export      -> {"version":1,"count":N,"rules":[...]}
// GET  /api/automation/rules/:id/export  -> {"rule": {...}}
// POST /api/automation/rules/import      <- {"rule": {...}} or {"rules": [...], "overwrite": false}

// portableRule is the export shape, and the import shape.
type portableRule struct {
	Name            string          `json:"name"`
	EventType       string          `json:"event_type"`
	Enabled         *bool           `json:"enabled,omitempty"`
	Conditions      []RuleCondition `json:"conditions"`
	Actions         []RuleAction    `json:"actions"`
	CooldownSeconds int             `json:"cooldown_seconds,omitempty"`
	Schedule        string          `json:"schedule,omitempty"`
}

// portableRuleSetVersion identifies the document shape, so a future change can be recognised rather than guessed at.
const portableRuleSetVersion = 1

// toPortable converts a live rule to the document shape.
func toPortable(rule AutomationRule) portableRule {
	enabled := rule.Enabled
	return portableRule{
		Name:            rule.Name,
		EventType:       rule.EventType,
		Enabled:         &enabled,
		Conditions:      rule.Conditions,
		Actions:         rule.Actions,
		CooldownSeconds: rule.Cooldown,
		Schedule:        rule.Schedule,
	}
}

// handleExportAutomationRules returns the caller's whole rule set as one document.
func (s *Server) handleExportAutomationRules(c *gin.Context) {
	rules := s.loadAutomationRules()
	tenantID := s.currentTenantID(c)
	out := make([]portableRule, 0, len(rules))
	for _, rule := range rules {
		if rule.TenantID != 0 && rule.TenantID != tenantID {
			continue
		}
		out = append(out, toPortable(rule))
	}
	respond(c, gin.H{"version": portableRuleSetVersion, "count": len(out), "rules": out})
}

// handleExportAutomationRule returns one rule in the portable shape.
func (s *Server) handleExportAutomationRule(c *gin.Context) {
	rules := s.loadAutomationRules()
	tenantID := s.currentTenantID(c)
	id := strings.TrimSpace(c.Param("id"))

	for _, rule := range rules {
		if rule.ID != id {
			continue
		}
		if rule.TenantID != 0 && rule.TenantID != tenantID {
			// Another tenant's rule does not exist as far as this caller is concerned.
			respondError(c, http.StatusNotFound, "rule not found")
			return
		}
		respond(c, gin.H{"rule": toPortable(rule)})
		return
	}
	respondError(c, http.StatusNotFound, "rule not found")
}

// handleImportAutomationRule creates rules from a portable document: one, or a whole set.
func (s *Server) handleImportAutomationRule(c *gin.Context) {
	var body struct {
		Rule      *portableRule  `json:"rule"`
		Rules     []portableRule `json:"rules"`
		Overwrite bool           `json:"overwrite"`
		// DryRun answers "what would this do" without writing: the same verdicts, the same warnings, no rows. It is the
		// difference between trying an import on a production console and trusting a document you have not read.
		DryRun bool `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest,
			`invalid body: expected {"rule": {...}} or {"rules": [...], "overwrite": false}`)
		return
	}

	// The set form: a partial success is the normal case, so the answer is per rule rather than a single status. A 409 for
	// the whole document would hide the rules that did import.
	if len(body.Rules) > 0 {
		s.importRuleSet(c, body.Rules, body.Overwrite, body.DryRun)
		return
	}

	if body.Rule == nil {
		respondError(c, http.StatusBadRequest, "provide rule or rules")
		return
	}
	outcome := s.importOneRule(c, *body.Rule, body.Overwrite, body.DryRun, s.existingImportDeps(c))
	switch outcome.Status {
	case "invalid":
		respondError(c, http.StatusBadRequest, outcome.Reason)
		return
	case "conflict":
		respondError(c, http.StatusConflict, outcome.Reason)
		return
	}
	respond(c, gin.H{
		"rule_id": outcome.RuleID, "overwrote": outcome.Status == "replaced", "enabled": outcome.Enabled,
		"warnings": outcome.Warnings, "dry_run": body.DryRun,
	})
}

// ruleImportOutcome is what happened to one document.
type ruleImportOutcome struct {
	Name    string
	Status  string // created | replaced | conflict | invalid
	RuleID  string
	Enabled bool
	Reason  string
	// Warnings are references in the imported document that will not resolve HERE. The import still succeeds - a rule
	// may be imported before the agent it targets exists - but the operator is standing in front of the response, which is
	// the only moment the mismatch is cheap to fix.
	Warnings []string
}

// importDeps is the destination's inventory, looked up once per request rather than once per rule.
type importDeps struct {
	agents      map[string]struct{}
	scripts     map[string]struct{}
	macros      map[string]struct{}
	unavailable map[string]string
}

// existingImportDeps collects what an imported rule might reference.
func (s *Server) existingImportDeps(c *gin.Context) importDeps {
	return importDeps{
		agents:      s.existingAgentIDs(c),
		scripts:     s.existingScriptIDs(c),
		macros:      s.existingMacroIDs(c),
		unavailable: s.agentAvailability(c),
	}
}

// checkRuleReferences reports the references an imported rule carries that do not exist here.
func (d importDeps) checkRuleReferences(rule portableRule) []string {
	var warnings []string
	for _, action := range rule.Actions {
		for _, target := range actionTargets(action) {
			switch target.kind {
			case "agent":
				if target.id == "" {
					continue
				}
				if _, ok := d.agents[target.id]; !ok {
					warnings = append(warnings, "targets agent "+target.id+", which does not exist here")
					continue
				}
				if reason := d.unavailable[target.id]; reason != "" {
					warnings = append(warnings, "targets agent "+target.id+", which cannot accept tasks: "+reason)
				}
			case "script":
				if _, ok := d.scripts[target.id]; !ok && target.id != "" {
					warnings = append(warnings, "targets script "+target.id+", which does not exist here")
				}
			case "macro":
				if _, ok := d.macros[target.id]; !ok && target.id != "" {
					warnings = append(warnings, "targets macro "+target.id+", which does not exist here")
				}
			}
		}
	}
	return warnings
}

// importOneRule validates and writes a single portable rule.
func (s *Server) importOneRule(c *gin.Context, rule portableRule, overwrite, dryRun bool, deps importDeps) ruleImportOutcome {
	outcome := ruleImportOutcome{Name: strings.TrimSpace(rule.Name)}
	outcome.Warnings = deps.checkRuleReferences(rule)

	if outcome.Name == "" {
		outcome.Status, outcome.Reason = "invalid", "the rule needs a name"
		return outcome
	}
	if strings.TrimSpace(rule.EventType) == "" {
		outcome.Status, outcome.Reason = "invalid", "the rule needs an event_type; a rule without one never fires"
		return outcome
	}
	if err := validateRuleConditions(rule.Conditions); err != nil {
		outcome.Status, outcome.Reason = "invalid", err.Error()
		return outcome
	}
	if len(rule.Actions) == 0 {
		outcome.Status, outcome.Reason = "invalid", "the rule needs at least one action"
		return outcome
	}

	enabled := true
	if rule.Enabled != nil {
		enabled = *rule.Enabled
	}
	outcome.Enabled = enabled
	tenantID := s.currentTenantID(c)

	// A name collision is refused unless the caller said overwrite: an import that silently replaces a rule the operator
	// built by hand is worse than a rejection.
	var existing db.AutomationRule
	found := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.AutomationRule{}), c).
		Where("name = ?", outcome.Name).First(&existing).Error == nil
	if found && !overwrite {
		outcome.Status = "conflict"
		outcome.Reason = fmt.Sprintf("a rule named %q already exists; pass overwrite to replace it", outcome.Name)
		return outcome
	}

	// The id is derived from the name on first import and kept on overwrite, so importing the same document twice is
	// idempotent rather than creating twins.
	ruleID := existing.ID
	if !found {
		ruleID = "imported-" + slugifyRuleName(outcome.Name)
		// A slug is not unique: two names can normalise the same way, so fall back to a timestamp suffix rather than
		// overwriting a rule the caller did not name.
		var clash db.AutomationRule
		if err := s.db.WithContext(s.ctx).First(&clash, "id = ?", ruleID).Error; err == nil {
			ruleID = fmt.Sprintf("%s-%d", ruleID, time.Now().UnixNano())
		}
	}

	toSave := AutomationRule{
		ID:            ruleID,
		Name:          outcome.Name,
		Enabled:       enabled,
		EventType:     rule.EventType,
		Conditions:    rule.Conditions,
		Actions:       rule.Actions,
		Cooldown:      rule.CooldownSeconds,
		Schedule:      rule.Schedule,
		TenantID:      tenantID,
		LastTriggered: existing.LastTriggered,
		RunCount:      existing.RunCount,
	}
	// The verdict is the same either way; only the write differs, so a preview cannot disagree with the import that
	// follows it.
	if found {
		outcome.Status = "replaced"
	} else {
		outcome.Status = "created"
	}
	outcome.RuleID = ruleID
	if dryRun {
		return outcome
	}

	if err := s.saveAutomationRule(toSave); err != nil {
		outcome.Status, outcome.Reason = "invalid", "failed to save"
		return outcome
	}

	if found {
		s.LogAuditRecord(c, "automation_rule_import_overwrite", "automation", ruleID,
			fmt.Sprintf("replaced rule %q from an imported document", outcome.Name), true, nil)
		return outcome
	}
	s.LogAuditRecord(c, "automation_rule_import", "automation", ruleID,
		fmt.Sprintf("imported rule %q (%d condition(s), %d action(s))",
			outcome.Name, len(rule.Conditions), len(rule.Actions)), true, nil)
	return outcome
}

// importRuleSet imports a whole document, reporting per rule.
func (s *Server) importRuleSet(c *gin.Context, rules []portableRule, overwrite, dryRun bool) {
	const maxRules = 200
	if len(rules) > maxRules {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("too many rules (%d); the limit is %d", len(rules), maxRules))
		return
	}

	type result struct {
		Name     string   `json:"name"`
		Status   string   `json:"status"`
		RuleID   string   `json:"rule_id,omitempty"`
		Reason   string   `json:"reason,omitempty"`
		Warnings []string `json:"warnings,omitempty"`
	}
	deps := s.existingImportDeps(c)
	results := make([]result, 0, len(rules))
	counts := map[string]int{"created": 0, "replaced": 0, "conflict": 0, "invalid": 0}
	for _, rule := range rules {
		outcome := s.importOneRule(c, rule, overwrite, dryRun, deps)
		counts[outcome.Status]++
		results = append(results, result{
			Name: outcome.Name, Status: outcome.Status, RuleID: outcome.RuleID, Reason: outcome.Reason,
			Warnings: outcome.Warnings,
		})
	}

	if dryRun {
		// A preview is audited too: "who checked what an import would do" is a question a review will ask.
		s.LogAuditRecord(c, "automation_rule_import_preview", "automation", "",
			fmt.Sprintf("previewed a rule set: %d would be created, %d replaced, %d conflict(s), %d invalid",
				counts["created"], counts["replaced"], counts["conflict"], counts["invalid"]), true, nil)
		respond(c, gin.H{"counts": counts, "results": results, "dry_run": true})
		return
	}
	s.LogAuditRecord(c, "automation_rule_import_set", "automation", "",
		fmt.Sprintf("imported a rule set: %d created, %d replaced, %d conflict(s), %d invalid",
			counts["created"], counts["replaced"], counts["conflict"], counts["invalid"]), true, nil)

	respond(c, gin.H{"counts": counts, "results": results})
}

// slugifyRuleName builds a readable id fragment from a rule name.
//
// Whitespace and punctuation become dashes, everything else is lowercased: the id has to be usable in a URL and legible in a
// log line, and readability matters more than reversibility here.
func slugifyRuleName(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	lastDash := false
	for _, r := range lowered {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(builder.String(), "-")
	if out == "" {
		out = "rule"
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	return out
}
