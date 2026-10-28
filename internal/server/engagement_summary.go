package server

import (
	"net/http"
	"sort"
	"strings"
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

// handleEngagementSummary answers what happened inside one engagement's window.
func (s *Server) handleEngagementSummary(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	var engagement db.Engagement
	lookup := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Engagement{}), c)
	if err := lookup.Where("id = ?", id).First(&engagement).Error; err != nil {
		respondError(c, http.StatusNotFound, "engagement not found")
		return
	}

	now := time.Now().UTC()
	windowEnd := engagement.EndsAt
	if engagement.ClosedAt != nil && engagement.ClosedAt.Before(windowEnd) {
		windowEnd = *engagement.ClosedAt
	}
	if now.Before(windowEnd) {
		windowEnd = now
	}

	// The fleet this engagement covered.
	var agents []db.Implant
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
		Where("engagement_id = ?", engagement.ID).
		Select("id, status, last_seen, expires_at").
		Find(&agents).Error; err != nil {
		handleQueryError(c, err, "Failed to summarise the engagement fleet")
		return
	}
	counts := engagementSummaryAgentCounts{Total: len(agents)}
	agentIDs := make([]string, 0, len(agents))
	offlineCutoff := now.Add(-s.offlineThreshold())
	for _, agent := range agents {
		agentIDs = append(agentIDs, agent.ID)
		if agent.LastSeen.After(offlineCutoff) {
			counts.Online++
		}
		if agent.ExpiresAt != nil && now.After(*agent.ExpiresAt) {
			counts.Expired++
		}
	}

	// Tasks are counted only for this engagement's agents, and only inside the window: a task created
	// before the exercise opened belongs to whatever came before it.
	tasks := db.TaskStats{}
	if len(agentIDs) > 0 {
		var rows []struct {
			Status string
			Count  int
		}
		if err := s.db.WithContext(s.ctx).Model(&db.Task{}).
			Select("status, COUNT(*) AS count").
			Where("agent_id IN ?", agentIDs).
			Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
			Group("status").
			Scan(&rows).Error; err != nil {
			handleQueryError(c, err, "Failed to summarise engagement tasks")
			return
		}
		for _, row := range rows {
			switch row.Status {
			case "pending":
				tasks.Pending += row.Count
			case "running":
				tasks.Running += row.Count
			case "completed":
				tasks.Completed += row.Count
			case "failed":
				tasks.Failed += row.Count
			}
		}
	}

	// Audited actions inside the window, highest count first. This is the "what did we do" list: it needs
	// no interpretation and it is already tamper-evident.
	type actionCount struct {
		Action string `json:"action"`
		Count  int    `json:"count"`
	}
	var actionRows []actionCount
	if err := s.db.WithContext(s.ctx).Model(&db.AuditLog{}).
		Select("action, COUNT(*) AS count").
		Where("tenant_id = ? OR tenant_id = 0", s.currentTenantID(c)).
		Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
		Group("action").
		Order("count DESC, action ASC").
		Limit(25).
		Scan(&actionRows).Error; err != nil {
		handleQueryError(c, err, "Failed to summarise engagement actions")
		return
	}
	sort.SliceStable(actionRows, func(i, j int) bool {
		if actionRows[i].Count == actionRows[j].Count {
			return actionRows[i].Action < actionRows[j].Action
		}
		return actionRows[i].Count > actionRows[j].Count
	})
	if actionRows == nil {
		actionRows = []actionCount{}
	}

	respond(c, gin.H{
		"engagement": engagementView(engagement, now),
		"window": gin.H{
			"starts_at": engagement.StartsAt.UTC().Format(time.RFC3339),
			"ends_at":   windowEnd.UTC().Format(time.RFC3339),
			"open":      now.Before(windowEnd),
		},
		"agents":  counts,
		"tasks":   tasks,
		"actions": actionRows,
	})
}
