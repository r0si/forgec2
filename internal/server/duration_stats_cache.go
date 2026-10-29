package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// durationStatsQuery is the whole identity of a statistics request.
//
// It exists so that ADDING A PARAMETER CANNOT FORGET THE CACHE KEY. The first version of the cache
// built its key from a hand-written list of arguments, which is the kind of thing that goes wrong in
// silence: a new parameter would shape the query but not the key, and callers would receive another
// query's answer. Here every field is part of the key by construction (the key is the struct printed),
// so the only way to add a parameter is to add a field - and then it is in the key.
type durationStatsQuery struct {
	Tenant  uint
	Since   string
	Type    string
	GroupBy string
	Bucket  string
	// Engagement scopes the report to one exercise. It is part of the identity, so two consumers looking at different
	// exercises cannot be served each other's cached answer.
	Engagement string
}

// key renders the cache identity. fmt prints every field, including any added later.
func (q durationStatsQuery) key() string {
	if durationStatsCacheTTL <= 0 {
		return ""
	}
	return fmt.Sprintf("%+v", q)
}

// durationStatsCacheEntry is one cached payload.
type durationStatsCacheEntry struct {
	payload gin.H
	expires time.Time
}

// durationStatsCache keeps the computed statistics for a few seconds.
//
// Measured before adding it: reading a 5000-row window for GET /api/stats/tasks/duration costs
// ~28 ms (BenchmarkDurationStatsWindow), and the console polls the endpoint while an operator watches
// the page. Statistics do not need second-by-second freshness - a ten-second-old p95 is the same
// decision as a fresh one - so repeat calls inside the window are answered without touching the
// database.
//
// Bounded on purpose: the entry count is capped so a dealer of random parameters cannot grow the map
// without limit.
type durationStatsCache struct {
	mu      sync.Mutex
	entries map[string]durationStatsCacheEntry
	// metrics is optional: a cache with no collector still works, it just cannot be observed.
	metrics *MetricsCollector
}

const durationStatsCacheMaxEntries = 256

// durationStatsCacheTTL is a variable so a test can drive expiry without sleeping for it.
var durationStatsCacheTTL = 10 * time.Second

// statsCache returns the server's cache, creating it on first use. Servers built by tests (and by any
// path that does not go through New) then get one too, instead of silently running without a cache.
func (s *Server) statsCache() *durationStatsCache {
	s.durationStatsCacheOnce.Do(func() {
		if s.durationStatsCache == nil {
			s.durationStatsCache = newDurationStatsCache()
		}
		s.durationStatsCache.metrics = s.metrics
	})
	if s.durationStatsCache.metrics == nil && s.metrics != nil {
		// A collector attached after the cache was created still gets the counters.
		s.durationStatsCache.setMetrics(s.metrics)
	}
	return s.durationStatsCache
}

func newDurationStatsCache() *durationStatsCache {
	return &durationStatsCache{entries: make(map[string]durationStatsCacheEntry, 32)}
}

// setMetrics attaches the collector after the fact (a test that installs metrics on an existing server).
func (c *durationStatsCache) setMetrics(m *MetricsCollector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = m
}

// get returns a cached payload when it is still fresh.
func (c *durationStatsCache) get(key string) (gin.H, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.payload, true
}

// put stores a payload, dropping expired entries and enforcing the cap.
func (c *durationStatsCache) put(key string, payload gin.H) {
	if c == nil || key == "" || payload == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for existing, entry := range c.entries {
		if now.After(entry.expires) {
			delete(c.entries, existing)
		}
	}
	if len(c.entries) >= durationStatsCacheMaxEntries {
		// Full of fresh entries: drop everything rather than evict one, so the bound is exact and
		// there is no eviction policy to get subtly wrong.
		c.entries = make(map[string]durationStatsCacheEntry, 32)
		if c.metrics != nil {
			c.metrics.StatsCacheEvictionsTotal.Inc()
		}
	}
	c.entries[key] = durationStatsCacheEntry{payload: payload, expires: now.Add(durationStatsCacheTTL)}
}
