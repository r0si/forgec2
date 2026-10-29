package server

import (
	"errors"
	"strings"
	"testing"
)

// TestBuildFailureText covers how a build failure reaches the operator.
//
// The defect this replaces: `sanitizeError(err, "Build failed")` replaced every unrecognised error with the fallback, so a compiler
// failure arrived as the literal text "Build failed" and the UI showed it under its own "Build failed" title. A build report that
// names nothing cannot be acted on, which is worse than a verbose one.
func TestBuildFailureText(t *testing.T) {
	// 1. The compiler's own words survive, because they are the diagnosis.
	compiler := errors.New("go build failed: exit status 1\n./agent.go:42:2: undefined: ResolveNtSyscalls")
	got := buildFailureText(compiler)
	if !strings.Contains(got, "undefined: ResolveNtSyscalls") {
		t.Fatalf("the compiler detail was dropped: %q", got)
	}
	if got == "Build failed" {
		t.Fatal("the failure collapsed to the old generic sentence")
	}

	// 2. ldflags secrets are redacted: a build error can echo a whole command line, and the flags carry the config blob.
	leaky := errors.New(`go build failed: -X main.ConfigBlob=U2VjcmV0QmxvYkhlcmU= -X main.OtherVar=fine exit status 1`)
	redacted := buildFailureText(leaky)
	if strings.Contains(redacted, "U2VjcmV0QmxvYkhlcmU=") {
		t.Fatalf("a config blob survived in the failure text: %q", redacted)
	}
	if !strings.Contains(redacted, "main.OtherVar") {
		t.Fatalf("the flag name should stay readable: %q", redacted)
	}

	// 3. A bare key-shaped assignment is redacted too.
	if got := buildFailureText(errors.New("beacon_key=deadbeefcafe")); strings.Contains(got, "deadbeefcafe") {
		t.Fatalf("a bare key survived: %q", got)
	}

	// 4. A missing toolchain says what to DO, not only what failed.
	missing := buildFailureText(errors.New(`go build failed: exec: "go": executable file not found in %PATH%`))
	if !strings.Contains(missing, "hint:") || !strings.Contains(missing, "PATH") {
		t.Fatalf("the toolchain failure carries no hint: %q", missing)
	}

	// 5. A long failure keeps both ends: the first cause and the final status.
	// Comfortably over the bound: head + marker + tail + status, so the truncation actually has to run.
	long := strings.Repeat("x", 3000) + " MIDDLE-MARKER " + strings.Repeat("y", 3000) + " exit status 2"
	truncated := buildFailureText(errors.New(long))
	if !strings.Contains(truncated, "characters omitted") {
		t.Fatalf("a long failure was not bounded: %d chars", len(truncated))
	}
	if !strings.Contains(truncated, "exit status 2") {
		t.Fatalf("the tail (the exit status) was dropped: %q", truncated[len(truncated)-80:])
	}
	if len(truncated) > buildFailureMaxChars+200 {
		t.Fatalf("truncation did not bound the size: %d chars", len(truncated))
	}

	// 6. Degenerate inputs are named rather than shown as empty.
	if got := buildFailureText(nil); got != "" {
		t.Fatalf("nil error = %q, want empty", got)
	}
	if got := buildFailureText(errors.New("   ")); got == "" {
		t.Fatal("a blank error produced an empty message")
	}

	// 7. The toolchain detector agrees with the hint path.
	if !buildFailureIsToolchainMissing(errors.New(`exec: "go": executable file not found`)) {
		t.Fatal("a missing toolchain was not detected")
	}
	if buildFailureIsToolchainMissing(compiler) {
		t.Fatal("a compiler error was misread as a missing toolchain")
	}
}

// TestBuildFailureTextDoesNotLeakThroughSanitizer pins the specific regression: the two helpers must not agree on this input.
func TestBuildFailureTextDoesNotLeakThroughSanitizer(t *testing.T) {
	compiler := errors.New("go build failed: exit status 1\n./x.go:1:1: syntax error")
	// The old path: sanitizeError replaces it with the fallback, which is why the UI read "Build failed" twice.
	if sanitized := sanitizeError(compiler, "Build failed"); sanitized != "Build failed" {
		t.Skipf("sanitizeError now keeps build errors (%q); the regression test is obsolete", sanitized)
	}
	if buildFailureText(compiler) == "Build failed" {
		t.Fatal("buildFailureText fell back to the generic sentence")
	}
}

// TestBuildFailureHint covers the machine-readable next step.
//
// The error text is for a human and changes with every compiler version; the hint is a closed set of codes with one stable sentence each,
// so a console can offer an action without parsing prose - and a client can switch on it exhaustively.
func TestBuildFailureHint(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing toolchain", errors.New(`go build failed: exec: "go": executable file not found in %PATH%`), HintToolchainMissing},
		{"unwritable cache", errors.New(`go: creating work dir: mkdir C:\tmp\go-build123: permission denied`), HintCacheUnwritable},
		{"disk full", errors.New("write /tmp/a: no space left on device"), HintDiskFull},
		{"compiler rejected the source", errors.New("go build failed: exit status 1\n./agent.go:42:2: undefined: ResolveNtSyscalls"), HintCompileError},
		{"unclassifiable", errors.New("something nobody has seen before"), HintUnknown},
	}
	for _, testCase := range cases {
		hint := buildFailureHint(testCase.err)
		if hint.Code != testCase.want {
			t.Fatalf("%s: code = %q, want %q", testCase.name, hint.Code, testCase.want)
		}
		// Every code except "unknown" must carry the sentence that says what to do; "unknown" must NOT invent advice.
		if testCase.want == HintUnknown {
			if hint.Text != "" {
				t.Fatalf("%s: invented advice for an unclassifiable failure: %q", testCase.name, hint.Text)
			}
			continue
		}
		if len(hint.Text) < 20 {
			t.Fatalf("%s: hint text is too terse to act on: %q", testCase.name, hint.Text)
		}
	}

	// A long hint is bounded, so the status JSON cannot become a second copy of the log.
	long := hintOrEmpty(BuildFailureHint{Code: HintCompileError, Text: strings.Repeat("x", hintMaxChars*2)})
	if len(long.Text) != hintMaxChars {
		t.Fatalf("hint text = %d chars, want the bound %d", len(long.Text), hintMaxChars)
	}
	// nil is not a hint.
	if hint := buildFailureHint(nil); hint.Code != HintUnknown {
		t.Fatalf("nil error classified as %q", hint.Code)
	}
}
