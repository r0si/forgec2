package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// taskDurationSampleLimit bounds how many rows the statistics endpoint reads. The value is a
// percentile input, not an export: reading the whole history of a busy deployment to answer "how slow
// is `shell` today" would be a self-inflicted load spike.
const taskDurationSampleLimit = 5000

// taskDurationStat is one row of GET /api/stats/tasks/duration.
type taskDurationStat struct {
	Type   string  `json:"type"`
	Count  int     `json:"count"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	MaxMs  float64 `json:"max_ms"`
	// Hostname is filled only when group_by=agent, so a caller can show a name instead of an id.
	Hostname string `json:"hostname,omitempty"`
	// Outcome counts come from the same rows as the timings, so the endpoint answers "is it slow" and
	// "is it failing" together instead of needing a second call.
	Completed   int     `json:"completed"`
	Failed      int     `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
}

// handleTaskDurationStats answers "which task types are getting slow" from the task history.
//
// GET /api/stats/tasks/duration?since=24h&type=shell
//
// Only tasks that reached a final state are counted - a pending or running row has no duration yet,
// and averaging it as zero would make every type look faster than it is. The window is the same
// duration syntax the UI already uses elsewhere ("1h", "24h", "7d", "30d"), and the reply is scoped to
// the caller's tenant.
func (s *Server) handleTaskDurationStats(c *gin.Context) {
	// One struct holds the whole identity of the request, so a parameter that shapes the query is also
	// part of the cache key (see durationStatsQuery).
	request := durationStatsQuery{
		Tenant:     s.currentTenantID(c),
		Since:      c.DefaultQuery("since", "24h"),
		Type:       c.Query("type"),
		GroupBy:    c.DefaultQuery("group_by", "type"),
		Bucket:     c.Query("bucket"),
		Engagement: c.Query("engagement"),
	}
	since, filterType, groupBy, bucket := request.Since, request.Type, request.GroupBy, request.Bucket

	// The engagement scope is validated BEFORE the cache lookup: an unusable value must answer 400, not be cached under a key
	// that ignores it.
	engagementAgents, scopedByEngagement, engagementErr := s.taskEngagementFilter(c)
	if engagementErr != nil {
		respondError(c, http.StatusBadRequest, engagementErr.Error())
		return
	}
	// Serve a fresh copy when one exists: the read costs ~28 ms per call for a 5000-row window
	// (BenchmarkDurationStatsWindow) and the console polls while an operator watches the page.
	// A CSV answer must never be served from a JSON cache entry, and the cache key does not carry the format (the format is
	// not a filter, it is a rendering). So a CSV request bypasses the cache entirely rather than poisoning it or being poisoned.
	wantCSV := strings.EqualFold(strings.TrimSpace(c.Query("format")), "csv")
	cacheKey := request.key()
	if payload, hit := s.statsCache().get(cacheKey); hit && !wantCSV {
		if s.metrics != nil {
			s.metrics.StatsCacheHitsTotal.Inc()
		}
		respond(c, payload)
		return
	}
	if s.metrics != nil && cacheKey != "" {
		s.metrics.StatsCacheMissesTotal.Inc()
	}

	window, ok := parseStatsWindow(since)
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid since (expected e.g. 1h, 24h, 7d, 30d)")
		return
	}

	query := s.tenantScope(s.db.Model(&db.Task{}), c).
		// Every column the aggregation reads must be projected: status arrived as "" and every row
		// was counted as failed, exactly as agent_id did when group_by=agent was added.
		Select("type, agent_id, status, created_at, updated_at").
		Where("status IN ?", []string{"completed", "failed"}).
		Where("created_at >= ?", time.Now().Add(-window)).
		Where("updated_at > created_at")
	if filterType != "" {
		query = query.Where("type = ?", filterType)
	}
	// Scoped like the failure reports: "how slow was THIS exercise" is a review question, and an unscoped answer to it is the
	// wrong answer.
	if scopedByEngagement {
		query = query.Where("agent_id IN ?", engagementAgents)
	}

	var rows []db.Task
	if err := query.Order("created_at desc").Limit(taskDurationSampleLimit).Find(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to compute task duration statistics")
		return
	}

	// group_by=type answers "which command is slow"; group_by=agent answers "which beacon is slow".
	// Both are the same question asked of a different key, so the aggregation is shared.
	if groupBy != "type" && groupBy != "agent" {
		respondError(c, http.StatusBadRequest, "invalid group_by (expected type or agent)")
		return
	}
	if bucket != "" && bucket != "hour" && bucket != "day" {
		respondError(c, http.StatusBadRequest, "invalid bucket (expected hour or day)")
		return
	}
	key := func(row db.Task) string {
		if groupBy == "agent" {
			return row.AgentID
		}
		return row.Type
	}

	byKey := make(map[string][]float64, 8)
	// The same rows already carry the outcome, so the success rate costs nothing extra: a type that
	// got slower is interesting, a type that started FAILING is the thing an operator needs to see.
	outcomes := make(map[string]*taskOutcomeCounts, 8)
	for _, row := range rows {
		if row.UpdatedAt.IsZero() || row.CreatedAt.IsZero() || !row.UpdatedAt.After(row.CreatedAt) {
			continue
		}
		k := key(row)
		byKey[k] = append(byKey[k], float64(row.UpdatedAt.Sub(row.CreatedAt).Milliseconds()))
		counts := outcomes[k]
		if counts == nil {
			counts = &taskOutcomeCounts{}
			outcomes[k] = counts
		}
		if row.Status == "completed" {
			counts.completed++
		} else {
			counts.failed++
		}
	}

	stats := make([]taskDurationStat, 0, len(byKey))
	for k, samples := range byKey {
		stat := summariseTaskDurations(k, samples)
		if counts := outcomes[k]; counts != nil {
			stat.Completed = counts.completed
			stat.Failed = counts.failed
			if total := counts.completed + counts.failed; total > 0 {
				stat.SuccessRate = roundToThousandths(float64(counts.completed) / float64(total))
			}
		}
		stats = append(stats, stat)
	}
	// Slowest p95 first: the endpoint exists to point at the problem type.
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].P95Ms == stats[j].P95Ms {
			return stats[i].Type < stats[j].Type
		}
		return stats[i].P95Ms > stats[j].P95Ms
	})

	// The rows are keyed by task type or by agent; name the field after the key so a caller cannot
	// mistake one for the other.
	rowsField := "types"
	if groupBy == "agent" {
		rowsField = "agents"
		stats = s.labelDurationGroups(c, stats)
	}
	resp := gin.H{
		"since":      c.DefaultQuery("since", "24h"),
		"group_by":   groupBy,
		"engagement": c.Query("engagement"),
		"window_ms":  window.Milliseconds(),
		"sampled":    len(rows),
		"truncated":  len(rows) >= taskDurationSampleLimit,
		rowsField:    stats,
	}
	if bucket != "" {
		resp["bucket"] = bucket
		resp["buckets"] = buildDurationBuckets(rows, bucket)
	}
	// ?format=csv takes the table away. The console shows this as a table an operator sorts by p95, and the next thing they do
	// is paste it into a report - which is what a CSV is for. Only the successful answer is cached above, so a CSV request is a
	// cache hit or a normal computation.
	if wantCSV {
		var buf strings.Builder
		writer := csv.NewWriter(&buf)
		header := []string{"Key", "Hostname", "Count", "Mean ms", "p50 ms", "p95 ms", "Max ms", "Completed", "Failed", "Success rate"}
		if err := writer.Write(header); err != nil {
			handleQueryError(c, err, "Failed to encode the duration report")
			return
		}
		for _, row := range stats {
			if err := writer.Write([]string{
				row.Type,
				row.Hostname,
				strconv.Itoa(row.Count),
				strconv.FormatFloat(row.MeanMs, 'f', 1, 64),
				strconv.FormatFloat(row.P50Ms, 'f', 1, 64),
				strconv.FormatFloat(row.P95Ms, 'f', 1, 64),
				strconv.FormatFloat(row.MaxMs, 'f', 1, 64),
				strconv.Itoa(row.Completed),
				strconv.Itoa(row.Failed),
				strconv.FormatFloat(row.SuccessRate, 'f', 2, 64),
			}); err != nil {
				handleQueryError(c, err, "Failed to encode the duration report")
				return
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			handleQueryError(c, err, "Failed to encode the duration report")
			return
		}
		name := fmt.Sprintf("forgec2_duration_%s_%s.csv", groupBy, time.Now().Format("2006-01-02_150405"))
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		if scope := taskExportScope(c); scope != "" {
			c.Header(taskExportScopeHeader, scope)
		} else {
			c.Header(taskExportScopeHeader, "all")
		}
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(buf.String()))
		return
	}
	// Only successful answers are cached; an error must never be remembered as the current state.
	if !wantCSV {
		s.statsCache().put(cacheKey, resp)
	}
	respond(c, resp)
}

