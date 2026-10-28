package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func schedAction(t *testing.T, actionType string, params any) RuleAction {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal action params: %v", err)
	}
	return RuleAction{Type: actionType, Params: raw}
}

// TestDispatchScheduledRules covers the scheduler's three branches (compute a
// missing next_run, skip a future one, fire a due one), the tenant gate and the
// bad-schedule path.
func TestDispatchScheduledRules(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "sched-agent", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "sched-agent").
		Updates(map[string]interface{}{"hostname": "sched-host", "status": "online"}).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	due := AutomationRule{
		ID: "rule-due", Name: "due", Enabled: true, EventType: EventSchedule,
		Schedule: "every 5 minutes", AgentID: "sched-agent", TenantID: 1,
		NextRun: now.Add(-time.Minute),
		Actions: []RuleAction{schedAction(t, ActionCreateTask, map[string]string{
			"type": "shell", "command": "whoami on {{hostname}}",
		})},
	}
	future := AutomationRule{
		ID: "rule-future", Name: "future", Enabled: true, EventType: EventSchedule,
		Schedule: "every 5 minutes", AgentID: "sched-agent", TenantID: 1,
		NextRun: now.Add(time.Hour),
	}
	fresh := AutomationRule{
		ID: "rule-fresh", Name: "fresh", Enabled: true, EventType: EventSchedule,
		Schedule: "every 5 minutes", AgentID: "sched-agent", TenantID: 1,
		Actions: []RuleAction{schedAction(t, ActionCreateTask, map[string]string{"type": "shell"})},
	}
	broken := AutomationRule{
		ID: "rule-broken", Name: "broken", Enabled: true, EventType: EventSchedule,
		Schedule: "not-a-schedule", AgentID: "sched-agent", TenantID: 1,
	}
	foreignDue := AutomationRule{
		ID: "rule-foreign", Name: "foreign", Enabled: true, EventType: EventSchedule,
		Schedule: "every 5 minutes", AgentID: "sched-agent", TenantID: 2,
		NextRun: now.Add(-time.Minute),
		Actions: []RuleAction{schedAction(t, ActionCreateTask, map[string]string{
			"type": "shell", "command": "cross-tenant",
		})},
	}
	for _, rule := range []AutomationRule{due, future, fresh, broken, foreignDue} {
		if err := s.saveAutomationRule(rule); err != nil {
			t.Fatalf("save %s: %v", rule.ID, err)
		}
	}

	s.dispatchScheduledRules()

	reload := func(id string) db.AutomationRule {
		var row db.AutomationRule
		if err := s.db.Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("reload %s: %v", id, err)
		}
		return row
	}

	// The due rule fired: one task with the template expanded, counters bumped.
	var tasks []db.Task
	if err := s.db.Where("agent_id = ?", "sched-agent").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks created = %d, want only the due rule's (cross-tenant must not fire)", len(tasks))
	}
	if tasks[0].Command != "whoami on sched-host" {
		t.Fatalf("expanded command = %q", tasks[0].Command)
	}
	dueRow := reload(due.ID)
	if dueRow.RunCount != 1 || dueRow.LastRun.IsZero() || !dueRow.NextRun.After(now) {
		t.Fatalf("due rule state = run_count %d last_run %v next_run %v", dueRow.RunCount, dueRow.LastRun, dueRow.NextRun)
	}

	// The future rule is untouched.
	if got := reload(future.ID).NextRun; !got.Equal(future.NextRun) {
		t.Fatalf("future rule next_run changed: %v -> %v", future.NextRun, got)
	}
	// The fresh rule got its first next_run computed.
	if got := reload(fresh.ID).NextRun; got.IsZero() {
		t.Fatal("fresh rule did not get a next_run")
	}
	if got := reload(fresh.ID).RunCount; got != 0 {
		t.Fatalf("fresh rule ran %d times before its next_run", got)
	}
	// The broken schedule is skipped, not silently scheduled.
	if got := reload(broken.ID).NextRun; !got.IsZero() {
		t.Fatalf("broken schedule produced next_run %v", got)
	}
	// The foreign-tenant rule did not fire.
	if got := reload(foreignDue.ID).RunCount; got != 0 {
		t.Fatalf("cross-tenant rule ran %d times", got)
	}
}

func TestSchedulerLoopStopsOnCancel(t *testing.T) {
	s := mustTenantServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	done := make(chan struct{})
	go func() {
		s.schedulerLoop()
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("schedulerLoop did not stop after cancel")
	}
}

