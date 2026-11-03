package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

type TrafficEntry struct {
	Time     time.Time `json:"time"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	RemoteIP string    `json:"remote_ip"`
	AgentID  string    `json:"agent_id"`
	Status   int       `json:"status"`
	Size     int       `json:"size"`
	Latency  string    `json:"latency"`
	TenantID uint      `json:"-"`
}

const maxTrafficLogs = 500
const trafficAgentIDContextKey = "traffic_agent_id"
const trafficTenantIDContextKey = "traffic_tenant_id"

type trafficRing struct {
	mu    sync.RWMutex
	logs  []TrafficEntry
	index int
	count int
}

// trafficByteBucket accumulates real request/response byte counts for one
// hour. The dashboard listener-traffic chart is fed from these counters
// instead of deriving fake sizes from task rows.
type trafficByteBucket struct {
	in  int64
	out int64
}

// trafficByteAccumulator keeps five-minute in/out byte counters for the last
// 7 days in memory (slimmed from 31d per user confirmation). Five-minute
// buckets preserve the dashboard's 1h view without storing individual
// requests. It is updated from the traffic middleware where the real request
// Content-Length and response body size are known.
type trafficByteAccumulator struct {
	mu      sync.RWMutex
	buckets map[trafficByteBucketKey]*trafficByteBucket
}

type trafficByteBucketKey struct {
	start    int64
	tenantID uint
}

const trafficAccumulatorWindowHours = 7 * 24
const trafficAccumulatorBucketSize = 5 * time.Minute

func newTrafficByteAccumulator() *trafficByteAccumulator {
	return &trafficByteAccumulator{
		buckets: make(map[trafficByteBucketKey]*trafficByteBucket),
	}
}

func (a *trafficByteAccumulator) add(t time.Time, tenantID uint, in, out int64) {
	key := trafficByteBucketKey{start: t.Truncate(trafficAccumulatorBucketSize).Unix(), tenantID: tenantID}
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.buckets[key]
	if !ok {
		b = &trafficByteBucket{}
		a.buckets[key] = b
	}
	b.in += in
	b.out += out

	// Prune stale buckets (only when the map grows past the window) so a
	// long-running server does not accumulate unbounded memory.
	windowBuckets := trafficAccumulatorWindowHours * int(time.Hour/trafficAccumulatorBucketSize)
	if prune := time.Now().Add(-trafficAccumulatorWindowHours * time.Hour).Unix(); len(a.buckets) > windowBuckets {
		for k := range a.buckets {
			if k.start < prune {
				delete(a.buckets, k)
			}
		}
	}
}

// sumRange returns one tenant's total in/out bytes for five-minute buckets in
// [start, end). Tenant 0 is the legacy/global view and intentionally
// aggregates every tenant bucket.
func (a *trafficByteAccumulator) sumRange(start, end time.Time, tenantID uint) (in, out int64) {
	startKey := start.Truncate(trafficAccumulatorBucketSize).Unix()
	endKey := end.Truncate(trafficAccumulatorBucketSize).Unix()
	if end.After(end.Truncate(trafficAccumulatorBucketSize)) {
		endKey = end.Truncate(trafficAccumulatorBucketSize).Add(trafficAccumulatorBucketSize).Unix()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for k, b := range a.buckets {
		if k.start >= startKey && k.start < endKey && (tenantID == 0 || k.tenantID == tenantID) {
			in += b.in
			out += b.out
		}
	}
	return in, out
}

func newTrafficRing() *trafficRing {
	return &trafficRing{
		logs: make([]TrafficEntry, maxTrafficLogs),
	}
}

func (t *trafficRing) add(entry TrafficEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs[t.index] = entry
	t.index = (t.index + 1) % maxTrafficLogs
	if t.count < maxTrafficLogs {
		t.count++
	}
}

func (t *trafficRing) recent(n int) []TrafficEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n <= 0 || n > maxTrafficLogs {
		n = maxTrafficLogs
	}
	if n > t.count {
		n = t.count
	}
	result := make([]TrafficEntry, n)
	start := (t.index - n + maxTrafficLogs) % maxTrafficLogs
	for i := 0; i < n; i++ {
		result[i] = t.logs[(start+i)%maxTrafficLogs]
	}
	return result
}

// recentFor copies at most n of the newest entries for one agent in
// chronological order. It avoids the copy-everything-then-filter pattern:
// single pass, no post-sort needed since ring order is already chronological.
func (t *trafficRing) recentFor(agentID string, n int) []TrafficEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n <= 0 || n > maxTrafficLogs {
		n = maxTrafficLogs
	}
	total := t.count
	if total > maxTrafficLogs {
		total = maxTrafficLogs
	}
	result := make([]TrafficEntry, 0, n)
	start := (t.index - total + maxTrafficLogs) % maxTrafficLogs
	// Walk oldest→newest, keeping only the newest n matches via sliding window.
	for i := 0; i < total; i++ {
		l := t.logs[(start+i)%maxTrafficLogs]
		if l.AgentID != agentID {
			continue
		}
		if len(result) == n {
			copy(result, result[1:])
			result = result[:n-1]
		}
		result = append(result, l)
	}
	return result
}

// trafficMiddleware captures beacon API requests for the live traffic viewer
func (s *Server) trafficMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		s.recordTrafficRequest(c, start)
	}
}

// recordTrafficRequest is shared with the custom-profile NoRoute path, which
// cannot call a normal c.Next-based middleware after route resolution.
func (s *Server) recordTrafficRequest(c *gin.Context, start time.Time) {
	path := c.Request.URL.Path
	agentID := c.GetString(trafficAgentIDContextKey)
	if agentID == "" {
		// Older agents may still send the optional header. Authenticated
		// handlers set the context value, which cannot be forged by a client.
		agentID = c.GetHeader("X-Agent-ID")
	}
	entry := TrafficEntry{
		Time:     start,
		Method:   c.Request.Method,
		Path:     path,
		RemoteIP: c.ClientIP(),
		AgentID:  agentID,
		Status:   c.Writer.Status(),
		Size:     c.Writer.Size(),
		Latency:  time.Since(start).Round(time.Millisecond).String(),
	}
	if tenantID, ok := c.Get(trafficTenantIDContextKey); ok {
		entry.TenantID, _ = tenantID.(uint)
	}
	s.trafficLog.add(entry)
	// Feed the listener-traffic chart with real wire sizes: the request
	// Content-Length (as received) and the actual response body written.
	reqBytes := c.Request.ContentLength
	if reqBytes < 0 {
		reqBytes = 0
	}
	s.trafficBytes.add(start, entry.TenantID, reqBytes, int64(entry.Size))
}

func (s *Server) handleTrafficPage(c *gin.Context) {
	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - Traffic Monitor",
		"ActiveNav": "traffic",
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleTrafficData(c *gin.Context) {
	logs := s.trafficLog.recent(200)
	tenantID, resolved := s.resolveTenant(c)
	if !resolved {
		respondError(c, http.StatusInternalServerError, "tenant lookup failed")
		return
	}
	// Legacy tenant 0 operators retain the global view. Scoped operators may
	// only see records attributed to an agent they currently own; unattributed
	// traffic (failed authentication, probes) stays in the global view because
	// it has no safe tenant relationship to scope through.
	if tenantID == 0 {
		respond(c, logs)
		return
	}

	agentIDs := uniqueTrafficAgentIDs(logs)
	visible := make(map[string]bool, len(agentIDs))
	if len(agentIDs) > 0 {
		var found []string
		if err := s.db.Model(&db.Implant{}).
			Where("tenant_id = ? AND id IN ?", tenantID, agentIDs).
			Pluck("id", &found).Error; err != nil {
			handleQueryError(c, err, "Failed to load traffic data")
			return
		}
		for _, id := range found {
			visible[id] = true
		}
	}

	filtered := make([]TrafficEntry, 0, len(logs))
	for _, entry := range logs {
		if entry.AgentID != "" && visible[entry.AgentID] {
			filtered = append(filtered, entry)
		}
	}
	respond(c, filtered)
}

func uniqueTrafficAgentIDs(logs []TrafficEntry) []string {
	seen := make(map[string]struct{}, len(logs))
	ids := make([]string, 0, len(logs))
	for _, entry := range logs {
		if entry.AgentID == "" {
			continue
		}
		if _, ok := seen[entry.AgentID]; ok {
			continue
		}
		seen[entry.AgentID] = struct{}{}
		ids = append(ids, entry.AgentID)
	}
	return ids
}
