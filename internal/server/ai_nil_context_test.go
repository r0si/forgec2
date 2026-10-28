package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNilAIToolContextStaysOutOfProduction makes an invariant executable that is currently only true
// by accident.
//
// authorizeAITool starts with:
//
//	if reqCtx == nil || reqCtx.Principal.UserID == 0 { return true, "" }
//
// i.e. a zero principal is ALLOWED - no policy lookup, no permission check, no approval intent. Every
// other scoping helper in this codebase fails closed instead (tenantScope, resolveTenant,
// lookupTenantID), which is why this one is a trap: a new background AI job or a "quick" call site
// passing nil would get unrestricted tool execution, including execute_command and delete_listener.
//
// The tests need the escape hatch (11 call sites in _test.go files), so rather than flip the branch and
// break them, this test pins the property that makes it safe today: no production file may reach the
// tool executor with a nil context.
func TestNilAIToolContextStaysOutOfProduction(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	// The wrapper that supplies the nil context, and the two direct entry points.
	nilCallers := []*regexp.Regexp{
		regexp.MustCompile(`\.executeToolSwitch\(`),
		regexp.MustCompile(`executeToolSwitchCtx\(\s*nil\s*,`),
		regexp.MustCompile(`executeToolCtx\(\s*nil\s*,`),
	}

	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, pattern := range nilCallers {
			for _, m := range pattern.FindAllIndex(source, -1) {
				line := 1 + strings.Count(string(source[:m[0]]), "\n")
				// The sanctioned wrapper (executeToolSwitch) DOES supply nil - that is its whole body.
				// Skip matches inside it by looking back for its signature.
				lines := strings.Split(string(source[:m[0]]), "\n")
				if len(lines) >= 2 && strings.Contains(lines[len(lines)-2], "func (s *Server) executeToolSwitch(") {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d", name, line))
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("production code reaches the AI tool executor without a principal, which "+
			"authorizeAITool treats as ALLOWED:\n  %s\n"+
			"Fix: build an aiReqCtx from the caller (or add an explicit system-principal path) instead "+
			"of relying on the fail-open branch.",
			strings.Join(offenders, "\n  "))
	}

	// Guard the guard: the branch is only safe while the tests are its only users.
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			continue
		}
		if strings.Contains(string(source), ".executeToolSwitch(") {
			found = true
			break
		}
	}
	if !found {
		t.Log("no test uses the nil-context helper any more; consider deleting it and making the " +
			"zero-principal branch fail closed")
	}
}
