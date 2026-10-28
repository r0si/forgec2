package server

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// durationStatsCache keeps the computed statistics for a few seconds.
//
// Measured before adding it: reading a 5000-row window for GET /api/stats/tasks/duration costs
// ~28 ms (BenchmarkDurationStatsWindow), and the console polls the endpoint while an operator watches
// the page. Statistics do not need second-by-second freshness - a ten-second-old p95 is the same
// decision as a fresh one - so repeat calls inside the window are answered without touching the
// database.
//
// Bounded and tenant-keyed on purpose: the key includes every request parameter and the tenant, so one
// operator's scoped view can never be served to another, and the entry count is capped so a dealer of
// random parameters cannot grow the map without limit.
type durationStatsCacheEntry struct {
	payload gin.H
	expires time.Time
}

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

// durationStatsCacheKey builds the identity of a request: the tenant plus every parameter that changes
// the answer.
func durationStatsCacheKey(tenantID uint, since, filterType, groupBy, bucket string) string {
	if durationStatsCacheTTL <= 0 {
		return ""
	}
	return strings.Join([]string{
		strconv.FormatUint(uint64(tenantID), 10), since, filterType, groupBy, bucket,
	}, "|")
}

// setMetrics attaches the collector after the fact (a test that installs metrics on an existing server).
func (c *durationStatsCache) setMetrics(m *MetricsCollector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = m
}
