package server

import (
	"os"
	"strings"
	"testing"
)

// TestAutomationRuleDocMatchesTheCode keeps docs/AUTOMATION_RULES.md true to the registry.
//
// The page is what an operator reads before writing a rule, and three of its lists are code constants: the event types, the operators, and
// the fail-closed inputs. A page that drifts from them is worse than none - it tells someone a rule will fire on an event that no longer
// exists, or that an operator is supported when it is refused at save time.
func TestAutomationRuleDocMatchesTheCode(t *testing.T) {
	raw, err := os.ReadFile("../../docs/AUTOMATION_RULES.md")
	if err != nil {
		t.Fatalf("the automation page is missing: %v", err)
	}
	doc := string(raw)

	// 1. Every event type the registry declares is listed.
	events := []EventType{
		EventImplantCheckin, EventImplantDisconnect, EventTaskComplete, EventTaskFail,
		EventCredentialFound, EventAlertTriggered, EventBuildFailed, EventBuildSucceeded,
	}
	for _, event := range events {
		if !strings.Contains(doc, "`"+string(event)+"`") {
			t.Fatalf("the page does not list the %q event", event)
		}
	}

	// 2. Every operator the matcher honours is listed, and nothing is listed that it refuses.
	for operator := range allowedConditionOperators {
		if !strings.Contains(doc, "`"+operator+"`") {
			t.Fatalf("the page does not document the %q operator", operator)
		}
	}
	for _, refused := range []string{"`equal`", "`==`", "`startswith`"} {
		if !strings.Contains(doc, refused) {
			t.Fatalf("the page does not name %s among the refused spellings", refused)
		}
	}

	// 3. The payload fields the build events carry are documented, because a condition is written against them.
	for _, field := range []string{"hint_code", "`data.", "platform"} {
		if !strings.Contains(doc, field) {
			t.Fatalf("the page does not document %q", field)
		}
	}

	// 4. The fail-closed rule and the three surfaces that catch an unfirable rule are stated, since they are the point of the page.
	// "fails closed" in the present tense: it is the rule, not an incident that happened once.
	for _, needle := range []string{"invalid_condition", "can never match", "fails closed"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the page does not state %q", needle)
		}
	}
}
