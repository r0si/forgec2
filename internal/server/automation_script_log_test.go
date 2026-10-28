package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestAutomationScriptFailureIsLogged pins the fix for a silent-failure shape: the ActionRunScript
// branch threw away the ExecutionResult, so a script that failed on every event (a typo, a
// permission the automation role lacks, a timeout) left no trace at all - the rule "ran", the event
// was consumed, and the only symptom was whatever the script was supposed to do not happening.
func TestAutomationScriptFailureIsLogged(t *testing.T) {
	s := mustTenantServer(t)

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	action := RuleAction{
		Type:   ActionRunScript,
		Params: []byte(`{"code":"throw new Error('boom')"}`),
	}
	s.executeAction(action, Event{Type: EventImplantCheckin, AgentID: "a-1", TenantID: 1})

	out := logs.String()
	if !strings.Contains(out, "script failed") {
		t.Fatalf("a failing automation script left no log line; got %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("the log line does not carry the script error: %q", out)
	}
	if !strings.Contains(out, "a-1") {
		t.Fatalf("the log line does not identify the agent: %q", out)
	}

	// A rule that cannot act must say so too: skipping used to be indistinguishable from a rule
	// that never matched.
	logs.Reset()
	s.executeAction(RuleAction{Type: ActionCreateTask, Params: []byte(`{"type":"shell","command":"whoami"}`)},
		Event{Type: EventImplantCheckin, TenantID: 1})
	if !strings.Contains(logs.String(), "create_task skipped") {
		t.Fatalf("a create_task action with no target was skipped silently: %q", logs.String())
	}

	logs.Reset()
	s.executeAction(RuleAction{Type: ActionSetSleep, Params: []byte(`{"agent_id":"a-1","interval":0}`)},
		Event{Type: EventImplantCheckin, AgentID: "a-1", TenantID: 1})
	if !strings.Contains(logs.String(), "set_sleep skipped") {
		t.Fatalf("a set_sleep action with interval 0 was skipped silently: %q", logs.String())
	}

	// A successful run stays at debug level, so routine automation does not spam the log.
	logs.Reset()
	s.executeAction(RuleAction{Type: ActionRunScript, Params: []byte(`{"code":"1 + 1"}`)},
		Event{Type: EventImplantCheckin, AgentID: "a-1", TenantID: 1})
	if !strings.Contains(logs.String(), "script ran") {
		t.Fatalf("a successful automation script was not logged at debug: %q", logs.String())
	}
}
