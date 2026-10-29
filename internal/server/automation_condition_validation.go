package server

import (
	"fmt"
	"strings"
)

// Rule condition validation, and the reason it exists.
//
// `matchCondition` used to return TRUE for an operator it did not recognise, and TRUE for an empty field (because the value resolved to
// "" and `contains("", …)` is always true). Both defaults fail OPEN: a rule whose operator is misspelled - `equal` instead of `equals`,
// `==`, `startsWith` - matches EVERY event of its type. With `build.failed` on the bus, that typo is a notification for every build in the
// deployment, and the operator's conclusion is "the rule works, the builds are all failing".
//
// So the runtime default is false, and the definition is refused at save time. A rule is configuration an operator writes once and
// trusts; failing closed is the only defensible direction.

// allowedConditionOperators is the closed set a rule may use.
var allowedConditionOperators = map[string]bool{
	"contains": true,
	"equals":   true,
	// regex was expected by an existing test but never implemented: it fell through to the fail-open default, so the test passed while
	// the operator did nothing. It is implemented now, and an INVALID pattern fails closed like every other unrecognised input.
	"regex": true,
}

// validateRuleConditions rejects a definition that could never match, or that would match everything.
//
// Called on the save path and on the import path: a rule can arrive either way, and a check on one of them is a check on neither.
func validateRuleConditions(conditions []RuleCondition) error {
	for index, condition := range conditions {
		field := strings.TrimSpace(condition.Field)
		if field == "" {
			return fmt.Errorf("condition %d has no field: an empty field matches every event", index+1)
		}
		operator := strings.TrimSpace(condition.Operator)
		if operator == "" {
			return fmt.Errorf("condition %d (%s) has no operator: expected contains or equals", index+1, field)
		}
		if !allowedConditionOperators[strings.ToLower(operator)] {
			return fmt.Errorf("condition %d (%s) uses unknown operator %q: expected contains or equals - "+
				"an unrecognised operator would have matched every event", index+1, field, operator)
		}
	}
	return nil
}
