package server

import (
	"fmt"
	"net/http"
	"sort"
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
	window, ok := parseStatsWindow(c.DefaultQuery("since", "24h"))
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid since (expected e.g. 1h, 24h, 7d, 30d)")
		return
	}

	query := s.tenantScope(s.db.Model(&db.Task{}), c).
		Select("type, created_at, updated_at").
		Where("status IN ?", []string{"completed", "failed"}).
		Where("created_at >= ?", time.Now().Add(-window)).
		Where("updated_at > created_at")
	if filterType := c.Query("type"); filterType != "" {
		query = query.Where("type = ?", filterType)
	}

	var rows []db.Task
	if err := query.Order("created_at desc").Limit(taskDurationSampleLimit).Find(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to compute task duration statistics")
		return
	}

	byType := make(map[string][]float64, 8)
	for _, row := range rows {
		if row.UpdatedAt.IsZero() || row.CreatedAt.IsZero() || !row.UpdatedAt.After(row.CreatedAt) {
			continue
		}
		byType[row.Type] = append(byType[row.Type], float64(row.UpdatedAt.Sub(row.CreatedAt).Milliseconds()))
	}

	stats := make([]taskDurationStat, 0, len(byType))
	for taskType, samples := range byType {
		stats = append(stats, summariseTaskDurations(taskType, samples))
	}
	// Slowest p95 first: the endpoint exists to point at the problem type.
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].P95Ms == stats[j].P95Ms {
			return stats[i].Type < stats[j].Type
		}
		return stats[i].P95Ms > stats[j].P95Ms
	})

	respond(c, gin.H{
		"since":     c.DefaultQuery("since", "24h"),
		"window_ms": window.Milliseconds(),
		"sampled":   len(rows),
		"truncated": len(rows) >= taskDurationSampleLimit,
		"types":     stats,
	})
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
