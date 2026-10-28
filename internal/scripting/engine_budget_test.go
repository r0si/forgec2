package scripting

import (
	"strings"
	"testing"
	"time"
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

// TestSleepIsCappedAndBudgeted pins the second half of the bound. A Go-side sleep cannot be
// interrupted, so the old 60-second allowance kept the VM goroutine alive long after the caller was
// told the script had timed out at 30 seconds. Sleeps are now capped at 5s and draw on the read
// budget, so a loop that sleeps still terminates with an error instead of pinning the goroutine.
func TestSleepIsCappedAndBudgeted(t *testing.T) {
	engine := NewScriptEngine()
	bridge := &countingBridge{}
	engine.SetBridge(bridge)

	// A single oversized sleep is clamped, not honoured: measure it.
	start := time.Now()
	res := engine.ExecuteCode(`sleep(60000); "done";`, nil, Caller{Username: "op", Role: "admin"})
	elapsed := time.Since(start)
	if !res.Success {
		t.Fatalf("sleep script failed: %q", res.Error)
	}
	if elapsed > time.Duration(scriptMaxSleepMillis+2000)*time.Millisecond {
		t.Fatalf("sleep(60000) took %s; the cap is %dms", elapsed, scriptMaxSleepMillis)
	}

	// A sleep loop exhausts the read budget rather than running to the timeout.
	engine2 := NewScriptEngine()
	engine2.SetBridge(&countingBridge{})
	res = engine2.ExecuteCode(`for (var i = 0; i < 10000; i++) { sleep(1); } "never";`, nil,
		Caller{Username: "op", Role: "admin"})
	if res.Success {
		t.Fatal("a 10000-sleep loop reported success")
	}
	if !strings.Contains(res.Error, "bridge query limit") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

// TestQueryLoopIsBudgeted bounds the read side: a script looping over query pins the single
// writer connection and allocates a result set per iteration.
func TestQueryLoopIsBudgeted(t *testing.T) {
	engine := NewScriptEngine()
	bridge := &countingBridge{}
	engine.SetBridge(bridge)
	res := engine.ExecuteCode(`for (var i = 0; i < 20000; i++) { query("agents", {}); } "never";`, nil,
		Caller{Username: "op", Role: "admin"})
	if res.Success {
		t.Fatal("a 20000-query loop reported success")
	}
	if !strings.Contains(res.Error, "bridge query limit") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}
