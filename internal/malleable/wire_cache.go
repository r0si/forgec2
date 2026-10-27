package malleable

import (
	"strings"
	"sync"
)

// parsedWireCache memoizes ParseWire results for the strings that arrive on the hot
// path. The beacon handler decodes the placed body on every check-in and rebuilt the
// step slice each time, which is pure allocation for a value that only changes when an
// operator edits the profile.
//
// The cache is bounded: the number of distinct chains a deployment uses is tiny (one
// per placement), and an unbounded map fed from request data would be a slow leak, so
// the eviction is "drop everything" once the cap is reached. That keeps the hot path
// allocation-free without turning it into a memory sink.
const parsedWireCacheMax = 256

type parsedWireEntry struct {
	steps []StepV2
	// empty distinguishes "parsed to nothing" from "not cached yet".
	empty bool
}

var (
	parsedWireMu    sync.RWMutex
	parsedWireCache = make(map[string]parsedWireEntry, parsedWireCacheMax)
)

// ParseWireCached is ParseWire with a bounded memo. The returned slice must be treated
// as read-only: StepV2 values are stored by value and shared across callers.
func ParseWireCached(wire string) []StepV2 {
	if wire == "" {
		return nil
	}
	parsedWireMu.RLock()
	if e, ok := parsedWireCache[wire]; ok {
		parsedWireMu.RUnlock()
		if e.empty {
			return nil
		}
		return e.steps
	}
	parsedWireMu.RUnlock()

	steps := ParseWire(wire)
	entry := parsedWireEntry{steps: steps, empty: len(steps) == 0}

	parsedWireMu.Lock()
	if len(parsedWireCache) >= parsedWireCacheMax {
		parsedWireCache = make(map[string]parsedWireEntry, parsedWireCacheMax)
	}
	parsedWireCache[wire] = entry
	parsedWireMu.Unlock()
	if entry.empty {
		return nil
	}
	return steps
}

// parsedWireCacheSize reports the current entry count (tests and diagnostics).
func parsedWireCacheSize() int {
	parsedWireMu.RLock()
	defer parsedWireMu.RUnlock()
	return len(parsedWireCache)
}

// resetParsedWireCache clears the memo (tests only).
func resetParsedWireCache() {
	parsedWireMu.Lock()
	parsedWireCache = make(map[string]parsedWireEntry, parsedWireCacheMax)
	parsedWireMu.Unlock()
}

// normalizedWireKey exists so callers that build the same chain with different
// surrounding whitespace share one entry.
func normalizedWireKey(wire string) string { return strings.TrimSpace(wire) }
