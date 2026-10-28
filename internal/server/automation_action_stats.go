package server

import (
	"sync"
	"time"
)

// Automation rule action statistics.
//
// A rule whose action fails every time looks exactly like one that works: `executeAction` logged the failure and moved on,
// and the rule list shows the same name and the same conditions. `run_count` counts FIRINGS, not successes, so the only way
// to discover "this rule has been firing into a void since Tuesday" was to read the log.
//
// In memory on purpose, like the route counters: this is an operational indicator, not an audit fact. A restart resets it,
// and the health report says so.
type ruleActionStat struct {
	OK          int64
	Failed      int64
	LastAttempt time.Time
	LastError   string
}

var (
	ruleActionStatsMu sync.Mutex
	ruleActionStats   = map[string]*ruleActionStat{}
)

// recordRuleActionOutcome folds one action result into the rule's counters.
func recordRuleActionOutcome(ruleID string, ok bool, err error) {
	if ruleID == "" {
		return
	}
	ruleActionStatsMu.Lock()
	defer ruleActionStatsMu.Unlock()
	stat, exists := ruleActionStats[ruleID]
	if !exists {
		stat = &ruleActionStat{}
		ruleActionStats[ruleID] = stat
	}
	stat.LastAttempt = time.Now()
	if ok {
		stat.OK++
		return
	}
	stat.Failed++
	stat.LastError = truncateString(errText(err), 200)
}

// errText renders an error defensively: a nil error here means "failed without a reason", which must not panic.
func errText(err error) string {
	if err == nil {
		return "failed"
	}
	return err.Error()
}

// ruleActionStatsSnapshot copies the counters so a handler cannot mutate them mid-read.
func ruleActionStatsSnapshot() map[string]ruleActionStat {
	ruleActionStatsMu.Lock()
	defer ruleActionStatsMu.Unlock()
	out := make(map[string]ruleActionStat, len(ruleActionStats))
	for id, stat := range ruleActionStats {
		out[id] = *stat
	}
	return out
}

// resetRuleActionStats clears the counters. Tests use it.
func resetRuleActionStats() {
	ruleActionStatsMu.Lock()
	defer ruleActionStatsMu.Unlock()
	ruleActionStats = map[string]*ruleActionStat{}
}

// ruleActionHealth classifies a rule's action dispatches.
//
// The thresholds are advisory: a rule that has never fired is not a fault, and one failure after a hundred successes is a
// blip. The distinction worth naming is "every action succeeds" versus "the actions are not landing".
func ruleActionHealth(stat ruleActionStat) string {
	switch {
	case stat.OK == 0 && stat.Failed == 0:
		return "unused"
	case stat.Failed == 0:
		return "dispatching"
	case stat.OK == 0:
		return "failing"
	case float64(stat.Failed)/float64(stat.OK+stat.Failed) >= 0.5:
		return "degraded"
	default:
		return "dispatching"
	}
}
