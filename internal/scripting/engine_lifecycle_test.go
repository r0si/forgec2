package scripting

import (
	"strings"
	"testing"
)

// TestToInt64 covers the numeric coercion used by the scripting bridge when it
// reads a value that may arrive as int, int64 or float64 (JSON numbers).
func TestToInt64(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int64
		ok   bool
	}{
		{int64(42), 42, true},
		{int(7), 7, true},
		{float64(3.9), 3, true},
		{float64(-2.5), -2, true},
		{int64(0), 0, true},
		{"42", 0, false},
		{nil, 0, false},
		{true, 0, false},
		{[]int{1}, 0, false},
	}
	for _, tc := range cases {
		got, gotOK := toInt64(tc.in)
		if gotOK != tc.ok || got != tc.want {
			t.Errorf("toInt64(%#v) = (%d, %v), want (%d, %v)", tc.in, got, gotOK, tc.want, tc.ok)
		}
	}
}

// TestScriptLifecycle covers load/execute/unload: a loaded script runs and sees
// its context, reloading by name replaces the code, and unloading removes it so
// Execute reports "script not found" instead of running stale code.
func TestScriptLifecycle(t *testing.T) {
	e := newTestEngine(t)

	if err := e.LoadScript(1, "hello", `"hello " + name`); err != nil {
		t.Fatalf("load: %v", err)
	}
	res := e.Execute("hello", map[string]interface{}{"name": "world"}, Caller{Username: "admin1", Role: "admin"})
	if !res.Success {
		t.Fatalf("execute: %s", res.Error)
	}
	if !strings.Contains(res.Output, "hello world") {
		t.Fatalf("output = %q", res.Output)
	}

	// Reloading by name replaces the code (no duplicate script).
	if err := e.LoadScript(1, "hello", `"reloaded"`); err != nil {
		t.Fatalf("reload: %v", err)
	}
	res = e.Execute("hello", nil, Caller{Username: "admin1", Role: "admin"})
	if !strings.Contains(res.Output, "reloaded") {
		t.Fatalf("reload did not replace the code: %q", res.Output)
	}

	// Unload removes it.
	e.UnloadScript("hello")
	res = e.Execute("hello", nil, Caller{Username: "admin1", Role: "admin"})
	if res.Success || res.Error != "script not found" {
		t.Fatalf("execute after unload = %+v", res)
	}

	// Unloading an unknown name is harmless.
	e.UnloadScript("never-loaded")
}

// TestExecuteRejectsInvalidSource pins the guard in front of every run: code
// that fails validation never reaches the VM.
func TestExecuteRejectsInvalidSource(t *testing.T) {
	e := newTestEngine(t)
	res := e.ExecuteCode(`this is not javascript {{{`, nil, Caller{Username: "admin1", Role: "admin"})
	if res.Success {
		t.Fatal("invalid source was executed")
	}
	if res.Error == "" {
		t.Fatal("no error was reported")
	}
	// And LoadScript refuses it too, leaving nothing loaded.
	if err := e.LoadScript(9, "bad", `this is not javascript {{{`); err == nil {
		t.Fatal("LoadScript accepted invalid source")
	}
	if res := e.Execute("bad", nil, Caller{Username: "admin1", Role: "admin"}); res.Success {
		t.Fatal("a rejected script was still loadable")
	}
}

// TestExecuteReportsRuntimeError pins that a script that throws is reported as a
// failure with the error text rather than a silent success.
func TestExecuteReportsRuntimeError(t *testing.T) {
	e := newTestEngine(t)
	res := e.ExecuteCode(`throw new Error("boom from script")`, nil, Caller{Username: "admin1", Role: "admin"})
	if res.Success {
		t.Fatal("a throwing script reported success")
	}
	if !strings.Contains(res.Error, "boom from script") {
		t.Fatalf("error = %q", res.Error)
	}
}

// TestExecuteCodeSeesContext pins that the per-run context is visible to the
// script as ctx.
func TestExecuteCodeSeesContext(t *testing.T) {
	e := newTestEngine(t)
	res := e.ExecuteCode(`"agent=" + agent_id + " n=" + n`,
		map[string]interface{}{"agent_id": "abc", "n": 3}, Caller{Username: "admin1", Role: "admin"})
	if !res.Success {
		t.Fatalf("execute: %s", res.Error)
	}
	if !strings.Contains(res.Output, "agent=abc") || !strings.Contains(res.Output, "n=3") {
		t.Fatalf("output = %q", res.Output)
	}

	// No context at all must not panic the VM.
	if res := e.ExecuteCode(`"ok"`, nil, Caller{Username: "admin1", Role: "admin"}); !res.Success {
		t.Fatalf("nil context: %s", res.Error)
	}
}

// newTestEngine returns an engine with no bridge, so scripts cannot reach the
// server. GetEngine would hand back the process-wide singleton.
func newTestEngine(t *testing.T) *ScriptEngine {
	t.Helper()
	return NewScriptEngine()
}
