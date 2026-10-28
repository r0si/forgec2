package server

import "fmt"

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
