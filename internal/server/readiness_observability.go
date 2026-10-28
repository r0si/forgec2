package server

import (
	"fmt"
	"strconv"
	"sync"
)

// Delivery and action health, exposed for the readiness probe.
//
// A route nobody can reach does not make the server unready - the process is fine - but it is exactly the kind of thing a
// monitor should be able to alert on without scraping a UI. Both figures come from in-memory counters, so a frequent poll
// costs nothing.
//
// The threshold is "every delivery failed", not "anything failed": one failure after a hundred successes is a blip, and a
// warning that fires on a blip is one an operator mutes.

// notificationRouteFailingCount counts routes whose every recorded delivery has failed.
func notificationRouteFailingCount() int {
	count := 0
	for _, stat := range notificationRouteStatsSnapshot() {
		if notificationRouteHealth(stat) == "failing" {
			count++
		}
	}
	return count
}

// deliverySummary renders the delivery state for the checks map.
func deliverySummary(failing int) string {
	total := len(notificationRouteStatsSnapshot())
	if failing == 0 {
		return fmt.Sprintf("ok (%d route(s) with recorded deliveries)", total)
	}
	return fmt.Sprintf("%d of %d recorded route(s) failing", failing, total)
}

// automationActionFailingCount counts rules whose every action dispatch has failed.
func automationActionFailingCount() int {
	count := 0
	for _, stat := range ruleActionStatsSnapshot() {
		if ruleActionHealth(stat) == "failing" {
			count++
		}
	}
	return count
}

// Observability warnings are recorded once per episode, like the audit-chain tamper notice.
//
// A readiness probe is polled continuously, so writing an audit row per poll would bury the trail in its own alarm. What an
// investigator needs is the moment a route or a rule STARTED looking broken and when it recovered - so a warning is
// audited when the count rises and when it returns to zero, and silence in between.
var (
	observabilityWarningMu   sync.Mutex
	observabilityWarningLast = map[string]int{}
)

// noteObservabilityWarning audits a transition in a readiness warning count.
func (s *Server) noteObservabilityWarning(kind, description string, count int) {
	observabilityWarningMu.Lock()
	previous := observabilityWarningLast[kind]
	if previous == count {
		observabilityWarningMu.Unlock()
		return
	}
	observabilityWarningLast[kind] = count
	observabilityWarningMu.Unlock()

	switch {
	case count == 0:
		s.LogAuditRecord(nil, "observability_recovered", "system", kind,
			description+": recovered (was "+strconv.Itoa(previous)+")", true, nil)
	case previous == 0:
		s.LogAuditRecord(nil, "observability_warning", "system", kind,
			description+": "+strconv.Itoa(count)+" now failing", false, nil)
	default:
		s.LogAuditRecord(nil, "observability_warning", "system", kind,
			description+": "+strconv.Itoa(previous)+" -> "+strconv.Itoa(count)+" failing", false, nil)
	}
}

// resetObservabilityWarnings clears the per-kind memory. Tests use it.
func resetObservabilityWarnings() {
	observabilityWarningMu.Lock()
	defer observabilityWarningMu.Unlock()
	observabilityWarningLast = map[string]int{}
}
