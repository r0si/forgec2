package server

import (
	"testing"
	"time"
)

// TestIsDuplicateResult pins result idempotency: a re-sent result id is a
// duplicate within the dedupe window, different results and different agents
// are not, and an empty result id is never treated as a duplicate (that would
// silently drop legacy results).
func TestIsDuplicateResult(t *testing.T) {
	s := mustTenantServer(t)

	if s.isDuplicateResult("agent-a", taskResult{ResultID: ""}) {
		t.Fatal("a result with no id was treated as a duplicate")
	}

	first := taskResult{ResultID: "rid-1"}
	if s.isDuplicateResult("agent-a", first) {
		t.Fatal("the first delivery was reported as a duplicate")
	}
	if !s.isDuplicateResult("agent-a", first) {
		t.Fatal("the second delivery of the same result id was not deduplicated")
	}

	// A different result id is new work.
	if s.isDuplicateResult("agent-a", taskResult{ResultID: "rid-2"}) {
		t.Fatal("a different result id was deduplicated")
	}
	// The same result id from a different agent is new work: the key is
	// (agent, rid), not rid alone.
	if s.isDuplicateResult("agent-b", first) {
		t.Fatal("the same result id from another agent was deduplicated")
	}

	// The window is what makes a re-send a duplicate: an entry older than
	// BeaconDedupWindow is treated as new.
	key := "agent-a/rid-1"
	s.resultDedupeMu.Lock()
	s.resultDedupeCache[key] = time.Now().Add(-2 * BeaconDedupWindow)
	s.resultDedupeMu.Unlock()
	if s.isDuplicateResult("agent-a", first) {
		t.Fatal("an entry past the dedupe window was still treated as a duplicate")
	}
}

// TestIsDuplicateResultCacheIsBounded proves the cache cannot grow without
// bound: past MaxBeaconDedupEntries it evicts, which is what keeps a long
// engagement from leaking memory.
func TestIsDuplicateResultCacheIsBounded(t *testing.T) {
	s := mustTenantServer(t)

	// Fill past the cap with entries older than the sweep window so the sweep
	// cannot clear them for us; only the eviction loop can bound the map.
	old := time.Now().Add(-2 * BeaconDedupWindow)
	s.resultDedupeMu.Lock()
	s.resultDedupeCache = make(map[string]time.Time, MaxBeaconDedupEntries+10)
	for i := 0; i < MaxBeaconDedupEntries+10; i++ {
		s.resultDedupeCache["agent-x/rid-"+jsonNumber(uint(i))] = old
	}
	s.resultDedupeSweep = time.Now() // a sweep is not due
	s.resultDedupeMu.Unlock()

	s.isDuplicateResult("agent-x", taskResult{ResultID: "fresh"})

	s.resultDedupeMu.Lock()
	size := len(s.resultDedupeCache)
	s.resultDedupeMu.Unlock()
	if size > MaxBeaconDedupEntries+1 {
		t.Fatalf("dedupe cache grew to %d entries, cap is %d", size, MaxBeaconDedupEntries)
	}
}
