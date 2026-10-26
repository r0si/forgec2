package server

import (
	"testing"
	"time"
)

// TestStaleThresholdUnits pins the unit of the stale band. The multiplier used
// to be applied to the bare seconds count, so the threshold was 180
// NANOSECONDS: the "stale" bucket matched almost nothing and every agent older
// than the offline threshold was reported as offline — on the agents page, the
// dashboard counts and the REST v1 list alike.
func TestStaleThresholdUnits(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.OfflineThreshold = 60

	offline := s.offlineThreshold()
	stale := s.staleThreshold()
	if offline != 60*time.Second {
		t.Fatalf("offline threshold = %v, want 60s", offline)
	}
	if stale != StaleThresholdMultiplier*60*time.Second {
		t.Fatalf("stale threshold = %v, want %v", stale, StaleThresholdMultiplier*60*time.Second)
	}
	if stale <= offline {
		t.Fatalf("stale (%v) must be wider than offline (%v), otherwise the stale band cannot exist", stale, offline)
	}

	// An agent seen two minutes ago is stale, not offline.
	age := 2 * time.Minute
	if age > offline && age <= stale {
		// This is the band the UI calls "stale"; nothing to assert beyond the
		// thresholds above, but the arithmetic is spelled out for the reader.
	} else {
		t.Fatalf("a %v-old agent falls outside the (offline, stale] band (%v, %v]", age, offline, stale)
	}
}
