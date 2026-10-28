package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// TestAIToolSwitchAndPolicyAgree keeps the two halves of the AI tool surface in step.
//
// A tool exists in two places: a `case` in executeToolSwitchCtx (it can do something) and an entry in
// aiToolPolicies (what it is allowed to do, and at what risk). Both directions are bugs:
//   - a case with no policy is REFUSED at runtime ("tool is not available"), so the feature silently
//     does nothing for everyone;
//   - a policy with no case is advertised to the model, gets approved, and then answers "unknown
//     tool" - the worst version, because a human approved it.
//
// The switch is source text and the policies are a map, so this reads the file and compares.
func TestAIToolSwitchAndPolicyAgree(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("ai_tools_exec.go"))
	if err != nil {
		t.Fatalf("read ai_tools_exec.go: %v", err)
	}
	casePattern := regexp.MustCompile(`(?m)^\tcase "([a-z_]+)":`)
	implemented := make(map[string]bool)
	for _, m := range casePattern.FindAllStringSubmatch(string(source), -1) {
		implemented[m[1]] = true
	}
	// Some tools are dispatched BEFORE the switch by a dedicated function (the knowledge search and
	// the web search, which need the run context); they are implemented, just not there.
	dispatcher, err := os.ReadFile(filepath.Join("ai_tools_defs.go"))
	if err != nil {
		t.Fatalf("read ai_tools_defs.go: %v", err)
	}
	earlyPattern := regexp.MustCompile(`if name == "([a-z_]+)" \{`)
	for _, m := range earlyPattern.FindAllStringSubmatch(string(dispatcher), -1) {
		implemented[m[1]] = true
	}
	if len(implemented) == 0 {
		t.Fatal("no tool cases found; the pattern no longer matches the switch")
	}

	var noPolicy, noCase []string
	for name := range implemented {
		if _, ok := aiToolPolicies[name]; !ok {
			noPolicy = append(noPolicy, name)
		}
	}
	for name := range aiToolPolicies {
		if !implemented[name] {
			noCase = append(noCase, name)
		}
	}
	sort.Strings(noPolicy)
	sort.Strings(noCase)

	if len(noPolicy) > 0 {
		t.Errorf("%d implemented tool(s) have no policy entry, so the assistant can never call them:\n  %v",
			len(noPolicy), noPolicy)
	}
	if len(noCase) > 0 {
		t.Errorf("%d policy entr(ies) have no implementation, so an approved intent would answer \"unknown tool\":\n  %v",
			len(noCase), noCase)
	}
}

// TestEveryNonReadAIToolRequiresAPermission is the security half: a tool that is not read-only must
// name the permission that gates it, and everything except a read must carry a risk level that sends
// the call through the approval intent (authorizeAITool only lets read-only and opted-in low-risk
// writes through directly).
func TestEveryNonReadAIToolRequiresAPermission(t *testing.T) {
	for name, policy := range aiToolPolicies {
		if policy.Permission == "" {
			t.Errorf("%s has no permission; authorizeAITool would call hasPermission(\"\")", name)
		}
		switch policy.Risk {
		case aiToolRiskRead:
		case aiToolRiskLowRiskWrite, aiToolRiskWrite, aiToolRiskSensitive, aiToolRiskDestructive:
			if policy.Permission == "" {
				t.Errorf("%s is a write but names no permission", name)
			}
		default:
			t.Errorf("%s has unknown risk %q; the approval decision would fall through to the intent path", name, policy.Risk)
		}
	}
}
