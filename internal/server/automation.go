package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/scripting"
	"gorm.io/gorm"
)

type AutomationRule struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Enabled       bool            `json:"enabled"`
	Priority      int             `json:"priority"` // higher = runs first
	EventType     string          `json:"event_type"`
	Conditions    []RuleCondition `json:"conditions"`
	Actions       []RuleAction    `json:"actions"`
	Cooldown      int             `json:"cooldown"` // seconds between triggers
	LastTriggered time.Time       `json:"last_triggered"`
	RunCount      int             `json:"run_count"`
	Schedule      string          `json:"schedule"` // cron/interval for event_type="schedule"
	AgentID       string          `json:"agent_id"`
	TaskType      string          `json:"task_type"`
	Command       string          `json:"command"`
	Params        string          `json:"params"`
	LastRun       time.Time       `json:"last_run"`
	NextRun       time.Time       `json:"next_run"`
	CreatedBy     string          `json:"created_by"`
	TenantID      uint            `json:"tenant_id"`
	CreatedAt     string          `json:"created_at"`
}

// EventSchedule is the pseudo event type used to mark schedule-driven rules.
const EventSchedule = "schedule"

// Action types for automation rules
const (
	ActionRunCommand = "command"
	ActionWebhook    = "webhook"
	ActionNotify     = "notify"
	ActionRunScript  = "script"
	ActionCreateTask = "create_task"
	ActionSetSleep   = "set_sleep"
	ActionRunMacro   = "run_macro"
)

type RuleCondition struct {
	Field    string `json:"field"`    // "agent.hostname", "data.*"
	Operator string `json:"operator"` // "contains", "equals", "regex"
	Value    string `json:"value"`
}

type RuleAction struct {
	Type   string          `json:"type"` // "command", "webhook", "notify"
	Params json.RawMessage `json:"params"`
}

func (s *Server) evaluateRule(evt Event, rule AutomationRule) {
	for _, cond := range rule.Conditions {
		if !s.matchCondition(cond, evt) {
			return
		}
	}
	// Cooldown throttle: without it a rule on agent.checkin fires on EVERY
	// beacon, and a task.complete+run_macro rule re-triggers itself forever
	// (macro step → new task → completion event → macro step ...). The
	// last-triggered stamp is persisted so restarts honor the window too.
	if rule.Cooldown > 0 && !rule.LastTriggered.IsZero() &&
		time.Since(rule.LastTriggered) < time.Duration(rule.Cooldown)*time.Second {
		return
	}
	for _, action := range rule.Actions {
		s.executeAction(action, evt)
	}
	// Stamp the firing unconditionally: run_count is operator-visible stats,
	// last_triggered powers the cooldown window above (survives restarts).
	s.persistScheduleState(rule.ID, map[string]interface{}{
		"last_triggered": time.Now(),
		"run_count":      gorm.Expr("run_count + 1"),
	})
}

func (s *Server) matchCondition(cond RuleCondition, evt Event) bool {
	var val string
	switch cond.Field {
	case "agent.hostname":
		val = evt.AgentHost
	default:
		if v, ok := evt.Data[cond.Field]; ok {
			val = fmt.Sprintf("%v", v)
		}
	}
	switch cond.Operator {
	case "contains":
		return strings.Contains(strings.ToLower(val), strings.ToLower(cond.Value))
	case "equals":
		return val == cond.Value
	default:
		return true
	}
}

