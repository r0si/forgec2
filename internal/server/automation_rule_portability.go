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
// GET  /api/automation/rules/:id/export    -> {"rule": {...}}
// POST /api/automation/rules/import        <- {"rule": {...}, "overwrite": false}

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
		enabled := rule.Enabled
		portable := portableRule{
			Name:            rule.Name,
			EventType:       rule.EventType,
			Enabled:         &enabled,
			Conditions:      rule.Conditions,
			Actions:         rule.Actions,
			CooldownSeconds: rule.Cooldown,
			Schedule:        rule.Schedule,
		}
		respond(c, gin.H{"rule": portable})
		return
	}
	respondError(c, http.StatusNotFound, "rule not found")
}

// handleImportAutomationRule creates a rule from a portable document.
func (s *Server) handleImportAutomationRule(c *gin.Context) {
	var body struct {
		Rule      portableRule `json:"rule"`
		Overwrite bool         `json:"overwrite"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"rule": {...}, "overwrite": false}`)
		return
	}
	rule := body.Rule
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		respondError(c, http.StatusBadRequest, "the rule needs a name")
		return
	}
	if strings.TrimSpace(rule.EventType) == "" {
		respondError(c, http.StatusBadRequest, "the rule needs an event_type; a rule without one never fires")
		return
	}
	if len(rule.Actions) == 0 {
		respondError(c, http.StatusBadRequest, "the rule needs at least one action")
		return
	}

	enabled := true
	if rule.Enabled != nil {
		enabled = *rule.Enabled
	}
	tenantID := s.currentTenantID(c)

	// A name collision is refused unless the caller said overwrite: an import that silently replaces a rule the operator
	// built by hand is worse than a rejection.
	var existing db.AutomationRule
	found := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.AutomationRule{}), c).
		Where("name = ?", rule.Name).First(&existing).Error == nil
	if found && !body.Overwrite {
		respondError(c, http.StatusConflict, fmt.Sprintf("a rule named %q already exists; pass overwrite to replace it", rule.Name))
		return
	}

	// The id is derived from the name on first import and kept on overwrite, so importing the same document twice is
	// idempotent rather than creating twins.
	ruleID := existing.ID
	if !found {
		ruleID = "imported-" + slugifyRuleName(rule.Name)
		// A slug is not unique: two names can normalise the same way, so fall back to a timestamp suffix rather than
		// overwriting a rule the caller did not name.
		var clash db.AutomationRule
		if err := s.db.WithContext(s.ctx).First(&clash, "id = ?", ruleID).Error; err == nil {
			ruleID = fmt.Sprintf("%s-%d", ruleID, time.Now().Unix())
		}
	}

	toSave := AutomationRule{
		ID:            ruleID,
		Name:          rule.Name,
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
	if err := s.saveAutomationRule(toSave); err != nil {
		handleQueryError(c, err, "Failed to import the rule")
		return
	}

	action := "automation_rule_import"
	detail := fmt.Sprintf("imported rule %q (%d condition(s), %d action(s))", rule.Name, len(rule.Conditions), len(rule.Actions))
	if found {
		action = "automation_rule_import_overwrite"
		detail = fmt.Sprintf("replaced rule %q from an imported document", rule.Name)
	}
	s.LogAuditRecord(c, action, "automation", ruleID, detail, true, nil)

	respond(c, gin.H{"rule_id": ruleID, "overwrote": found, "enabled": enabled})
}

// slugifyRuleName builds a readable id fragment from a rule name.
//
// Whispace and punctuation become dashes, everything else is lowercased: the id has to be usable in a URL and legible in a
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
