package scripting

import (
	"strings"
	"testing"
)

// TestScriptVMSurfaceHasNoHostReferences pins the shape of the sandbox from the
// script's point of view. The capability bridge is meant to be the ONLY way out, so
// the VM must not expose the host's module/process globals - and it must not expose
// a way to reach them through the constructor trick that works in Node.
func TestScriptVMSurfaceHasNoHostReferences(t *testing.T) {
	e := NewScriptEngine()
	caller := Caller{Username: "tester", Role: "user"}

	absent := []string{
		"process", "module", "exports", "global", "globalThis.process",
		"Deno", "Bun", "window", "document", "navigator",
	}
	for _, name := range absent {
		code := `typeof ` + name + ` === "undefined" ? "absent" : "PRESENT: " + typeof ` + name
		res := e.ExecuteCode(code, nil, caller)
		if !res.Success {
			// A ReferenceError for an unknown identifier is also acceptable.
			if strings.Contains(res.Error, "is not defined") {
				continue
			}
			t.Fatalf("%s: probe failed for an unexpected reason: %v", name, res.Error)
		}
		if res.Output != "absent" {
			t.Fatalf("%s leaked into the script VM: %s", name, res.Output)
		}
	}

	// The constructor escape (the classic Node sandbox break) must not find a host
	// process object either.
	res := e.ExecuteCode(`(function(){}).constructor("return typeof process")()`, nil, caller)
	if res.Success && res.Output != "undefined" {
		t.Fatalf("the constructor escape reached the host: %q", res.Output)
	}

	// require exists only as the restricted registry loader: a builtin that is not
	// a Go core module must not resolve.
	if res := e.ExecuteCode(`require("child_process")`, nil, caller); res.Success {
		t.Fatalf("require reached a host module: %q", res.Output)
	}
}

// Note on runaway scripts: unbounded recursion does NOT raise a stack error in
// this VM - it keeps running until executeScript's 30-second interrupt fires (a
// measured run spent the full budget and returned "script execution timeout").
// That interrupt is the real bound, which is why it must stay in place; a test for
// it would cost 30 seconds of CI time, so the behaviour is recorded here instead.
