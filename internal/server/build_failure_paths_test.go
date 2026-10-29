package server

import (
	"os"
	"strings"
	"testing"
)

// TestBuildFailurePathsUseBuildFailureText is the guard for the class of defect, not one instance of it.
//
// The bug was not "completeBuildJob sanitized an error": it was **a build error being routed through a helper whose purpose is to
// replace unrecognised errors with a fallback sentence**. Fixing one call site and leaving the next one to be found by a user is how
// this kind of thing comes back, so this test walks every handler file that can fail a build and requires those failure responses to
// use buildFailureText - and, in the other direction, requires the fallback-sentence helper not to appear on a line that reports a
// build.
func TestBuildFailurePathsUseBuildFailureText(t *testing.T) {
	// Files that can fail a build and answer the operator in the same request.
	buildReportingFiles := []string{
		"build_jobs.go",
		"handlers_generate_stagers.go",
		"handlers_generate_shellcode.go",
	}

	for _, name := range buildReportingFiles {
		source := readServerFile(t, name)
		if !strings.Contains(source, "buildFailureText(") {
			t.Fatalf("%s reports a build failure but never calls buildFailureText", name)
		}
		// A line that both sanitizes AND mentions a build is the defect this guard exists for. Config/setup errors are allowed to use
		// the sanitizer - they can name key material - so the check is on the combination, not on the helper itself.
		for lineNumber, line := range strings.Split(source, "\n") {
			lowered := strings.ToLower(line)
			if strings.Contains(line, "sanitizeError(") && (strings.Contains(lowered, "build failed") ||
				strings.Contains(lowered, "stager generation") || strings.Contains(lowered, "shellcode generation")) {
				t.Fatalf("%s:%d routes a build failure through the fallback-sentence helper: %s", name, lineNumber+1, strings.TrimSpace(line))
			}
		}
	}

	// And the behaviour those files rely on, pinned where a reader will find it.
	failure := buildFailureText(errForGuard("go build failed: exit status 1\nundefined: ResolveNtSyscalls"))
	if !strings.Contains(failure, "undefined: ResolveNtSyscalls") {
		t.Fatalf("the shared helper no longer keeps compiler detail: %q", failure)
	}
}

// errForGuard is a tiny error type so this test does not depend on fmt.Errorf formatting rules.
type errForGuard string

func (e errForGuard) Error() string { return string(e) }

// readServerFile reads one file from this package's directory.
func readServerFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
