package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Setting, extending or clearing an implant's engagement deadline by hand.
//
// Round 17 made the deadline enforceable but only settable at build time, through the registration
// secret. That is the right default (the deadline travels with the binary) and the wrong only option:
// engagements get extended, a single host gets pulled out of scope, and an operator who cannot record
// that ends up keeping it in their head - which is exactly what the deadline exists to stop.
//
// POST /api/agents/:id/expiry   {"expires_at":"2026-11-01T18:00:00Z"}   or   {"clear":true}
//
// Every change is audited with the previous value, because moving a deadline is a decision about the
// engagement, not a UI toggle.

// agentExpiryRequest is the body.
type agentExpiryRequest struct {
	ExpiresAt string `json:"expires_at"`
	Clear     bool   `json:"clear"`
}

// handleSetAgentExpiry updates one implant's deadline.
func (s *Server) handleSetAgentExpiry(c *gin.Context) {
	agentID := c.Param("id")
	agent, ok := s.getAgentOrFail(c, agentID)
	if !ok {
		return
	}

	var req agentExpiryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body: expected {\"expires_at\":\"<RFC3339>\"} or {\"clear\":true}")
		return
	}

	before := "none"
	if agent.ExpiresAt != nil && !agent.ExpiresAt.IsZero() {
		before = agent.ExpiresAt.UTC().Format(time.RFC3339)
	}

	var next *time.Time
	if req.Clear {
		next = nil
	} else {
		raw := strings.TrimSpace(req.ExpiresAt)
		if raw == "" {
			respondError(c, http.StatusBadRequest, "expires_at is required unless clear is true")
			return
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			respondError(c, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp")
			return
		}
		// A deadline in the past is accepted but must be deliberate: it is how an operator ends an
		// engagement for one host immediately, and silently rounding it up to "now" would hide that.
		next = &parsed
	}

	if err := s.db.Model(&db.Implant{}).Where("id = ?", agentID).Update("expires_at", next).Error; err != nil {
		handleQueryError(c, err, "Failed to update the engagement deadline")
		return
	}

	after := "none"
	expired := false
	if next != nil {
		after = next.UTC().Format(time.RFC3339)
		expired = time.Now().After(*next)
	}
	s.LogAuditRecord(c, "agent_expiry_set", "agent", agentID,
		fmt.Sprintf("engagement deadline %s -> %s", before, after), true, nil)

	respond(c, gin.H{
		"agent_id":   agentID,
		"expires_at": after,
		"previous":   before,
		"expired":    expired,
	})
}
