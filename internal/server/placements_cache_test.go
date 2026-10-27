package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/malleable"
	"github.com/forgec2/forgec2/internal/testutil"
)

func writeProfileFile(t *testing.T, s *Server, name string, pls []malleable.PlacementV2) string {
	t.Helper()
	dir := filepath.Join(s.profileDataDir(), "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"name": name, "version": 2, "placements": pls,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestActivePlacementsCacheIsUsed pins the hot-path fix: the beacon handler reads the
// placement set on every check-in, and the uncached version opened and parsed the
// profile JSON per request.
func TestActivePlacementsCacheIsUsed(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	pls := []malleable.PlacementV2{{Target: "query:id", Chain: "base64"}}
	writeProfileFile(t, s, "prof", pls)
	s.cfg.Malleable.ProfileName = "prof"

	first := s.activePlacements()
	if len(first) == 0 {
		t.Fatal("no placements resolved from the profile file")
	}

	s.placementsCache.mu.Lock()
	hitsAfterFirst, missesAfterFirst := s.placementsCache.hits, s.placementsCache.misses
	s.placementsCache.mu.Unlock()
	if missesAfterFirst != 1 || hitsAfterFirst != 0 {
		t.Fatalf("first call: hits=%d misses=%d, want 0/1", hitsAfterFirst, missesAfterFirst)
	}

	for i := 0; i < 10; i++ {
		if got := s.activePlacements(); len(got) != len(first) {
			t.Fatalf("call %d returned %d placements, want %d", i, len(got), len(first))
		}
	}
	s.placementsCache.mu.Lock()
	hits, misses := s.placementsCache.hits, s.placementsCache.misses
	s.placementsCache.mu.Unlock()
	if misses != 1 {
		t.Fatalf("misses = %d after repeated reads, want 1 (the file was re-parsed)", misses)
	}
	if hits < 10 {
		t.Fatalf("hits = %d after ten reads", hits)
	}
}

// TestActivePlacementsCacheRefreshesAfterTheStatWindow documents the bound: the cache
// re-stats the profile file at most once per placementsStatInterval, so an edit becomes
// visible within that window rather than instantly. The trade is deliberate - the stat
// was the dominant cost of a hit - and 200ms is far below anything an operator notices.
func TestActivePlacementsCacheRefreshesAfterTheStatWindow(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	writeProfileFile(t, s, "prof", []malleable.PlacementV2{{Target: "query:id", Chain: "base64"}})
	s.cfg.Malleable.ProfileName = "prof"

	if got := s.activePlacements(); len(got) != 1 {
		t.Fatalf("initial placements = %d", len(got))
	}

	// Within the window the cached set is still served: that is the documented bound.
	if got := s.activePlacements(); len(got) != 1 {
		t.Fatalf("inside the stat window the cache served %d placements, want the cached 1", len(got))
	}
	// Rewrite with a different placement set and wait out the window.
	time.Sleep(placementsStatInterval + 20*time.Millisecond)
	writeProfileFile(t, s, "prof", []malleable.PlacementV2{
		{Target: "query:id", Chain: "base64"},
		{Target: "header:X-Cover", Chain: "base64"},
	})

	got := s.activePlacements()
	if len(got) != 2 {
		t.Fatalf("after the edit the cache still served %d placements, want 2", len(got))
	}
}

// TestActivePlacementsCacheInvalidatesOnConfigChange is the other half: switching the
// profile name or supplying inline placements changes the key.
func TestActivePlacementsCacheInvalidatesOnConfigChange(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	s.cfg.Malleable.ProfileName = ""
	s.cfg.Malleable.Placements = ""

	if got := s.activePlacements(); len(got) != 0 {
		t.Fatalf("expected no placements, got %d", len(got))
	}

	inline, err := json.Marshal([]malleable.PlacementV2{{Target: "query:id", Chain: "base64"}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Malleable.Placements = string(inline)
	if got := s.activePlacements(); len(got) != 1 {
		t.Fatalf("inline placements configured but got %d", len(got))
	}

	s.cfg.Malleable.Placements = ""
	if got := s.activePlacements(); len(got) != 0 {
		t.Fatalf("clearing the inline placements still returned %d", len(got))
	}
}

// BenchmarkActivePlacements measures the per-beacon cost with and without the cache.
// The uncached path is resolvePlacements, which is what the handler used to call.
func BenchmarkActivePlacements(b *testing.B) {
	s, _ := initBeaconTestServer(b, testutil.SetupTestDB(b))
	s.cfg.Server.DataDir = b.TempDir()
	dir := filepath.Join(s.profileDataDir(), "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"name": "prof", "version": 2,
		"placements": []malleable.PlacementV2{{Target: "query:id", Chain: "base64"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prof.json"), body, 0o600); err != nil {
		b.Fatal(err)
	}
	s.cfg.Malleable.ProfileName = "prof"

	b.Run("cached", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if got := s.activePlacementSet(); len(got) != 1 {
				b.Fatalf("placements = %d", len(got))
			}
		}
	})
	b.Run("uncached", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if got := s.resolvePlacements("", "prof"); len(got) != 1 {
				b.Fatalf("placements = %d", len(got))
			}
		}
	})
}
