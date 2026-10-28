package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestStatsHandlerAllocationBudget turns last round's benchmark into a regression guard.
//
// The numbers it pins are the ones that make the cache worth having: measured with
// BenchmarkTaskDurationStatsHandler, a cached call allocates ~56 objects while an uncached one allocates
// ~170,270. Allocation counts are deterministic (unlike a timing ratio, which would flake on a busy CI box),
// so they can be asserted in a plain test:
//
//   - the cached path must stay under a few hundred allocations - if someone breaks the cache lookup, the
//     number jumps three orders of magnitude and this fails;
//   - the uncached path must stay in the tens of thousands, which proves the two variants really are
//     different and the "cached" measurement is not passing for the wrong reason.
//
// The thresholds are deliberately loose (10x headroom) because the point is to catch a lost cache or a
// runaway decode, not to freeze today's exact numbers.
func TestStatsHandlerAllocationBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	const rows = 5000
	base := time.Now().Add(-time.Hour)
	batch := make([]db.Task, 0, rows)
	for i := 0; i < rows; i++ {
		created := base.Add(time.Duration(i) * time.Second)
		batch = append(batch, db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
			CreatedAt: created,
			UpdatedAt: created.Add(time.Duration(i%500+1) * time.Millisecond),
		})
	}
	if err := s.db.CreateInBatches(&batch, 500).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	call := func() {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/duration?since=24h", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleTaskDurationStats(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
	}

	previousTTL := durationStatsCacheTTL
	t.Cleanup(func() { durationStatsCacheTTL = previousTTL })

	// Uncached: every call decodes the whole window.
	durationStatsCacheTTL = 0
	uncachedAllocs := testing.AllocsPerRun(2, call)
	if uncachedAllocs < 10_000 {
		t.Fatalf("uncached path allocated %.0f objects; the window is not being decoded, so the cached measurement below would prove nothing", uncachedAllocs)
	}

	// Cached: a warm entry answers from memory.
	durationStatsCacheTTL = time.Minute
	call() // fill the entry
	cachedAllocs := testing.AllocsPerRun(10, call)
	if cachedAllocs > 500 {
		t.Fatalf("cached path allocated %.0f objects per call (measured ~56 when the cache works); the cache is not being hit", cachedAllocs)
	}
	if cachedAllocs >= uncachedAllocs/10 {
		t.Fatalf("cached (%.0f) is not an order of magnitude below uncached (%.0f); the cache is doing nothing", cachedAllocs, uncachedAllocs)
	}

	t.Logf("allocations per call: cached %.0f, uncached %.0f (ratio %.0fx)", cachedAllocs, uncachedAllocs, uncachedAllocs/cachedAllocs)
}
