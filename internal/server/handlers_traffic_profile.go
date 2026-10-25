package server

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// autoAdaptMinInterval rate-limits the server-side auto-adapt loop so a broken
// agent cannot churn out a new set_sleep task on every beacon.
const autoAdaptMinInterval = 10 * time.Minute

// adaptationSuggestion is a typed traffic-profile recommendation. It mirrors
// the JSON shape the frontend consumes and is also used by the auto-adapt loop.
type adaptationSuggestion struct {
	DesiredInterval int    `json:"desired_interval"`
	DesiredJitter   int    `json:"desired_jitter"`
	PadSize         int    `json:"pad_size"`
	Reason          string `json:"reason"`
	Confidence      string `json:"confidence"`
	SuggestedAt     string `json:"suggested_at"`
}

// trafficProfileStats is the raw timing/body-size report for one agent.
type trafficProfileStats struct {
	agentLogs        []TrafficEntry
	baselineInterval int
	baselineJitter   int
	baselinePacket   int
	meanInterval     float64
	stddevInterval   float64
	meanPacketSize   float64
	cv               float64
}

func (s *Server) trafficProfileStatsFor(agentID string) trafficProfileStats {
	// recentFor returns matches in chronological order already; no re-sort.
	agentLogs := s.trafficLog.recentFor(agentID, 500)

	st := trafficProfileStats{agentLogs: agentLogs}
	if len(agentLogs) < 2 {
		return st
	}

	intervals := make([]float64, 0, len(agentLogs)-1)
	sizes := make([]float64, 0, len(agentLogs))
	for i, l := range agentLogs {
		sizes = append(sizes, float64(l.Size))
		if i > 0 {
			intervals = append(intervals, agentLogs[i].Time.Sub(agentLogs[i-1].Time).Seconds())
		}
	}

	meanInterval := avg(intervals)
	stddevInterval := stddev(intervals, meanInterval)
	meanSize := avg(sizes)
	cv := 0.0
	if meanInterval > 0 {
		cv = stddevInterval / meanInterval
	}

	st.baselineInterval = int(math.Round(meanInterval))
	if st.baselineInterval > 0 {
		st.baselineJitter = int(math.Round(cv * 100))
	}
	st.baselinePacket = int(math.Round(meanSize))
	st.meanInterval = meanInterval
	st.stddevInterval = stddevInterval
	st.meanPacketSize = meanSize
	st.cv = cv
	return st
}

// handleTrafficProfileGet returns a baseline report and recent beacon records for an agent.
func (s *Server) handleTrafficProfileGet(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent id required")
		return
	}
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	st := s.trafficProfileStatsFor(agentID)

	autoAdapt := false
	var agent db.Implant
	if err := s.tenantScope(s.db, c).First(&agent, "id = ?", agentID).Error; err == nil {
		autoAdapt = agent.AutoAdapt
	}

	var suggestion *adaptationSuggestion
	if len(st.agentLogs) >= 2 {
		// Use actual CurrentJitter for adaptation decision, baselineJitter is only for display (cv*100)
		jitterForAdapt := agent.CurrentJitter
		if jitterForAdapt == 0 {
			jitterForAdapt = st.baselineJitter
		}
		suggestion = computeAdaptationSuggestion(st.baselineInterval, jitterForAdapt, st.baselinePacket)
	}

	// Cap the record payload: full 500-entry dumps on every poll waste
	// bandwidth; the UI pages from the newest end.
	recent := st.agentLogs
	const maxRecentRecords = 50
	if len(recent) > maxRecentRecords {
		recent = recent[len(recent)-maxRecentRecords:]
	}
	respond(c, gin.H{
		"agent_id":             agentID,
		"sample_count":         len(st.agentLogs),
		"baseline_interval":    st.baselineInterval,
		"baseline_jitter":      st.baselineJitter,
		"baseline_packet_size": st.baselinePacket,
		"mean_interval":        int(math.Round(st.meanInterval)),
		"stddev_interval":      int(math.Round(st.stddevInterval)),
		"mean_packet_size":     int(math.Round(st.meanPacketSize)),
		"cv":                   math.Round(st.cv*1000) / 1000,
		"auto_adapt":           autoAdapt,
		"recent_records":       recent,
		"suggestion":           suggestion,
	})
}

// handleTrafficProfileAdapt queues a real set_sleep task that moves the agent
// onto the suggested interval/jitter profile.
func (s *Server) handleTrafficProfileAdapt(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent id required")
		return
	}
	agent, ok := s.getAgentOrFail(c, agentID)
	if !ok {
		return
	}

	st := s.trafficProfileStatsFor(agentID)
	if len(st.agentLogs) < 2 {
		respondError(c, http.StatusBadRequest, "insufficient beacon samples to adapt")
		return
	}
	suggestion := computeAdaptationSuggestion(st.baselineInterval, st.baselineJitter, st.baselinePacket)
	if suggestion == nil {
		respondError(c, http.StatusBadRequest, "no adaptation suggestion available")
		return
	}

	interval := clampInt(suggestion.DesiredInterval, 1, 86400)
	jitter := clampInt(suggestion.DesiredJitter, 0, 100)
	if interval == agent.CurrentInterval && jitter == agent.CurrentJitter {
		respond(c, gin.H{"message": "agent already matches the suggested profile"})
		return
	}

	sleep := fmt.Sprintf("%d,%d", interval, jitter)
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "set_sleep", Command: sleep})
	if task == nil {
		return
	}
	slog.Info("Traffic adaptation task queued", "agent_id", agentID, "task_id", task.ID, "sleep", sleep)
	s.LogAuditRecord(c, "traffic_adapt", "agent", agentID, "Adaptation task queued: set_sleep "+sleep, true, nil)
	respond(c, gin.H{"task_id": task.ID, "message": "set_sleep " + sleep})
}

