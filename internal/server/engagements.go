package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Engagements: the time-boxed container above a tenant.
//
// The tenant isolates DATA (who may see which fleet). An engagement bounds WHEN work may happen and
// gives the deadline that rounds 17-20 made enforceable a natural home, so an operator records the
// exercise once instead of setting a deadline on every implant. This round is the object itself -
// create, list, close - because everything else (linking secrets to it, enforcing its window) needs
// something to link to first.
//
// Integrity rule that is worth having from the start: an engagement may not overlap another open one in
// the same tenant. Two simultaneous "exercises" against one customer is almost always a mistake, and it
// is the kind of mistake that should be refused at creation rather than discovered when two teams task
// the same host.

const (
	engagementNameMaxLen = 128
	engagementNotesMax   = 1024
)

// engagementRequest is the create body.
type engagementRequest struct {
	Name     string `json:"name"`
	Notes    string `json:"notes"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

// handleCreateEngagement creates a time-boxed engagement for the caller's tenant.
func (s *Server) handleCreateEngagement(c *gin.Context) {
	var req engagementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body: expected name, starts_at and ends_at (RFC3339)")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > engagementNameMaxLen {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("name is required (max %d characters)", engagementNameMaxLen))
		return
	}
	if len(req.Notes) > engagementNotesMax {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("notes too long (max %d characters)", engagementNotesMax))
		return
	}
	starts, err := time.Parse(time.RFC3339, strings.TrimSpace(req.StartsAt))
	if err != nil {
		respondError(c, http.StatusBadRequest, "starts_at must be an RFC3339 timestamp")
		return
	}
	ends, err := time.Parse(time.RFC3339, strings.TrimSpace(req.EndsAt))
	if err != nil {
		respondError(c, http.StatusBadRequest, "ends_at must be an RFC3339 timestamp")
		return
	}
	if !ends.After(starts) {
		respondError(c, http.StatusBadRequest, "ends_at must be after starts_at")
		return
	}

	tenantID := s.currentTenantID(c)
	// Overlap check: an interval [starts, ends) that intersects an open engagement's interval. Closed
	// engagements are history and never block a new one.
	var overlaps int64
	if err := s.db.WithContext(s.ctx).Model(&db.Engagement{}).
		Where("tenant_id = ?", tenantID).
		Where("closed_at IS NULL").
		Where("starts_at < ? AND ends_at > ?", ends, starts).
		Count(&overlaps).Error; err != nil {
		handleQueryError(c, err, "Failed to check for overlapping engagements")
		return
	}
	if overlaps > 0 {
		respondError(c, http.StatusConflict, "this window overlaps an existing open engagement for this tenant; close it or choose another window")
		return
	}

	engagement := db.Engagement{
		TenantID:  tenantID,
		Name:      name,
		Notes:     strings.TrimSpace(req.Notes),
		CreatedBy: usernameOrEmpty(c),
		StartsAt:  starts.UTC(),
		EndsAt:    ends.UTC(),
	}
	if err := s.db.WithContext(s.ctx).Create(&engagement).Error; err != nil {
		handleQueryError(c, err, "Failed to create the engagement")
		return
	}

	s.LogAuditRecord(c, "engagement_create", "engagement", fmt.Sprintf("%d", engagement.ID),
		fmt.Sprintf("engagement %q %s .. %s", engagement.Name,
			engagement.StartsAt.Format(time.RFC3339), engagement.EndsAt.Format(time.RFC3339)), true, nil)

	respond(c, gin.H{"engagement": engagementView(engagement, time.Now())})
}

// handleListEngagements lists the tenant's engagements, newest window first.
func (s *Server) handleListEngagements(c *gin.Context) {
	var engagements []db.Engagement
	// Tenant-scoped: an engagement describes one customer, so no tenant may see another window. This
	// was missing in the first version and the lifecycle test caught it.
	query := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Engagement{}), c)
	if err := query.Order("starts_at desc").Find(&engagements).Error; err != nil {
		handleQueryError(c, err, "Failed to list engagements")
		return
	}
	now := time.Now()
	views := make([]gin.H, 0, len(engagements))
	for _, engagement := range engagements {
		views = append(views, engagementView(engagement, now))
	}
	if len(views) == 0 {
		views = []gin.H{}
	}
	respond(c, gin.H{"count": len(views), "engagements": views})
}

// handleCloseEngagement ends an engagement early without rewriting its agreed window.
func (s *Server) handleCloseEngagement(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	var engagement db.Engagement
	// Scoped like the list: closing another tenant's engagement must be a 404, not a silent success.
	lookup := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Engagement{}), c)
	if err := lookup.Where("id = ?", id).First(&engagement).Error; err != nil {
		respondError(c, http.StatusNotFound, "engagement not found")
		return
	}
	if engagement.ClosedAt != nil {
		respondError(c, http.StatusConflict, "engagement is already closed")
		return
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(s.ctx).Model(&db.Engagement{}).Where("id = ?", engagement.ID).
		Update("closed_at", now).Error; err != nil {
		handleQueryError(c, err, "Failed to close the engagement")
		return
	}
	engagement.ClosedAt = &now
	// The original window stays in the record: closing early is a decision, not an edit of history.
	s.LogAuditRecord(c, "engagement_close", "engagement", fmt.Sprintf("%d", engagement.ID),
		fmt.Sprintf("engagement %q closed early (agreed window ended %s)", engagement.Name,
			engagement.EndsAt.Format(time.RFC3339)), true, nil)
	respond(c, gin.H{"engagement": engagementView(engagement, now)})
}

// engagementView renders an engagement with the state a caller would otherwise compute by hand.
func engagementView(engagement db.Engagement, now time.Time) gin.H {
	state := "scheduled"
	switch {
	case engagement.ClosedAt != nil:
		state = "closed"
	case now.After(engagement.EndsAt):
		state = "ended"
	case !now.Before(engagement.StartsAt):
		state = "active"
	}
	view := gin.H{
		"id":         engagement.ID,
		"name":       engagement.Name,
		"notes":      engagement.Notes,
		"created_by": engagement.CreatedBy,
		"starts_at":  engagement.StartsAt.UTC().Format(time.RFC3339),
		"ends_at":    engagement.EndsAt.UTC().Format(time.RFC3339),
		"state":      state,
		"active":     state == "active",
	}
	if engagement.ClosedAt != nil {
		view["closed_at"] = engagement.ClosedAt.UTC().Format(time.RFC3339)
	}
	return view
}

// usernameOrEmpty is the caller's name for the record, or empty when the context has none (a test
// path, or an API key without a session).
func usernameOrEmpty(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if raw, ok := c.Get("user"); ok {
		if name, ok := raw.(string); ok {
			return name
		}
	}
	return ""
}
