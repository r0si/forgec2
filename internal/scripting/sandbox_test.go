package scripting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScriptRequireCannotReadHostFiles proves the script VM's require() cannot
// load or evaluate host modules. Before this, the default goja_nodejs source
// loader resolved relative/absolute paths, so a script could read server-side
// JS/JSON and step outside the capability bridge.
func TestScriptRequireCannotReadHostFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"token":"super-secret-value"}`), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	e := NewScriptEngine()
	caller := Caller{Username: "tester", Role: "user"}

	cases := []struct {
		name string
		code string
	}{
		{"absolute path", `require("` + filepath.ToSlash(secret) + `")`},
		{"relative traversal", `require("secret.json")`},
		{"bare builtin guess", `require("fs")`},
		{"node prefix", `require("node:fs")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := e.ExecuteCode(tc.code, nil, caller)
			if res.Success {
				t.Fatalf("require() of a host module succeeded: output=%q", res.Output)
			}
			// File-backed modules are refused by the deny-all loader; unknown
			// built-ins fail earlier in resolution. Either way nothing loads.
			refused := strings.Contains(res.Error, "host modules is disabled") ||
				strings.Contains(res.Error, "No such built-in module")
			if !refused {
				t.Fatalf("unexpected error (want a module refusal): %v", res.Error)
			}
			if strings.Contains(res.Error, "super-secret-value") {
				t.Fatal("fixture contents leaked into the script error")
			}
		})
	}
}

// TestScriptConsoleStillWorks guards the capability change: console is a Go
// core module and must keep resolving even though file-backed modules do not.
func TestScriptConsoleStillWorks(t *testing.T) {
	e := NewScriptEngine()
	res := e.ExecuteCode(`console.log("hello from script"); "done"`, nil, Caller{Username: "tester", Role: "user"})
	if !res.Success {
		t.Fatalf("console-enabled script failed: %v", res.Error)
	}
	if res.Output != "done" {
		t.Fatalf("output = %q, want %q", res.Output, "done")
	}
}

// TestScriptSourceLimit proves oversized scripts are refused before they reach
// the parser (memory/parse-time DoS on the in-process VM).
func TestScriptSourceLimit(t *testing.T) {
	e := NewScriptEngine()
	huge := "// " + strings.Repeat("x", scriptSourceLimit) + "\n"
	res := e.ExecuteCode(huge, nil, Caller{Username: "tester", Role: "user"})
	if res.Success {
		t.Fatal("oversized script was executed")
	}
	if !strings.Contains(res.Error, "script too large") {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	if err := e.LoadScript(1, "huge", huge); err == nil {
		t.Fatal("LoadScript accepted an oversized script")
	}
}

// TestLoadScriptDoesNotExecute pins the load contract: saving a script must
// only parse it. The previous implementation RAN the body in the reparse VM, so
//   - "while(true){}" wedged the save until the parse timeout,
//   - a top-level sendTask() fired a real task at save time,
//   - any reference to a per-run binding (agent_id, args, ...) failed with
//     ReferenceError, so most useful scripts could never be saved.
//
// A valid loop now loads instantly; a syntax error is still rejected.
func TestLoadScriptDoesNotExecute(t *testing.T) {
	if testing.Short() {
		t.Skip("timeout test")
	}
	e := NewScriptEngine()

	if err := e.LoadScript(1, "spin", "while(true){}"); err != nil {
		t.Fatalf("a syntactically valid script was rejected at load: %v", err)
	}
	// Loading must not have executed it: the engine still answers requests.
	if res := e.ExecuteCode(`"alive"`, nil, Caller{Username: "admin1", Role: "admin"}); !res.Success || res.Output != "alive" {
		t.Fatalf("the engine is wedged after loading a loop: %+v", res)
	}

	// Side effects at the top level are not run at load: a script that calls
	// sendTask would otherwise fire a task merely by being saved.
	e2 := NewScriptEngine()
	if err := e2.LoadScript(2, "sends", `sendTask("agent", "shell", "{}")`); err != nil {
		t.Fatalf("a side-effecting script was rejected at load: %v", err)
	}

	// Syntax errors are still caught, and nothing is added to the engine.
	if err := e2.LoadScript(3, "broken", "function ("); err == nil {
		t.Fatal("a syntax error was accepted")
	}
	if res := e2.Execute("broken", nil, Caller{Username: "admin1", Role: "admin"}); res.Success {
		t.Fatal("a script that failed to parse is executable")
	}
}
