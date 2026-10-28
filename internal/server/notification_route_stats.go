package server

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Notification route delivery statistics.
//
// A route that has been failing for a week looks exactly like one that is fine: the fan-out logs each failure and moves on,
// so "nothing said so" was fixed for the SITE and not for the OPERATOR. These counters answer "is this route still
// delivering", which is the question the delivery log cannot answer without reading it line by line.
//
// In memory on purpose: this is an operational indicator, not an audit fact - the audit log holds what was done, and the
// notification rows themselves hold what was persisted. A restart resets the counters, and the API says so.
type notificationRouteStat struct {
	Delivered   int64
	Failed      int64
	LastAttempt time.Time
	LastError   string
}

var (
	notificationStatsMu sync.Mutex
	notificationStats   = map[uint]*notificationRouteStat{}
)

// recordNotificationDelivery folds one delivery outcome into the route's counters.
func recordNotificationDelivery(routeID uint, err error) {
	notificationStatsMu.Lock()
	defer notificationStatsMu.Unlock()
	stat, ok := notificationStats[routeID]
	if !ok {
		stat = &notificationRouteStat{}
		notificationStats[routeID] = stat
	}
	stat.LastAttempt = time.Now()
	if err != nil {
		stat.Failed++
		// Stored trimmed: a delivery error can carry the whole response body, and this is shown in a table.
		stat.LastError = truncateString(err.Error(), 200)
		return
	}
	stat.Delivered++
}

// notificationRouteStatsSnapshot returns a copy, so a handler cannot mutate the live counters while another goroutine
// writes them.
func notificationRouteStatsSnapshot() map[uint]notificationRouteStat {
	notificationStatsMu.Lock()
	defer notificationStatsMu.Unlock()
	out := make(map[uint]notificationRouteStat, len(notificationStats))
	for id, stat := range notificationStats {
		out[id] = *stat
	}
	return out
}

// resetNotificationStats clears the counters. Tests use it; nothing else should.
func resetNotificationStats() {
	notificationStatsMu.Lock()
	defer notificationStatsMu.Unlock()
	notificationStats = map[uint]*notificationRouteStat{}
}

// notificationRouteHealth classifies a route from its counters.
//
// The thresholds are advisory: a route that has never been used is not a fault, and a single failure after a hundred
// successes is a blip rather than an outage. The point is to name the difference, not to alarm.
func notificationRouteHealth(stat notificationRouteStat) string {
	switch {
	case stat.Delivered == 0 && stat.Failed == 0:
		return "unused"
	case stat.Failed == 0:
		return "delivering"
	case stat.Failed > 0 && stat.Delivered == 0:
		return "failing"
	case float64(stat.Failed)/float64(stat.Delivered+stat.Failed) >= 0.5:
		return "degraded"
	default:
		return "delivering"
	}
}

// notificationRouteStatsView renders the snapshot for the list response, keyed by route id as a string so the JSON shape
// stays obvious.
func notificationRouteStatsView() map[string]map[string]interface{} {
	snapshot := notificationRouteStatsSnapshot()
	out := make(map[string]map[string]interface{}, len(snapshot))
	for id, stat := range snapshot {
		view := map[string]interface{}{
			"delivered": stat.Delivered,
			"failed":    stat.Failed,
			"state":     notificationRouteHealth(stat),
		}
		if !stat.LastAttempt.IsZero() {
			view["last_attempt"] = stat.LastAttempt.UTC().Format(time.RFC3339)
		}
		if stat.LastError != "" {
			// The reason a route fails is what makes the counter actionable; the same redaction rule as the route list
			// applies, because a delivery error can quote the target URL.
			redacted := redactDeliveryError(stat.LastError)
			view["last_error"] = redacted
			// The same normalisation the task-failure report uses: two routes failing for the same reason (a 401, a DNS
			// failure) should group together in a list, and a signature is what lets a UI put them side by side.
			view["last_error_signature"] = normaliseFailureText(redacted)
		}
		out[strconv.FormatUint(uint64(id), 10)] = view
	}
	return out
}

// redactDeliveryError keeps a delivery error readable while removing the credential it may quote.
//
// A webhook target IS a credential (the path is the secret), and Go's http client puts the whole URL in its error text.
// The scheme and host stay - they are what an operator needs to see which destination failed.
func redactDeliveryError(text string) string {
	compact := strings.TrimSpace(text)
	if compact == "" {
		return ""
	}
	return notificationURLPattern.ReplaceAllString(compact, "$1/<redacted>")
}

// notificationURLPattern matches a URL and captures scheme plus host, so the path can be dropped.
var notificationURLPattern = regexp.MustCompile(`(https?://[^/\s"'` + "`" + `]+)(/[^\s"'` + "`" + `]*)?`)
