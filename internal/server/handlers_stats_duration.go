package server

import (
	"fmt"
	"math"
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
	window, ok := parseStatsWindow(c.DefaultQuery("since", "24h"))
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
	if filterType := c.Query("type"); filterType != "" {
		query = query.Where("type = ?", filterType)
	}

	var rows []db.Task
	if err := query.Order("created_at desc").Limit(taskDurationSampleLimit).Find(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to compute task duration statistics")
		return
	}

	// group_by=type answers "which command is slow"; group_by=agent answers "which beacon is slow".
	// Both are the same question asked of a different key, so the aggregation is shared.
	groupBy := c.DefaultQuery("group_by", "type")
	if groupBy != "type" && groupBy != "agent" {
		respondError(c, http.StatusBadRequest, "invalid group_by (expected type or agent)")
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
				stat.SuccessRate = math.Round(float64(counts.completed)/float64(total)*1000) / 1000
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
	respond(c, gin.H{
		"since":     c.DefaultQuery("since", "24h"),
		"group_by":  groupBy,
		"window_ms": window.Milliseconds(),
		"sampled":   len(rows),
		"truncated": len(rows) >= taskDurationSampleLimit,
		rowsField:   stats,
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
