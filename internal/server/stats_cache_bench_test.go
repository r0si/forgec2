package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
)

// BenchmarkTaskDurationStatsHandler measures the endpoint with and without its cache.
//
// The cache was added on the strength of an isolated query measurement (BenchmarkDurationStatsWindow,
// ~28 ms for a 5000-row window). This is the number that matters for the operator-facing path: the whole
// handler, including the aggregation and the JSON envelope, on a realistic window. Running both variants in
// one file keeps the comparison honest - same data, same context setup, same assertion.
func BenchmarkTaskDurationStatsHandler(b *testing.B) {
	gin.SetMode(gin.TestMode)
	// The package harness builds a server from a *testing.T; the benchmark opens its own in-memory
	// database the same way, so the measurement does not depend on test-only helpers.
	database := testutil.SetupTestDB(b)
	cfg := config.DefaultConfig()
	setServerTestKeys(cfg)
	s := New(cfg, database)

	const rows = 5000
	base := time.Now().Add(-time.Hour)
	batch := make([]db.Task, 0, rows)
	for i := 0; i < rows; i++ {
		created := base.Add(time.Duration(i) * time.Second)
		batch = append(batch, db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami",
			// Every third task failed, so the outcome aggregation has both branches to count.
			Status:    map[bool]string{true: "failed", false: "completed"}[i%3 == 0],
			CreatedAt: created,
			UpdatedAt: created.Add(time.Duration(i%500+1) * time.Millisecond),
		})
	}
	if err := s.db.CreateInBatches(&batch, 500).Error; err != nil {
		b.Fatalf("seed: %v", err)
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
			b.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
	}

	// Uncached: every call recomputes.
	previousTTL := durationStatsCacheTTL
	durationStatsCacheTTL = 0
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			call()
		}
	})

	// Cached: the first call fills the entry, the rest hit it. Reporting the average over a run that
	// includes one miss is the honest number for a polling client.
	durationStatsCacheTTL = previousTTL
	if durationStatsCacheTTL <= 0 {
		durationStatsCacheTTL = time.Minute
	}
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			call()
		}
	})
	durationStatsCacheTTL = previousTTL
}