func (s *Server) executeAction(action RuleAction, evt Event) {
	switch action.Type {
	case ActionRunCommand:
		var params struct {
			Command string `json:"command"`
			AgentID string `json:"agent_id"` // optional: target specific agent
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			slog.Error("Automation: unmarshal command params", "error", err)
			return
		}
		if params.Command != "" {
			targetAgent := s.automationTarget(evt, params.AgentID)
			if targetAgent != "" {
				expanded := s.expandTemplate(params.Command, evt)
				if _, err := s.createSystemTask(targetAgent, "automation", expanded, "", "pending", "automation"); err != nil {
					slog.Error("Automation: failed to create command task", "error", err)
				}
			}
		}
	case ActionWebhook:
		var params struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Secret  string            `json:"secret"` // HMAC secret
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			slog.Error("Automation: unmarshal webhook params", "error", err)
			return
		}
		if params.URL != "" {
			go s.executeWebhook(params, evt)
		}
	case ActionNotify:
		var params struct {
			Message string `json:"message"`
			Channel string `json:"channel"` // "all", "ops", "admins"
		}
		if err := json.Unmarshal(action.Params, &params); err == nil {
			msg := s.expandTemplate(params.Message, evt)
			// Structured marshal: the previous fmt.Sprintf template produced
			// invalid JSON frames whenever the message contained a quote.
			if frame, ok := marshalJSONSafe(map[string]interface{}{"type": "notification", "message": msg, "source": "automation"}); ok {
				s.broadcastToClients(frame)
			}
		}
	case ActionRunScript:
		var params struct {
			ScriptID string `json:"script_id"`
			Code     string `json:"code"`
		}
		if err := json.Unmarshal(action.Params, &params); err == nil {
			engine := scripting.GetEngine()
			// Deep-copy the event handed to the VM: goja exposes evt.Data as
			// a MUTABLE Go map, and a script writing event.Data[x] would race
			// the concurrently spawned webhook marshalers (fatal concurrent
			// map write). Scripts get a snapshot.
			dataCopy := make(map[string]interface{}, len(evt.Data))
			for k, v := range evt.Data {
				dataCopy[k] = v
			}
			scriptEvt := evt
			scriptEvt.Data = dataCopy
			context := map[string]interface{}{
				"event":    scriptEvt,
				"agent_id": evt.AgentID,
			}
			// Automation-triggered scripts run with the standard user role
			// permissions: they can read agents/credentials and queue tasks
			// (the same powers rule authors already have via other actions),
			// but cannot reach the network (httpRequest stays admin-only).
			// Tenant flows through the event (P0-2 fills evt.TenantID at
			// fire time): scripts inherit exactly the firing tenant's reach.
			caller := scripting.Caller{Username: "automation", Role: db.RoleUser, TenantID: evt.TenantID}
			// Capture the outcome. These results used to be discarded, so an automation script
			// that failed on every event - a typo, a permission the automation role does not have,
			// a timeout - left no trace anywhere: the rule "ran", the event was consumed, and the
			// only symptom was whatever the script was supposed to do not happening.
			started := time.Now()
			var res scripting.ExecutionResult
			switch {
			case params.ScriptID != "":
				res = engine.Execute(params.ScriptID, context, caller)
			case params.Code != "":
				res = engine.ExecuteCode(params.Code, context, caller)
			}
			fields := []any{
				"script_id", params.ScriptID,
				"event", string(evt.Type),
				"agent_id", evt.AgentID,
				"tenant_id", evt.TenantID,
				"duration_ms", time.Since(started).Milliseconds(),
			}
			if res.Success {
				slog.Debug("Automation: script ran", fields...)
			} else {
				slog.Warn("Automation: script failed",
					append(fields, "error", truncateString(res.Error, 300))...)
			}
		}
	case ActionCreateTask:
		var params struct {
			AgentID string `json:"agent_id"`
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(action.Params, &params); err == nil {
			targetAgent := s.automationTarget(evt, params.AgentID)
			if targetAgent == "" || params.Type == "" {
				// A rule that cannot act must say so: silently doing nothing is the same symptom as
				// a rule that never matched, and there was no trace of either.
				slog.Warn("Automation: create_task skipped, the rule has no usable target",
					"event", string(evt.Type), "agent_id", params.AgentID, "task_type", params.Type)
				break
			}
			{
				expanded := s.expandTemplate(params.Command, evt)
				task, err := s.createTask(targetAgent, params.Type, expanded, "", "", "", 0, 0)
				if err != nil {
					slog.Error("Automation: failed to create task", "error", err)
				} else if err := s.db.Model(task).Update("created_by", "automation").Error; err != nil {
					slog.Error("Automation: failed to mark task as automation", "task_id", task.ID, "error", err)
				}
			}
		}
	case ActionSetSleep:
		var params struct {
			AgentID  string `json:"agent_id"`
			Interval int    `json:"interval"`
			Jitter   int    `json:"jitter"`
		}
		if err := json.Unmarshal(action.Params, &params); err == nil {
			targetAgent := s.automationTarget(evt, params.AgentID)
			if targetAgent == "" || params.Interval <= 0 {
				slog.Warn("Automation: set_sleep skipped, the rule has no usable target or interval",
					"event", string(evt.Type), "agent_id", params.AgentID, "interval", params.Interval)
				break
			}
			{
				interval, jitter, clamped := s.clampSleepInts(params.Interval, params.Jitter)
				if clamped {
					s.LogAuditRecord(nil, "automation_sleep_clamped", "agent", targetAgent,
						"automation set_sleep raised to OPSEC floors", true, nil)
				}
				cmd := fmt.Sprintf("%d,%d", interval, jitter)
				if _, err := s.createSystemTask(targetAgent, "set_sleep", cmd, "", "pending", "automation"); err != nil {
					slog.Error("Automation: failed to create set_sleep task", "error", err)
				}
			}
		}
	case ActionRunMacro:
		var params struct {
			MacroID     uint `json:"macro_id"`
			StopOnError bool `json:"stop_on_error"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil || params.MacroID == 0 {
			slog.Error("Automation: invalid run_macro params")
			return
		}
		var macro db.CommandMacro
		if err := s.db.First(&macro, params.MacroID).Error; err != nil {
			slog.Error("Automation: macro not found for run_macro action", "macro_id", params.MacroID, "error", err)
			return
		}
		// Macro rows are tenant assets too: a rule must not execute (or
		// probe the existence of) another tenant's playbook.
		if evt.TenantID != 0 && macro.TenantID != evt.TenantID {
			slog.Warn("Automation: cross-tenant macro blocked", "macro_id", params.MacroID, "tenant", evt.TenantID)
			s.LogAuditRecord(nil, "automation_blocked_cross_tenant", "macro", fmt.Sprint(params.MacroID),
				"run_macro referenced foreign-tenant macro", true, nil)
			return
		}
		targetAgent := evt.AgentID
		if targetAgent == "" {
			slog.Warn("Automation: run_macro skipped, event has no agent context", "macro", macro.Name)
			return
		}
		// Run against the triggering agent so playbooks like "on connect,
		// auto-recon" work without pinning a specific host.
		macroCopy := macro
		if err := s.startMacroRun(&macroCopy, targetAgent, "automation", params.StopOnError); err != nil {
			slog.Error("Automation: failed to start macro run", "macro", macro.Name, "agent_id", targetAgent, "error", err)
		} else {
			slog.Info("Automation: macro dispatched", "macro", macro.Name, "agent_id", targetAgent)
		}
	}
}

// expandTemplate replaces template variables in a string with event data.
func (s *Server) expandTemplate(input string, evt Event) string {
	result := input
	result = strings.ReplaceAll(result, "{{agent_id}}", evt.AgentID)
	result = strings.ReplaceAll(result, "{{hostname}}", evt.AgentHost)
	result = strings.ReplaceAll(result, "{{ip}}", evt.AgentIP)
	result = strings.ReplaceAll(result, "{{os}}", evt.AgentOS)
	result = strings.ReplaceAll(result, "{{user}}", evt.User)
	result = strings.ReplaceAll(result, "{{event_type}}", string(evt.Type))
	return result
}

// executeWebhook sends a webhook request with optional HMAC signature.
func (s *Server) executeWebhook(params struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Secret  string            `json:"secret"`
}, evt Event) {
	if err := webhookTargetValidator(params.URL); err != nil {
		slog.Error("Automation: webhook URL rejected", "error", err)
		return
	}
	body, err := json.Marshal(evt)
	if err != nil {
		slog.Error("Automation: marshal webhook body", "error", err)
		return
	}
	method := params.Method
	if method == "" {
		method = "POST"
	}
	req, err := http.NewRequest(method, params.URL, bytes.NewReader(body))
	if err != nil {
		slog.Error("Automation: create webhook request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ForgeC2-Automation/1.0")

	// Add custom headers
	for k, v := range params.Headers {
		req.Header.Set(k, v)
	}

	// Add HMAC signature if secret is configured
	if params.Secret != "" {
		h := hmac.New(sha256.New, []byte(params.Secret))
		h.Write(body)
		req.Header.Set("X-ForgeC2-Signature", hex.EncodeToString(h.Sum(nil)))
	}

	resp, err := ssrfSafeClient(s.httpClient).Do(req)
	if err != nil {
		// *url.Error embeds the full URL (may carry a webhook secret): log cause only.
		slog.Error("Automation: webhook request failed", "error", err)
		return
	}
	defer resp.Body.Close()
}

func (s *Server) loadAutomationRules() []AutomationRule {
	s.automationRulesMu.RLock()
	cached := s.automationRules
	age := time.Since(s.automationRulesAt)
	s.automationRulesMu.RUnlock()
	if cached != nil && age < 30*time.Second {
		return cached
	}

	var dbRules []db.AutomationRule
	if err := s.db.Limit(AutomationRuleLimit).Find(&dbRules).Error; err != nil {
		slog.Error("Automation: failed to load rules", "error", err)
		// Do NOT cache the empty result: a transient SQLite lock would
		// otherwise blind all event automation for the full 30s TTL. Return
		// the previous snapshot when one exists.
		s.automationRulesMu.RLock()
		stale := s.automationRules
		s.automationRulesMu.RUnlock()
		return stale
	}

	var rules []AutomationRule
	for _, dr := range dbRules {
		var conditions []RuleCondition
		if dr.Conditions != "" {
			if err := json.Unmarshal([]byte(dr.Conditions), &conditions); err != nil {
				// Corrupt conditions must NOT degrade to match-all: evaluateRule
				// treats a nil condition list as pass-through, so a partially
				// corrupt row would fire every action on every event. Skip it.
				slog.Warn("Automation: skipping rule with corrupt conditions", "rule", dr.Name, "error", err)
				continue
			}
		}
		var actions []RuleAction
		if dr.Actions != "" {
			if err := json.Unmarshal([]byte(dr.Actions), &actions); err != nil {
				slog.Warn("Automation: unmarshal actions", "rule", dr.Name, "error", err)
			}
		}
		if len(actions) == 0 {
			continue // nothing to do; don't even keep it for schedule state churn
		}
		rules = append(rules, AutomationRule{
			ID:            dr.ID,
			Name:          dr.Name,
			Enabled:       dr.Enabled,
			EventType:     dr.EventType,
			Conditions:    conditions,
			Actions:       actions,
			Cooldown:      dr.CooldownSeconds,
			Schedule:      dr.Schedule,
			AgentID:       dr.AgentID,
			TaskType:      dr.TaskType,
			Command:       dr.Command,
			Params:        dr.Params,
			LastRun:       dr.LastRun,
			NextRun:       dr.NextRun,
			RunCount:      dr.RunCount,
			LastTriggered: dr.LastTriggered,
			CreatedBy:     dr.CreatedBy,
			TenantID:      dr.TenantID,
			CreatedAt:     dr.CreatedAt.Format(time.RFC3339),
		})
	}

	s.automationRulesMu.Lock()
	s.automationRules = rules
	s.automationRulesAt = time.Now()
	s.automationRulesMu.Unlock()

	return rules
}

func (s *Server) invalidateAutomationCache() {
	s.automationRulesMu.Lock()
	s.automationRules = nil
	s.automationRulesAt = time.Time{}
	s.automationRulesMu.Unlock()
}

func (s *Server) saveAutomationRule(rule AutomationRule) error {
	conditionsData, err := json.Marshal(rule.Conditions)
	if err != nil {
		return fmt.Errorf("marshal conditions: %w", err)
	}
	actionsData, err := json.Marshal(rule.Actions)
	if err != nil {
		return fmt.Errorf("marshal actions: %w", err)
	}

	dbRule := db.AutomationRule{
		ID:              rule.ID,
		Name:            rule.Name,
		Enabled:         rule.Enabled,
		EventType:       rule.EventType,
		Conditions:      string(conditionsData),
		Actions:         string(actionsData),
		Schedule:        rule.Schedule,
		AgentID:         rule.AgentID,
		TaskType:        rule.TaskType,
		Command:         rule.Command,
		Params:          rule.Params,
		LastRun:         rule.LastRun,
		NextRun:         rule.NextRun,
		RunCount:        rule.RunCount,
		CooldownSeconds: rule.Cooldown,
		CreatedBy:       rule.CreatedBy,
		TenantID:        rule.TenantID,
	}
	if rule.LastTriggered.After(dbRule.LastTriggered) {
		dbRule.LastTriggered = rule.LastTriggered
	}

	if rule.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, rule.CreatedAt); err == nil {
			dbRule.CreatedAt = t
		}
	}
	// Save() writes every column: when the caller's snapshot carries no
	// last_triggered (e.g. the toggle handler re-saving a cached rule),
	// carry the persisted stamp over so a toggle never resets an active
	// cooldown window.
	if rule.LastTriggered.IsZero() {
		var existing db.AutomationRule
		if err := s.db.Select("last_triggered").First(&existing, "id = ?", rule.ID).Error; err == nil {
			dbRule.LastTriggered = existing.LastTriggered
		}
	}

	if err := s.db.Save(&dbRule).Error; err != nil {
		return err
	}
	s.invalidateAutomationCache()
	return nil
}

func (s *Server) deleteAutomationRule(id string) error {
	err := s.db.Delete(&db.AutomationRule{}, "id = ?", id).Error
	if err == nil {
		s.invalidateAutomationCache()
	}
	return err
}

func (s *Server) migrateAutomationRules() {
	var count int64
	if err := s.db.Model(&db.AutomationRule{}).Count(&count).Error; err != nil {
		slog.Error("Automation: failed to count rules", "error", err)
	}
	if count > 0 {
		return
	}

	raw := s.getConfigJSON("automation_rules")
	if raw == "" {
		return
	}

	var rules []AutomationRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return
	}

	for _, rule := range rules {
		if err := s.saveAutomationRule(rule); err != nil {
			slog.Warn("Automation: failed to import rule", "id", rule.ID, "error", err)
		}
	}

	if err := s.db.Where("key = ?", "automation_rules").Delete(&db.ServerConfig{}).Error; err != nil {
		slog.Error("Automation: failed to delete legacy config", "error", err)
	}
}

func (s *Server) getConfigJSON(key string) string {
	var cfg struct{ Value string }
	if err := s.db.Model(&db.ServerConfig{}).Where("key = ?", key).First(&cfg).Error; err != nil {
		slog.Debug("Config key not found", "key", key, "error", err)
	}
	return cfg.Value
}

func (s *Server) registerBuiltinAutomations() {
	rule := AutomationRule{
		ID:        "auto_dc_alert",
		Name:      "DC Login Alert",
		Enabled:   true,
		EventType: string(EventImplantCheckin),
		Conditions: []RuleCondition{
			{Field: "agent.hostname", Operator: "contains", Value: "DC"},
		},
		Actions: []RuleAction{
			{
				Type:   "command",
				Params: json.RawMessage(`{"command": "ldap_users"}`),
			},
			{
				Type:   "webhook",
				Params: json.RawMessage(`{"url": "", "method": "POST"}`),
			},
		},
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	rules := s.loadAutomationRules()
	exists := false
	for _, r := range rules {
		if r.ID == rule.ID {
			exists = true
			break
		}
	}
	if !exists {
		if err := s.saveAutomationRule(rule); err != nil {
			slog.Warn("Automation: failed to register builtin rule", "id", rule.ID, "error", err)
		}
	}
}

// persistScheduleState writes ONLY the scheduler bookkeeping columns for a
// rule. The previous full-row saveAutomationRule() path rewrote command/
// actions/enabled from a cache snapshot up to 30s stale, silently reverting
// concurrent operator edits.
func (s *Server) persistScheduleState(ruleID string, updates map[string]interface{}) {
	if len(updates) == 0 {
		return
	}
	if err := s.db.Model(&db.AutomationRule{}).Where("id = ?", ruleID).Updates(updates).Error; err != nil {
		slog.Error("Automation: persist schedule state failed", "id", ruleID, "error", err)
	}
}

// schedulerLoop periodically dispatches schedule-driven automation rules
// (event_type="schedule"). It runs every 30s and only touches rules whose
// next_run is due, so interval and cron expressions both work.
func (s *Server) schedulerLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.dispatchScheduledRules()
		}
	}
}

func (s *Server) dispatchScheduledRules() {
	now := time.Now()
	rules := s.loadAutomationRules()
	for _, rule := range rules {
		if rule.EventType != EventSchedule || !rule.Enabled || strings.TrimSpace(rule.Schedule) == "" {
			continue
		}
		next := rule.NextRun
		if next.IsZero() {
			computed, err := nextScheduleTime(rule.Schedule, now)
			if err != nil {
				slog.Warn("Automation: bad schedule, disabling rule", "rule", rule.Name, "schedule", rule.Schedule, "error", err)
				continue
			}
			rule.NextRun = computed
			// Targeted write: a full-row Save here rewrote the rule from a
			// possibly stale cache snapshot and reverted operator edits.
			s.persistScheduleState(rule.ID, map[string]interface{}{"next_run": computed})
			continue
		}
		if next.After(now) {
			continue
		}

		evt := Event{
			Type:    EventSchedule,
			AgentID: rule.AgentID,
			// Targeted schedules inherit the rule's tenant; agent-less
			// schedules stay legacy 0 (ruleMayFireOn already gates firing).
			TenantID: rule.TenantID,
		}
		if rule.AgentID != "" {
			var implant db.Implant
			if err := s.db.First(&implant, "id = ?", rule.AgentID).Error; err == nil {
				evt.AgentHost = implant.Hostname
				evt.AgentIP = implant.IP
				evt.AgentOS = implant.OS
				evt.User = implant.Username
			}
		}
		// Tenant gate: a rule may only fire for events attributed to its
		// own tenant's agents. Agent-less schedule rules fire only when
		// legacy (tenant 0) — they cannot be attributed to any tenant.
		if !s.ruleMayFireOn(rule.TenantID, evt.AgentID) {
			continue
		}
		s.evaluateRule(evt, rule)

		rule.LastRun = now
		rule.RunCount++
		computed, err := nextScheduleTime(rule.Schedule, now)
		stateUpdates := map[string]interface{}{
			"last_run":  now,
			"run_count": rule.RunCount,
			"next_run":  computed,
		}
		if err != nil {
			slog.Warn("Automation: schedule broken after run, disabling rule", "rule", rule.Name, "error", err)
			rule.Enabled = false
			stateUpdates["enabled"] = false
			stateUpdates["next_run"] = time.Time{}
		}
		s.persistScheduleState(rule.ID, stateUpdates)
	}
}

// nextScheduleTime computes the next occurrence of schedule after `from`.
// Supported formats (case-insensitive):
//
//	"every N minutes" | "every N hours" | "hourly" | "daily HH:MM" |
//	"weekly DOW HH:MM" (DOW: mon..sun) | 5-field cron "m h dom mon dow"
func nextScheduleTime(schedule string, from time.Time) (time.Time, error) {
	s := strings.ToLower(strings.TrimSpace(schedule))
	if s == "" {
		return time.Time{}, fmt.Errorf("empty schedule")
	}

	if strings.HasPrefix(s, "every ") {
		var n int
		var unit string
		if _, err := fmt.Sscanf(strings.TrimPrefix(s, "every "), "%d %s", &n, &unit); err != nil {
			return time.Time{}, fmt.Errorf("invalid interval: %q", schedule)
		}
		unit = strings.TrimSuffix(unit, "s")
		switch unit {
		case "minute", "min":
			if n < 1 {
				return time.Time{}, fmt.Errorf("interval must be >= 1: %q", schedule)
			}
			return from.Add(time.Duration(n) * time.Minute), nil
		case "hour":
			if n < 1 {
				return time.Time{}, fmt.Errorf("interval must be >= 1: %q", schedule)
			}
			return from.Add(time.Duration(n) * time.Hour), nil
		default:
			return time.Time{}, fmt.Errorf("unsupported interval unit: %q", unit)
		}
	}

	if s == "hourly" {
		return from.Add(time.Hour), nil
	}

	if strings.HasPrefix(s, "daily ") {
		hhmm := strings.TrimSpace(strings.TrimPrefix(s, "daily "))
		next, err := nextDailyTime(hhmm, from)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid daily schedule: %q", schedule)
		}
		return next, nil
	}

	if strings.HasPrefix(s, "weekly ") {
		rest := strings.TrimSpace(strings.TrimPrefix(s, "weekly "))
		parts := strings.Fields(rest)
		if len(parts) != 2 {
			return time.Time{}, fmt.Errorf("weekly schedule needs 'DOW HH:MM': %q", schedule)
		}
		dow, ok := weekdayIndex(parts[0])
		if !ok {
			return time.Time{}, fmt.Errorf("unknown weekday: %q", parts[0])
		}
		next, err := nextDailyTime(parts[1], from)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid weekly schedule: %q", schedule)
		}
		for int(next.Weekday()) != dow {
			next = next.Add(24 * time.Hour)
		}
		return next, nil
	}

	// Fallback: standard 5-field cron expression.
	next, err := nextCronTime(s, from)
	if err != nil {
		return time.Time{}, fmt.Errorf("unsupported schedule: %q", schedule)
	}
	return next, nil
}

func nextDailyTime(hhmm string, from time.Time) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("invalid time %q", hhmm)
	}
	candidate := time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, from.Location())
	if !candidate.After(from) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate, nil
}

func weekdayIndex(dow string) (int, bool) {
	names := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	for i, n := range names {
		if dow == n {
			return i, true
		}
	}
	return 0, false
}

type cronField struct {
	raw     string
	allowed map[int]bool
	step    int
}

func parseCronField(raw string, min, max int) (cronField, error) {
	f := cronField{raw: raw, step: 1, allowed: map[int]bool{}}
	if raw == "*" {
		for v := min; v <= max; v++ {
			f.allowed[v] = true
		}
		return f, nil
	}
	// step suffix */n or a-b/n
	step := 1
	stepPart := ""
	if idx := strings.IndexByte(raw, '/'); idx >= 0 {
		stepPart = raw[idx+1:]
		raw = raw[:idx]
		fmt.Sscanf(stepPart, "%d", &step)
		if step < 1 {
			step = 1
		}
		f.step = step
	}
	expand := func(from, to int) {
		for v := from; v <= to; v += step {
			f.allowed[v] = true
		}
	}
	for _, part := range strings.Split(raw, ",") {
		if part == "*" {
			for v := min; v <= max; v += step {
				f.allowed[v] = true
			}
			continue
		}
		if idx := strings.IndexByte(part, '-'); idx >= 0 {
			var a, b int
			if _, err := fmt.Sscanf(part, "%d-%d", &a, &b); err != nil || a < min || b > max || a > b {
				return f, fmt.Errorf("bad cron range %q", part)
			}
			expand(a, b)
			continue
		}
		var v int
		if _, err := fmt.Sscanf(part, "%d", &v); err != nil || v < min || v > max {
			return f, fmt.Errorf("bad cron value %q", part)
		}
		expand(v, v)
	}
	return f, nil
}

func cronMatches(field cronField, v int) bool {
	return field.allowed[v]
}

// expandWeekdayNames rewrites sun..sat to 0..6 inside a day-of-week cron
// field so the numeric parser can handle it.
func expandWeekdayNames(dow string) string {
	names := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	for i, n := range names {
		dow = strings.ReplaceAll(dow, n, fmt.Sprintf("%d", i))
	}
	return dow
}

func nextCronTime(expr string, from time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron must have 5 fields")
	}
	minuteF, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, err
	}
	hourF, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, err
	}
	domF, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, err
	}
	monF, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, err
	}
	dowF, err := parseCronField(expandWeekdayNames(fields[4]), 0, 6)
	if err != nil {
		return time.Time{}, err
	}

	t := from.Add(time.Minute).Truncate(time.Minute)
	// Standard cron semantics: if BOTH day-of-month and day-of-week are
	// restricted (non-`*`), a match on EITHER field satisfies the schedule.
	// Using AND here would make e.g. "0 9 15 * mon" never fire unless the
	// 15th happens to be a Monday.
	domWild := fields[2] == "*"
	dowWild := fields[4] == "*"
	for i := 0; i < 366*25; i++ {
		domOK := cronMatches(domF, t.Day())
		dowOK := cronMatches(dowF, int(t.Weekday()))
		dayOK := domOK
		if !domWild || !dowWild {
			dayOK = domOK || dowOK
		}
		if cronMatches(monF, int(t.Month())) && dayOK &&
			cronMatches(hourF, t.Hour()) && cronMatches(minuteF, t.Minute()) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("no cron match found")
}
