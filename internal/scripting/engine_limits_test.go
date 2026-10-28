package scripting

import (
	"strings"
	"testing"
)

// TestRunawayRecursionIsACatchableError pins the depth bound. goja ships maxCallStackSize =
// math.MaxInt32 (no limit), so an unbounded-recursion script ran until the 30-second interrupt —
// measured: the unfixed run failed with "script execution timeout (30s)" after 30.0s, holding a
// core and growing the stack the whole time. With the bound it fails IMMEDIATELY with a JS error.
// I expected a fatal Go stack overflow and could not reproduce one (Go grows goroutine stacks), so
// the fix is documented as what it is: a latency/CPU bound, not a crash fix.
func TestRunawayRecursionIsACatchableError(t *testing.T) {
	engine := NewScriptEngine()

	// Before the stack bound this killed the test binary outright; now it is a JS error.
	res := engine.ExecuteCode(`function f(){ return f(); } f();`, nil, Caller{})
	if res.Success {
		t.Fatal("runaway recursion reported success")
	}
	if res.Error == "" {
		t.Fatal("runaway recursion produced no error")
	}
	// goja's StackOverflowError renders as a bare position trace (" at f (<eval>:1:23(2))"),
	// so the assertion is on the outcome — an error, not a dead process — rather than on wording.
	if !strings.HasPrefix(res.Error, " at ") {
		t.Fatalf("unexpected error for runaway recursion: %q", res.Error)
	}

	// A bounded recursion still works, so the limit is not so tight that normal scripts break.
	res = engine.ExecuteCode(`function f(n){ return n <= 0 ? 0 : f(n-1) + 1; } f(200);`, nil, Caller{})
	if !res.Success || res.Output != "200" {
		t.Fatalf("bounded recursion failed: success=%v output=%q error=%q", res.Success, res.Output, res.Error)
	}
}

// TestHugeScriptOutputIsTruncated keeps a script from pushing an arbitrarily large body into an
// HTTP response: the result string is capped, and the cap is marked.
func TestHugeScriptOutputIsTruncated(t *testing.T) {
	engine := NewScriptEngine()
	res := engine.ExecuteCode(`"x".repeat(4 * 1024 * 1024)`, nil, Caller{})
	if !res.Success {
		t.Fatalf("output script failed: %q", res.Error)
	}
	if len(res.Output) > scriptMaxOutputBytes+64 {
		t.Fatalf("output is %d bytes, cap is %d", len(res.Output), scriptMaxOutputBytes)
	}
	if !strings.Contains(res.Output, "[output truncated]") {
		t.Fatal("the truncation marker is missing")
	}
}
