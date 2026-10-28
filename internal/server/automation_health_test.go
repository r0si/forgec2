package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAutomationHealth covers the static checks on a rule SET - the questions no per-event preview can reach.
func TestAutomationHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	conditions := func(conds []RuleCondition) string {
		raw, _ := json.Marshal(conds)
		return string(raw)
	}
	actions := func(acts []RuleAction) string {
		raw, _ := json.Marshal(acts)
		return string(raw)
	}

	// One existing agent, so a rule pointing at a missing one can be told apart.
	if err := s.db.Create(&db.Implant{ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// A macro that exists, for the same reason.
	if err := s.db.Create(&db.CommandMacro{ID: 7, Name: "recon"}).Error; err != nil {
		t.Fatalf("seed macro: %v", err)
	}

	seed := func(id, name string, slighty string, conds []RuleCondition, acts []RuleAction, tenant uint) {
		rule := db.AutomationRule{
			ID: id, Name: name, Enabled: true, EventType: slighty,
			Conditions: conditions(conds), Actions: actions(acts), TenantID: tenant,
		}
		if err := s.db.Create(&rule).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// An unconditional rule that runs a command: fires on every event of its type.
	seed("unconditional", "unconditional command", string(EventImplantCheckin), nil,
		[]RuleAction{{Type: ActionRunCommand, Params: json.RawMessage(`{"command":"whoami"}`)}}, 1)

	// A rule with no conditions that only notifies: noisy, not dangerous - must NOT be reported.
	seed("notify-only", "notify only", string(EventImplantCheckin), nil,
		[]RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"hi"}`)}}, 1)

	// A rule whose command targets an agent that does not exist.
	seed("ghost-agent", "ghost target", string(EventTaskComplete),
		[]RuleCondition{{Field: "agent.hostname", Operator: "contains", Value: "web"}},
		[]RuleAction{{Type: ActionRunCommand, Params: json.RawMessage(`{"command":"whoami","agent_id":"gone"}`)}}, 1)

	// A rule whose macro exists, and a webhook with no target of either kind: no finding expected.
	seed("healthy", "healthy rule", string(EventTaskComplete),
		[]RuleCondition{{Field: "agent.hostname", Operator: "contains", Value: "web"}},
		[]RuleAction{{Type: ActionRunMacro, Params: json.RawMessage(`{"macro_id":"7"}`)}}, 1)

	// Two rules with identical bodies: the actions would run twice.
	dupConds := []RuleCondition{{Field: "agent.hostname", Operator: "equals", Value: "web-01"}}
	dupActs := []RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"dup"}`)}}
	seed("dup-a", "duplicate a", string(EventImplantCheckin), dupConds, dupActs, 1)
	seed("dup-b", "duplicate b", string(EventImplantCheckin), dupConds, dupActs, 1)

	// Another tenant's unconditional rule must not appear.
	seed("foreign", "foreign unconditional", string(EventImplantCheckin), nil,
		[]RuleAction{{Type: ActionRunCommand, Params: json.RawMessage(`{"command":"whoami"}`)}}, 2)

	s.invalidateAutomationCache()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/automation/health", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleAutomationHealth(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Summary map[string]int `json:"summary"`
			Rules   []struct {
				RuleID        string  `json:"rule_id"`
				RuleName      string  `json:"rule_name"`
				Enabled       bool    `json:"enabled"`
				RunCount      int     `json:"run_count"`
				LastTriggered *string `json:"last_triggered"`
			} `json:"rules"`
			Findings []struct {
				RuleID   string `json:"rule_id"`
				RuleName string `json:"rule_name"`
				Severity string `json:"severity"`
				Code     string `json:"code"`
				Detail   string `json:"detail"`
			} `json:"findings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	byCode := map[string]int{}
	for _, finding := range resp.Data.Findings {
		byCode[finding.Code]++
		if finding.RuleID == "foreign" {
			t.Fatalf("another tenant's rule appeared: %+v", finding)
		}
	}

	// The unconditional mutating action is an error; the notify-only rule of the same shape is not.
	if byCode["unconditional_mutating_action"] != 1 {
		t.Fatalf("unconditional findings = %d, want 1 (%v)", byCode["unconditional_mutating_action"], resp.Data.Findings)
	}
	// The dangling agent target is an error.
	if byCode["missing_agent"] != 1 {
		t.Fatalf("missing_agent findings = %d, want 1", byCode["missing_agent"])
	}
	// Both duplicates are reported, one finding each.
	if byCode["duplicate_rule"] != 2 {
		t.Fatalf("duplicate findings = %d, want 2", byCode["duplicate_rule"])
	}
	// The healthy rule (existing macro, conditions present) produces nothing.
	for _, finding := range resp.Data.Findings {
		if finding.RuleID == "healthy" || finding.RuleID == "notify-only" {
			t.Fatalf("a healthy rule was reported: %+v", finding)
		}
	}

	// The summary counts match the findings, and rule counts are tenant-scoped.
	if resp.Data.Summary["errors"] != 2 || resp.Data.Summary["warnings"] != 2 {
		t.Fatalf("summary = %v, want 2 errors and 2 warnings", resp.Data.Summary)
	}
	if resp.Data.Summary["rules"] != 6 {
		t.Fatalf("rules = %d, want the six of this tenant", resp.Data.Summary["rules"])
	}

	// Firing statistics: a rule that has never fired and one that fires constantly look identical in the list, so both
	// are surfaced here. The seeded rules have never fired, which is itself the finding.
	if len(resp.Data.Rules) != 6 {
		t.Fatalf("rules stats = %d, want the six of this tenant", len(resp.Data.Rules))
	}
	for _, entry := range resp.Data.Rules {
		if entry.RunCount != 0 {
			t.Fatalf("%s reports run_count %d, want 0 for a freshly seeded rule", entry.RuleName, entry.RunCount)
		}
		if entry.LastTriggered != nil {
			t.Fatalf("%s reports a last_triggered it never had", entry.RuleName)
		}
	}
	// A freshly created rule has not had a chance to fire, so the report must stay quiet about it: that is the
	// difference between a finding and noise.
	if byCode["never_fired"] != 0 {
		t.Fatalf("a rule created seconds ago was reported as never fired: %v", resp.Data.Findings)
	}

	// An OLD rule that has still never fired is the real signal.
	old := db.AutomationRule{
		ID: "stale", Name: "stale rule", Enabled: true, EventType: string(EventImplantCheckin),
		Conditions: conditions([]RuleCondition{{Field: "agent.hostname", Operator: "contains", Value: "web"}}),
		Actions:    actions([]RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"x"}`)}}),
		TenantID:   1,
	}
	if err := s.db.Create(&old).Error; err != nil {
		t.Fatalf("seed stale rule: %v", err)
	}
	// created_at is set by the ORM only on insert, so backdate it explicitly.
	if err := s.db.Model(&db.AutomationRule{}).Where("id = ?", "stale").
		Update("created_at", time.Now().Add(-72*time.Hour)).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	s.invalidateAutomationCache()

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/automation/health", nil)
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handleAutomationHealth(c2)
	var aged struct {
		Data struct {
			Findings []struct {
				RuleID string `json:"rule_id"`
				Code   string `json:"code"`
			} `json:"findings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &aged); err != nil {
		t.Fatalf("decode aged: %v", err)
	}
	foundStale := false
	for _, finding := range aged.Data.Findings {
		if finding.RuleID == "stale" && finding.Code == "never_fired" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Fatalf("a rule enabled for three days with no firing was not reported: %+v", aged.Data.Findings)
	}

	// Errors come first, so the report opens with what will bite.
	if len(resp.Data.Findings) > 0 && resp.Data.Findings[0].Severity != "error" {
		t.Fatalf("first finding is %q, want an error", resp.Data.Findings[0].Severity)
	}
}
