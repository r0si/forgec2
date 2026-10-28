package server

import (
	"fmt"
	"strings"
	"time"
)

// Time-bound query parameters, in the two spellings an operator actually uses.
//
// `?since=24h` / `?since=7d` is the dashboard's relative form; an RFC3339 timestamp is what an investigation
// pastes from a report. Three exports now accept bounds (audit, tasks, and anything added later), and the first
// implementation of the audit one inlined the parse-and-fallback. Extracting it before a second copy exists is
// the cheap version of the fix this package keeps having to make after the fact.
//
// Returns the instant, or false when the value is neither form. An empty value is the caller's business: it means
// "no bound", not "invalid".

// parseTimeBound resolves one bound. A relative form is measured backwards from now, which is what "since 24h"
// means; `until` accepts the same spellings so a caller can ask for "the last shift" or for a window that ends at
// a past instant.
func parseTimeBound(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if window, ok := parseStatsWindow(raw); ok {
		return now.Add(-window), true
	}
	if at, err := time.Parse(time.RFC3339, raw); err == nil {
		return at, true
	}
	return time.Time{}, false
}

// formatTimeBoundError words the rejection once, so every export says the same thing.
func formatTimeBoundError(name string) string {
	return fmt.Sprintf("invalid %s (expected e.g. 24h, 7d or an RFC3339 timestamp)", name)
}
