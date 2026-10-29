package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryRuleHealthCodeIsDocumented keeps the rule-health vocabulary complete.
//
// The codes are what an operator filters on and what the console renders, and each one names a different failure of a rule -
// several of which are silent (a rule that never fires looks exactly like a rule that has not been triggered yet). A code that
// exists in the implementation and nowhere in the guide is a finding the reader cannot act on, so the check reads the source
// rather than a list somebody remembers to update.
func TestEveryRuleHealthCodeIsDocumented(t *testing.T) {
	source, err := os.ReadFile("automation_health.go")
	if err != nil {
		t.Fatalf("the health implementation is missing: %v", err)
	}
	doc, err := os.ReadFile("../../docs/DIAGNOSTICS.md")
	if err != nil {
		t.Fatalf("the diagnostics page is missing: %v", err)
	}

	// Two spellings: the helper the conditional findings use, and the struct literal the per-rule findings use.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`add\("(?:error|warning|info)", "([a-z_]+)"`),
		regexp.MustCompile(`Code: "([a-z_]+)"`),
	}
	codes := map[string]bool{}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			codes[match[1]] = true
		}
	}
	if len(codes) < 5 {
		t.Fatalf("only %d codes found; this guard is not reading the implementation", len(codes))
	}

	missing := make([]string, 0, len(codes))
	for code := range codes {
		if !strings.Contains(string(doc), "`"+code+"`") {
			missing = append(missing, code)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("docs/DIAGNOSTICS.md does not explain: %s", strings.Join(missing, ", "))
	}

	// And the page keeps the distinction that makes the severities usable.
	if !strings.Contains(string(doc), "what to do first") {
		t.Fatal("the page no longer explains what severity means")
	}
}
