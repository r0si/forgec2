package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Setting an expiry for a whole fleet at once.
//
// An operator ending an exercise has to time-box every host, and doing that one agent at a time is forty requests and one
// omission away from a beacon that outlives the engagement. This endpoint takes either an explicit list of ids or an
// engagement, and it is deliberately strict about what it skips: an agent the caller cannot see is not silently ignored,
// it is counted and named, because "I set 39 of 40" must not look like success.
//
// POST /api/agents/bulk/expiry
//   {"agent_ids": ["a-1","a-2"], "expires_at": "2026-11-01T18:00:00Z"}
//   {"engagement_id": 7, "clear": true}

const bulkExpiryMaxAgents = 500

// bulkExpiryRequest is the body shape.
type bulkExpiryRequest struct {
	AgentIDs     []string `json:"agent_ids"`
	EngagementID *uint    `json:"engagement_id"`
	ExpiresAt    string   `json:"expires_at"`
	Clear        bool     `json:"clear"`
}

// handleBulkSetAgentExpiry applies one deadline (or clears it) across a selection.
func (s *Server) handleBulkSetAgentExpiry(c *gin.Context) {
	var req bulkExpiryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest,
			`invalid body: expected {"agent_ids":[...]|"engagement_id":N, "expires_at":"<RFC3339>"|"clear":true}`)
		return
	}

	var next *time.Time
	if !req.Clear {
		raw := strings.TrimSpace(req.ExpiresAt)
		if raw == "" {
			respondError(c, http.StatusBadRequest, "expires_at is required unless clear is true")
			return
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid expires_at (expected an RFC3339 timestamp)")
			return
		}
		// A deadline in the past is allowed on purpose: it is how an operator says "end this now, without killing the host".
		next = &parsed
	}

	if len(req.AgentIDs) == 0 && req.EngagementID == nil {
		respondError(c, http.StatusBadRequest, "provide agent_ids or engagement_id")
		return
	}
	if len(req.AgentIDs) > bulkExpiryMaxAgents {
		respondError(c, http.StatusBadRequest,
			fmt.Sprintf("too many agent_ids (%d); the limit is %d - use engagement_id for a whole exercise",
				len(req.AgentIDs), bulkExpiryMaxAgents))
		return
	}

	// Resolve the selection THROUGH the tenant scope, so an id the caller cannot see is reported rather than touched.
	query := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c)
	var agents []db.Implant
	switch {
	case len(req.AgentIDs) > 0:
		if err := query.Where("id IN ?", req.AgentIDs).
			Select("id, hostname").Find(&agents).Error; err != nil {
			handleQueryError(c, err, "Failed to load the selected agents")
			return
		}
	case req.EngagementID != nil:
		if err := query.Where("engagement_id = ?", *req.EngagementID).
			Select("id, hostname").Find(&agents).Error; err != nil {
			handleQueryError(c, err, "Failed to load the engagement agents")
			return
		}
	}

	visible := make(map[string]bool, len(agents))
	for _, agent := range agents {
		visible[agent.ID] = true
	}
	// Requested ids the caller cannot see: named, not silently dropped.
	var skipped []string
	for _, id := range req.AgentIDs {
		if !visible[id] {
			skipped = append(skipped, id)
		}
	}

	if len(agents) > 0 {
		ids := make([]string, 0, len(agents))
		for _, agent := range agents {
			ids = append(ids, agent.ID)
		}
		if err := s.db.WithContext(s.ctx).Model(&db.Implant{}).
			Where("id IN ?", ids).
			Update("expires_at", next).Error; err != nil {
			handleQueryError(c, err, "Failed to update the expiry")
			return
		}
	}

	deadline := "cleared"
	if next != nil {
		deadline = next.UTC().Format(time.RFC3339)
	}
	s.LogAuditRecord(c, "agent_expiry_bulk", "agent", "",
		fmt.Sprintf("set expiry %s on %d agent(s), %d skipped", deadline, len(agents), len(skipped)), true, nil)

	response := gin.H{
		"updated":    len(agents),
		"expires_at": deadline,
		"skipped":    skipped,
	}
	if req.EngagementID != nil {
		response["engagement_id"] = *req.EngagementID
	}
	respond(c, response)
}
