package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestTaskDurationStatsCache covers the short-TTL cache added for the endpoint's polling path.
//
// The read costs ~28 ms for a 5000-row window (BenchmarkDurationStatsWindow), so a repeat request
// inside the TTL must not hit the database - but the cache may never mix tenants or parameters, and it
// must expire.
func TestTaskDurationStatsCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	previousTTL := durationStatsCacheTTL
	durationStatsCacheTTL = time.Minute
	t.Cleanup(func() { durationStatsCacheTTL = previousTTL })

	base := time.Now().Add(-time.Hour)
	seed := func() {
		task := db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "x", Status: "completed",
			CreatedAt: base, UpdatedAt: base.Add(100 * time.Millisecond),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed()

	call := func(query string, tenant uint) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/duration"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleTaskDurationStats(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		var resp struct {
			Data struct {
				Sampled int `json:"sampled"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Data.Sampled
	}

	if got := call("?since=24h", 1); got != 1 {
		t.Fatalf("sampled = %d, want 1", got)
	}

	// A new row inside the TTL must NOT be visible: that is the cache doing its job.
	seed()
	if got := call("?since=24h", 1); got != 1 {
		t.Fatalf("sampled = %d after seeding a row; the cached answer should still be 1", got)
	}

	// A different parameter is a different key, so it recomputes and sees the new row.
	if got := call("?since=12h", 1); got != 2 {
		t.Fatalf("sampled = %d for a different window; want 2 (a fresh computation)", got)
	}

	// Another tenant has its own entry (none of its rows), not tenant 1's cached payload.
	if got := call("?since=24h", 2); got != 0 {
		t.Fatalf("sampled = %d for tenant 2; the cache leaked tenant 1's answer", got)
	}

	// Expiry: with the TTL disabled the cache is bypassed entirely and the new row appears.
	durationStatsCacheTTL = 0
	if got := call("?since=24h", 1); got != 2 {
		t.Fatalf("sampled = %d after expiry; want 2", got)
	}
}

// TestStatsCacheCountersIsExposed proves the cache is observable: a hit rate near zero means the TTL is
// wrong for how the console polls, and an eviction count means the key space outgrew the cap. Without
// these counters the cache is a guess.
func TestStatsCacheCountersIsExposed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	s.metrics = NewMetricsCollector(s)
	s.durationStatsCache = nil
	s.durationStatsCacheOnce = sync.Once{}

	previousTTL := durationStatsCacheTTL
	durationStatsCacheTTL = time.Minute
	t.Cleanup(func() { durationStatsCacheTTL = previousTTL })

	base := time.Now().Add(-time.Hour)
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "x", Status: "completed",
		CreatedAt: base, UpdatedAt: base.Add(100 * time.Millisecond),
	}).Error; err != nil {
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
	}

	call() // miss
	call() // hit
	call() // hit

	if got := promtestutil.ToFloat64(s.metrics.StatsCacheMissesTotal); got != 1 {
		t.Fatalf("misses = %v, want 1", got)
	}
	if got := promtestutil.ToFloat64(s.metrics.StatsCacheHitsTotal); got != 2 {
		t.Fatalf("hits = %v, want 2", got)
	}
	if got := promtestutil.ToFloat64(s.metrics.StatsCacheEvictionsTotal); got != 0 {
		t.Fatalf("evictions = %v, want 0", got)
	}
}
