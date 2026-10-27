package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleAgentStatusHistory(c *gin.Context) {
	agentID := c.Param("id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent id required")
		return
	}

	// agent_status_events has no tenant column: scoping the EVENTS query would add
	// "WHERE tenant_id = ?" to a table that cannot support it - which answered 500 for every
	// tenant-scoped operator ("no such column: tenant_id"). The agent carries the tenant, so
	// verify ownership through it and then filter events by agent id.
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}

	rangeParam := c.DefaultQuery("range", "24h")
	var startTime time.Time
	switch rangeParam {
	case "7d":
		startTime = time.Now().AddDate(0, 0, -7)
	case "30d":
		startTime = time.Now().AddDate(0, 0, -30)
	case "1h":
		startTime = time.Now().Add(-time.Hour)
	default:
		startTime = time.Now().Add(-24 * time.Hour)
	}

	var events []db.AgentStatusEvent
	q := s.db.Where("agent_id = ? AND timestamp >= ?", agentID, startTime)
	// Fetch the newest N first (an unbounded Find would return a flapping
	// agent's entire retention window), then restore the ascending order the
	// API has always returned.
	if err := q.Order("timestamp DESC").Limit(AgentStatusHistoryLimit).Find(&events).Error; err != nil {
		handleQueryError(c, err, "Failed to load agent status history")
		return
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	respond(c, gin.H{
		"agent_id": agentID,
		"range":    rangeParam,
		"events":   events,
	})
}

func (s *Server) recordAgentStatusEvent(agentID, status string) {
	event := db.AgentStatusEvent{
		AgentID:   agentID,
		Status:    status,
		Timestamp: time.Now(),
	}
	if err := s.db.Create(&event).Error; err != nil {
		slog.Warn("Failed to record agent status event", "agent_id", agentID, "status", status, "err", err)
	}
}