// roundToThousandths keeps success_rate readable instead of a long float.
func roundToThousandths(value float64) float64 {
	return float64(int64(value*1000+0.5)) / 1000
}

// taskDurationBucket is one time bucket of GET /api/stats/tasks/duration?bucket=hour|day.
//
// The per-type and per-agent rows answer "what is slow"; a bucket answers "WHEN did it get slow",
// which is the question after someone notices a regression. Buckets are derived from the rows the
// endpoint already read, so asking for them costs no extra query.
type taskDurationBucket struct {
	Start       string  `json:"start"`
	Count       int     `json:"count"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	MaxMs       float64 `json:"max_ms"`
	Failed      int     `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
}

// buildDurationBuckets groups the sampled rows into hourly or daily buckets, oldest first.
//
// Empty buckets are omitted rather than emitted as zeros: a zero-width bucket reads as "everything was
// instant" instead of "nothing ran". The start field makes the gaps visible to a caller that plots the
// series.
func buildDurationBuckets(rows []db.Task, granularity string) []taskDurationBucket {
	type accumulator struct {
		samples   []float64
		failed    int
		completed int
	}
	buckets := make(map[time.Time]*accumulator)

	for _, row := range rows {
		if row.UpdatedAt.IsZero() || row.CreatedAt.IsZero() || !row.UpdatedAt.After(row.CreatedAt) {
			continue
		}
		start := bucketStart(row.CreatedAt, granularity)
		bucket := buckets[start]
		if bucket == nil {
			bucket = &accumulator{}
			buckets[start] = bucket
		}
		bucket.samples = append(bucket.samples, float64(row.UpdatedAt.Sub(row.CreatedAt).Milliseconds()))
		if row.Status == "completed" {
			bucket.completed++
		} else {
			bucket.failed++
		}
	}

	starts := make([]time.Time, 0, len(buckets))
	for start := range buckets {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	out := make([]taskDurationBucket, 0, len(starts))
	for _, start := range starts {
		bucket := buckets[start]
		sort.Float64s(bucket.samples)
		entry := taskDurationBucket{
			Start:  start.UTC().Format(time.RFC3339),
			Count:  len(bucket.samples),
			P50Ms:  percentile(bucket.samples, 0.50),
			P95Ms:  percentile(bucket.samples, 0.95),
			MaxMs:  bucket.samples[len(bucket.samples)-1],
			Failed: bucket.failed,
		}
		if total := bucket.completed + bucket.failed; total > 0 {
			entry.SuccessRate = roundToThousandths(float64(bucket.completed) / float64(total))
		}
		out = append(out, entry)
	}
	return out
}

// bucketStart truncates to the start of the bucket in the zone the timestamps are stored in, so a
// "day" bucket begins at local midnight rather than at 08:00 (which is what Truncate(24h) gives on a
// +08:00 host).
func bucketStart(at time.Time, granularity string) time.Time {
	local := at.In(time.Local)
	if granularity == "day" {
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	}
	return local.Truncate(time.Hour)
}

// summariseTaskDurations computes count/mean/percentiles for one task type.
func summariseTaskDurations(taskType string, samples []float64) taskDurationStat {
	sort.Float64s(samples)
	var total float64
	for _, sample := range samples {
		total += sample
	}
	stat := taskDurationStat{
		Type:  taskType,
		Count: len(samples),
		MaxMs: samples[len(samples)-1],
		P50Ms: percentile(samples, 0.50),
		P95Ms: percentile(samples, 0.95),
	}
	if len(samples) > 0 {
		stat.MeanMs = total / float64(len(samples))
	}
	return stat
}

// percentile uses the nearest-rank method: index = ceil(p*n)-1, clamped. With one sample every
// percentile is that sample, which is the honest answer rather than an interpolation.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(float64(len(sorted))*p+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// parseStatsWindow accepts the "<n><unit>" form the dashboard ranges use.
func parseStatsWindow(raw string) (time.Duration, bool) {
	if raw == "" {
		return 24 * time.Hour, true
	}
	value, err := time.ParseDuration(raw)
	if err == nil && value > 0 && value <= 90*24*time.Hour {
		return value, true
	}
	// time.ParseDuration does not accept "7d"/"30d", which the UI uses for ranges.
	if len(raw) > 1 && raw[len(raw)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(raw[:len(raw)-1], "%d", &days); err == nil && days > 0 && days <= 90 {
			return time.Duration(days) * 24 * time.Hour, true
		}
	}
	return 0, false
}

// labelDurationGroups adds the hostname to each agent-keyed row, in one query rather than one per row.
func (s *Server) labelDurationGroups(c *gin.Context, stats []taskDurationStat) []taskDurationStat {
	if len(stats) == 0 {
		return stats
	}
	ids := make([]string, 0, len(stats))
	for _, stat := range stats {
		ids = append(ids, stat.Type)
	}
	var implants []db.Implant
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Select("id, hostname").Where("id IN ?", ids).Find(&implants).Error; err != nil {
		// A missing hostname is cosmetic: the statistics are still valid without it.
		return stats
	}
	hosts := make(map[string]string, len(implants))
	for _, implant := range implants {
		hosts[implant.ID] = implant.Hostname
	}
	for i := range stats {
		stats[i].Hostname = hosts[stats[i].Type]
	}
	return stats
}

// taskOutcomeCounts is the per-key outcome tally used while grouping.
type taskOutcomeCounts struct {
	completed int
	failed    int
}