// handleTrafficProfileAutoAdapt persists the per-agent auto-adapt toggle.
func (s *Server) handleTrafficProfileAutoAdapt(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent id required")
		return
	}
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("id = ?", agentID).Update("auto_adapt", req.Enabled).Error; err != nil {
		slog.Error("Failed to persist auto-adapt toggle", "agent_id", agentID, "error", err)
		respondError(c, http.StatusInternalServerError, "failed to update auto-adapt")
		return
	}
	s.broadcastAgentDataUpdate(agentID, map[string]interface{}{"auto_adapt": req.Enabled})
	s.LogAuditRecord(c, "traffic_auto_adapt", "agent", agentID,
		"Auto-adapt toggled "+boolWord(req.Enabled), true, nil)
	respond(c, gin.H{"auto_adapt": req.Enabled, "message": "auto-adapt " + boolWord(req.Enabled)})
}

// maybeAutoAdaptBeacon implements the server-side auto-adapt loop: on every
// beacon from an auto-adapt agent, compare the observed beacon timing to the
// stored sleep config and queue a real set_sleep task when they deviate.
// Rate-limited per agent so a failing implant cannot spam tasks.
func (s *Server) maybeAutoAdaptBeacon(agent db.Implant) {
	if !agent.AutoAdapt {
		return
	}
	// Rate-limit first: computing stats (ring scan + sort + floats) on every
	// beacon is O(500) per check-in. A recent adaptation means the next one
	// cannot fire for autoAdaptMinInterval anyway, so skip the work.
	s.autoAdaptMu.RLock()
	last := s.autoAdaptLast[agent.ID]
	s.autoAdaptMu.RUnlock()
	if time.Since(last) < autoAdaptMinInterval {
		return
	}

	st := s.trafficProfileStatsFor(agent.ID)
	if len(st.agentLogs) < 2 {
		return
	}
	suggestion := computeAdaptationSuggestion(st.baselineInterval, st.baselineJitter, st.baselinePacket)
	if suggestion == nil {
		return
	}

	interval := clampInt(suggestion.DesiredInterval, 1, 86400)
	jitter := clampInt(suggestion.DesiredJitter, 0, 100)
	if interval == agent.CurrentInterval && jitter == agent.CurrentJitter {
		return
	}

	// Never pile up adaptations: a pending/running set_sleep means the agent is
	// still converging toward the target profile.
	var pending int64
	if err := s.db.Model(&db.Task{}).
		Where("agent_id = ? AND type = ? AND status IN ?", agent.ID, "set_sleep", []string{"pending", "running"}).
		Count(&pending).Error; err != nil || pending > 0 {
		return
	}

	task, err := s.createTask(agent.ID, "set_sleep", fmt.Sprintf("%d,%d", interval, jitter), "", "", "", 0, 0)
	if err != nil {
		slog.Warn("Auto-adapt task creation failed", "agent_id", agent.ID, "error", err)
		return
	}

	s.autoAdaptMu.Lock()
	s.autoAdaptLast[agent.ID] = time.Now()
	// Prune stale entries so the map cannot grow unboundedly with agent IDs.
	if len(s.autoAdaptLast) > 10000 {
		cutoff := time.Now().Add(-24 * time.Hour)
		for id, ts := range s.autoAdaptLast {
			if ts.Before(cutoff) {
				delete(s.autoAdaptLast, id)
			}
		}
	}
	s.autoAdaptMu.Unlock()

	slog.Info("Auto-adapt queued set_sleep", "agent_id", agent.ID, "task_id", task.ID, "interval", interval, "jitter", jitter)
	s.broadcastAgentDataUpdate(agent.ID, map[string]interface{}{"auto_adapt": true})
}

func boolWord(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func avg(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func stddev(vals []float64, mean float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var ss float64
	for _, v := range vals {
		d := v - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(vals)))
}

func computeAdaptationSuggestion(interval, jitter, packetSize int) *adaptationSuggestion {
	if interval <= 0 {
		return nil
	}
	desired := interval
	reason := "baseline matches environment"
	if jitter < 10 {
		desired = int(math.Round(float64(interval) * 1.2))
		reason = "low jitter detected, increasing interval"
	} else if jitter > 50 {
		desired = int(math.Round(float64(interval) * 0.8))
		reason = "high jitter detected, reducing interval"
	}
	if desired < 5 {
		desired = 5
	}
	return &adaptationSuggestion{
		DesiredInterval: desired,
		DesiredJitter:   jitter,
		PadSize:         packetSize + 32,
		Reason:          reason,
		Confidence:      "medium",
		SuggestedAt:     time.Now().Format(time.RFC3339),
	}
}
