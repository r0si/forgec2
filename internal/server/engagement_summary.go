package server

import (
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Engagement summary: "what did this exercise actually do".
//
// Closing an engagement (or letting it end) leaves the question every report has to answer, and until now
// it could only be assembled by hand from three different pages. This is a read-only roll-up over the
// engagement's own window: how many hosts it covered and how they ended up, what tasks ran and with which
// outcome, and which audited actions were taken while it was open.
//
// Window rule: from StartsAt to the earliest of EndsAt, ClosedAt (when closed early) and now. Counting past
// the effective end would attribute the next engagement's work to this one - the same mistake the deadline
// enforcement avoids by merging the two bounds.
//
// GET /api/engagements/:id/summary

// engagementSummaryAgentCounts is the fleet part of the summary.
type engagementSummaryAgentCounts struct {
	Total   int `json:"total"`
	Online  int `json:"online"`
	Expired int `json:"expired"`
}

// handleEngagementSummary answers what happened inside one engagement window.
func (s *Server) handleEngagementSummary(c *gin.Context) {
	data, _, ok := s.buildEngagementSummary(c)
	if !ok {
		return
	}
	respond(c, data)
}

// engagementSummaryPayload is the one place the summary shape is built, shared by the JSON endpoint and the
// Markdown export so the two cannot describe the same exercise differently.
func engagementSummaryPayload(engagement db.Engagement, now, windowEnd time.Time, agents engagementSummaryAgentCounts, tasks db.TaskStats, actions []engagementActionCount, failures engagementFailures) gin.H {
	if actions == nil {
		actions = []engagementActionCount{}
	}
	return gin.H{
		"engagement": engagementView(engagement, now),
		"window": gin.H{
			"starts_at": engagement.StartsAt.UTC().Format(time.RFC3339),
			"ends_at":   windowEnd.UTC().Format(time.RFC3339),
			"open":      now.Before(windowEnd),
		},
		"agents":   agents,
		"tasks":    tasks,
		"actions":  actions,
		"failures": failures,
	}
}

// engagementActionCount is one audited action and how often it happened.
type engagementActionCount struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}
