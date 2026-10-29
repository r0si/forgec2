package server

import (
	"strings"
	"testing"
)

// TestConditionMatchingFailsClosed covers the trap that made a misspelled rule fire on everything.
//
// The old runtime default returned TRUE for an unrecognised operator, and an empty field resolved to "" so `contains` matched every event.
// Combined with the new `build.failed` event, a typo (`equal`, `==`, `startsWith`) meant a notification for every build in the deployment -
// and the operator would reasonably conclude the rule works.
func TestConditionMatchingFailsClosed(t *testing.T) {
	s := &Server{} // matchCondition reads no state
	evt := Event{
		Type:      EventBuildFailed,
		AgentHost: "web-01",
		Data: map[string]interface{}{
			"hint_code": "compile_error",
			"platform":  "windows",
		},
	}

	// 1. The intended match still works, on both a field the resolver knows and one from the event payload.
	matches := []RuleCondition{
		{Field: "agent.hostname", Operator: "equals", Value: "web-01"},
		{Field: "hint_code", Operator: "equals", Value: "compile_error"},
		{Field: "platform", Operator: "contains", Value: "win"},
	}
	for _, condition := range matches {
		if !s.matchCondition(condition, evt) {
			t.Fatalf("%+v should have matched", condition)
		}
	}

	// 2. A misspelled operator matches NOTHING. This is the regression: it used to match everything.
	for _, operator := range []string{"equal", "==", "startswith", "StartWith", "not_equals", "  "} {
		condition := RuleCondition{Field: "hint_code", Operator: operator, Value: "compile_error"}
		if s.matchCondition(condition, evt) {
			t.Fatalf("operator %q matched: an unrecognised operator must fail closed", operator)
		}
	}

	// 3. An empty field matches nothing, for the same reason.
	if s.matchCondition(RuleCondition{Field: "", Operator: "contains", Value: ""}, evt) {
		t.Fatal("an empty field matched")
	}
	// Whitespace-only counts as empty.
	if s.matchCondition(RuleCondition{Field: "   ", Operator: "contains", Value: "x"}, evt) {
		t.Fatal("a whitespace-only field matched")
	}

	// 4. A field the event does not carry does not accidentally match an empty value.
	if s.matchCondition(RuleCondition{Field: "missing_key", Operator: "contains", Value: ""}, evt) {
		t.Fatal("a missing key matched an empty value")
	}
}

// TestValidateRuleConditions covers the save-time half: the same defects are refused while the operator is looking at the rule.
func TestValidateRuleConditions(t *testing.T) {
	// Valid definitions pass, including an empty condition list (a rule that fires on every event OF ITS TYPE, deliberately).
	for _, conditions := range [][]RuleCondition{
		nil,
		{{Field: "agent.hostname", Operator: "contains", Value: "dc"}},
		{{Field: "hint_code", Operator: "equals", Value: "compile_error"}},
		{{Field: "hint_code", Operator: "EQUALS", Value: "compile_error"}}, // case-insensitive
	} {
		if err := validateRuleConditions(conditions); err != nil {
			t.Fatalf("%+v was refused: %v", conditions, err)
		}
	}

	// Invalid definitions are refused with a message that names the condition and the fix.
	cases := []struct {
		name       string
		conditions []RuleCondition
		wantIn     string
	}{
		{"no field", []RuleCondition{{Operator: "equals", Value: "x"}}, "no field"},
		{"no operator", []RuleCondition{{Field: "hint_code", Value: "x"}}, "no operator"},
		{"unknown operator", []RuleCondition{{Field: "hint_code", Operator: "equal", Value: "x"}}, "unknown operator"},
		{"second condition is the bad one", []RuleCondition{
			{Field: "platform", Operator: "equals", Value: "windows"},
			{Field: "hint_code", Operator: "===", Value: "x"},
		}, "condition 2"},
	}
	for _, testCase := range cases {
		err := validateRuleConditions(testCase.conditions)
		if err == nil {
			t.Fatalf("%s: accepted", testCase.name)
		}
		if !strings.Contains(err.Error(), testCase.wantIn) {
			t.Fatalf("%s: message %q does not mention %q", testCase.name, err.Error(), testCase.wantIn)
		}
	}
}
