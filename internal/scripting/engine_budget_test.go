package scripting

import (
	"strings"
	"testing"
)

// countingBridge counts SendTask calls so the budget can be observed from the outside.
type countingBridge struct {
	fakeBridge
	sent int
}

func (c *countingBridge) SendTask(caller Caller, agentID, taskType, params string) (uint64, error) {
	c.sent++
	return c.fakeBridge.SendTask(caller, agentID, taskType, params)
}

// TestSendTaskLoopIsBudgeted pins the write-amplification bound: every sendTask inserts a task row
// AND an audit row on a single-writer database, so a script looping over it - or an event-bound
// script firing on every matching event - used to fill the table until the 30-second timeout.
func TestSendTaskLoopIsBudgeted(t *testing.T) {
	engine := NewScriptEngine()
	bridge := &countingBridge{}
	engine.SetBridge(bridge)

	res := engine.ExecuteCode(
		`var n = 0; for (var i = 0; i < 2000; i++) { sendTask("agent-1", "shell", "whoami"); n++; } n;`,
		nil, Caller{Username: "op", Role: "admin"})
	if res.Success {
		t.Fatalf("a 2000-call sendTask loop reported success: %q", res.Output)
	}
	if !strings.Contains(res.Error, "mutating bridge call limit") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	if bridge.sent != scriptMaxBridgeMutations {
		t.Fatalf("the bridge saw %d calls, budget is %d", bridge.sent, scriptMaxBridgeMutations)
	}

	// A normal handful of calls still works.
	engine2 := NewScriptEngine()
	bridge2 := &countingBridge{}
	engine2.SetBridge(bridge2)
	res = engine2.ExecuteCode(`for (var i = 0; i < 3; i++) { sendTask("agent-1", "shell", "whoami"); } "ok";`, nil,
		Caller{Username: "op", Role: "admin"})
	if !res.Success || bridge2.sent != 3 {
		t.Fatalf("a 3-call script failed: success=%v sent=%d error=%q", res.Success, bridge2.sent, res.Error)
	}
}
