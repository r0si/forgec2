package server

import (
	"sort"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Expiry status: the engagement deadlines that the last round made enforceable, made visible.
//
// A deadline an operator cannot see is still a surprise: the fleet simply stops accepting work one day
// and the first sign is a 403. This endpoint answers "what is about to expire, and what already has",
// in the buckets someone actually plans around - 24 hours, a week, already past - and names the earliest
// deadline so a status line can be a single glance.
//
// Deliberately a read only. Closing an engagement early is a write with consequences for running work
// (killing it mid-collection, or leaving queued tasks un-runnable), and it belongs with the engagement
// entity rather than being smuggled in as a flag on a status endpoint.
//
// GET /api/engagements/expiry

// expiryAgent is one implant in the status list.
type expiryAgent struct {
	AgentID   string    `json:"agent_id"`
	Hostname  string    `json:"hostname,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Expired   bool      `json:"expired"`
}

// handleEngagementExpiry summarises how the caller's fleet ages out.
func (s *Server) handleEngagementExpiry(c *gin.Context) {
	now := time.Now()
	soon := now.Add(24 * time.Hour)
	week := now.Add(7 * 24 * time.Hour)

	var agents []db.Implant
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
		// engagement_id is projected because the per-engagement counts below need it; a projection that
		// omits a column the aggregation reads has silently broken this package twice before.
		Select("id, hostname, expires_at, engagement_id").
		Order("expires_at asc").
		Find(&agents).Error; err != nil {
		handleQueryError(c, err, "Failed to summarise engagement deadlines")
		return
	}

	var (
		unbounded, expired, withinDay, withinWeek int
		earliest                                  *expiryAgent
		listed                                    []expiryAgent
	)
	for _, agent := range agents {
		if agent.ExpiresAt == nil || agent.ExpiresAt.IsZero() {
			unbounded++
			continue
		}
		deadline := agent.ExpiresAt.UTC()
		entry := expiryAgent{
			AgentID:   agent.ID,
			Hostname:  agent.Hostname,
			ExpiresAt: deadline,
			Expired:   now.After(*agent.ExpiresAt),
		}
		switch {
		case entry.Expired:
			expired++
		case deadline.Before(soon):
			withinDay++
		case deadline.Before(week):
			withinWeek++
		}
		if earliest == nil {
			first := entry
			earliest = &first
		}
		listed = append(listed, entry)
	}

	// Deterministic order: the query already sorts, but a stable secondary key keeps two calls equal
	// even if the database returns equal timestamps in a different order.
	sort.SliceStable(listed, func(i, j int) bool {
		if listed[i].ExpiresAt.Equal(listed[j].ExpiresAt) {
			return listed[i].AgentID < listed[j].AgentID
		}
		return listed[i].ExpiresAt.Before(listed[j].ExpiresAt)
	})

	resp := gin.H{
		"total":      len(agents),
		"bounded":    len(listed),
		"unbounded":  unbounded,
		"expired":    expired,
		"within_24h": withinDay,
		"within_7d":  withinWeek,
		"agents":     listedOrEmpty(listed),
	}
	if earliest != nil {
		resp["earliest"] = *earliest
	}

	// Per-engagement roll-up: the deadline that stops work usually belongs to an exercise, so "how much
	// time is left on it and how many hosts does it cover" is the question an operator asks - the
	// per-host list alone does not answer it.
	engagementCounts := make(map[uint]struct{ agents, expired int }, 8)
	for _, agent := range agents {
		if agent.EngagementID == nil {
			continue
		}
		counts := engagementCounts[*agent.EngagementID]
		counts.agents++
		if agent.ExpiresAt != nil && now.After(*agent.ExpiresAt) {
			counts.expired++
		}
		engagementCounts[*agent.EngagementID] = counts
	}
	var engagements []db.Engagement
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Engagement{}), c).
		Order("ends_at asc").Find(&engagements).Error; err == nil {
		summaries := make([]gin.H, 0, len(engagements))
		for _, engagement := range engagements {
			view := engagementView(engagement, now)
			counts := engagementCounts[engagement.ID]
			view["agents"] = counts.agents
			view["agents_expired"] = counts.expired
			if now.Before(engagement.EndsAt) {
				view["ends_in_seconds"] = int(engagement.EndsAt.Sub(now).Seconds())
			} else {
				view["ends_in_seconds"] = 0
			}
			summaries = append(summaries, view)
		}
		resp["engagements"] = summaries
	}
	respond(c, resp)
}

// listedOrEmpty keeps the JSON shape stable: a nil slice marshals as null.
func listedOrEmpty(items []expiryAgent) []expiryAgent {
	if items == nil {
		return []expiryAgent{}
	}
	return items
}