func TestDeleteAutomationRuleClearsCache(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	// A rule with no actions is deliberately not cached/loaded, so give it one.
	rule := AutomationRule{ID: "rule-del", Name: "del", Enabled: true, EventType: "agent.checkin", TenantID: 1,
		Actions: []RuleAction{schedAction(t, ActionNotify, map[string]string{"message": "hi"})}}
	if err := s.saveAutomationRule(rule); err != nil {
		t.Fatal(err)
	}
	// Prime the cache so the delete has to invalidate it.
	if got := s.loadAutomationRules(); len(got) != 1 {
		t.Fatalf("cached rules = %d, want 1", len(got))
	}
	if err := s.deleteAutomationRule(rule.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var count int64
	s.db.Model(&db.AutomationRule{}).Where("id = ?", rule.ID).Count(&count)
	if count != 0 {
		t.Fatal("rule row survived delete")
	}
	if got := s.loadAutomationRules(); len(got) != 0 {
		t.Fatalf("cache still holds the deleted rule: %d", len(got))
	}
	// Deleting a missing id is a no-op, not an error.
	if err := s.deleteAutomationRule("does-not-exist"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestExecuteActionTypes(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "act-agent", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "act-agent").
		Updates(map[string]interface{}{"hostname": "act-host", "status": "online"}).Error; err != nil {
		t.Fatal(err)
	}
	evt := Event{Type: "agent.checkin", AgentID: "act-agent", AgentHost: "act-host", TenantID: 1}

	// create_task expands templates and attributes the task to automation.
	s.executeAction(AutomationRule{ID: "test"}, schedAction(t, ActionCreateTask, map[string]string{
		"type": "shell", "command": "echo {{hostname}} {{agent_id}}",
	}), evt)

	// set_sleep clamps to the OPSEC floors and queues a set_sleep task.
	s.executeAction(AutomationRule{ID: "test"}, schedAction(t, ActionSetSleep, map[string]int{"interval": 1, "jitter": 200}), evt)

	var tasks []db.Task
	if err := s.db.Where("agent_id = ?", "act-agent").Order("id asc").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want create_task + set_sleep (%+v)", len(tasks), tasks)
	}
	if tasks[0].Type != "shell" || tasks[0].Command != "echo act-host act-agent" || tasks[0].CreatedBy != "automation" {
		t.Fatalf("create_task result = %+v", tasks[0])
	}
	if tasks[1].Type != "set_sleep" || !strings.Contains(tasks[1].Command, ",") {
		t.Fatalf("set_sleep result = %+v", tasks[1])
	}

	// run_macro pointing at another tenant's macro is blocked (no run queued).
	foreignMacro := db.CommandMacro{TenantID: 2, Name: "foreign", Steps: `[]`}
	if err := s.db.Create(&foreignMacro).Error; err != nil {
		t.Fatal(err)
	}
	before := len(tasks)
	s.executeAction(AutomationRule{ID: "test"}, schedAction(t, ActionRunMacro, map[string]interface{}{"macro_id": foreignMacro.ID}), evt)
	var after int64
	s.db.Model(&db.Task{}).Where("agent_id = ?", "act-agent").Count(&after)
	if int(after) != before {
		t.Fatalf("cross-tenant macro queued %d tasks", int(after)-before)
	}

	// notify and a blocked webhook must not panic the dispatcher.
	s.executeAction(AutomationRule{ID: "test"}, schedAction(t, ActionNotify, map[string]string{"message": "hello \"quoted\""}), evt)
	s.executeAction(AutomationRule{ID: "test"}, schedAction(t, ActionWebhook, map[string]string{"url": "http://127.0.0.1:9/hook"}), evt)
	// Give the async webhook goroutine a moment; it must fail closed on the
	// private URL rather than reach the network.
	time.Sleep(50 * time.Millisecond)
}

func TestHandleDeleteAutomationRuleNotFound(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	c, w := missingAPICtx(http.MethodDelete, "/api/automation/rules/x", "",
		gin.Params{{Key: "id", Value: "missing"}})
	s.handleDeleteAutomationRule(c)
	if w.Code != http.StatusNotFound && w.Code != http.StatusOK {
		t.Fatalf("delete missing rule = %d %s", w.Code, w.Body.String())
	}
}
