package server

import (
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// stalledQueueSummary is the whole-fleet version of the queue report, for the readiness probe.
//
// The probe runs when nobody is watching the console, which is exactly when a beacon stops polling: work keeps being accepted, the
// task list keeps growing, and nothing says so. The count is deliberately the same rule as the per-beacon report - oldest pending
// task older than that beacon's own sleep plus the grace period - so the warning and the detail page cannot disagree.
func (s *Server) stalledQueueSummary(c *gin.Context) (stalled int, queued int) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	var rows []struct {
		AgentID   string
		Pending   int
		Running   int
		OldestAge int64
	}
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Task{}), c).
		Select(`agent_id,
			SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending,
			SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) AS running,
			COALESCE(MAX(CASE WHEN status = 'pending'
				THEN CAST(strftime('%s', ?) - strftime('%s', created_at) AS INTEGER) END), 0) AS oldest_age`, now).
		Where("status IN ?", []string{"pending", "running"}).
		Group("agent_id").Scan(&rows).Error; err != nil {
		// A probe that cannot count must not claim zero: "unknown" is the honest answer, and zero stalled would be a reassurance
		// the probe cannot back.
		return 0, -1
	}

	intervals := map[string]int{}
	if len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.AgentID)
		}
		var agents []db.Implant
		if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
			Select("id, current_interval").Where("id IN ?", ids).Find(&agents).Error; err == nil {
			for _, agent := range agents {
				intervals[agent.ID] = agent.CurrentInterval
			}
		}
	}

	for _, row := range rows {
		queued += row.Pending + row.Running
		sleep := time.Duration(intervals[row.AgentID]) * time.Second
		if sleep < time.Second {
			sleep = time.Second
		}
		if row.Pending > 0 && row.OldestAge > int64((sleep+queueStallGrace).Seconds()) {
			stalled++
		}
	}
	return stalled, queued
}
