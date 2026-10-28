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
		Select("id, hostname, expires_at").
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
	respond(c, resp)
}

// listedOrEmpty keeps the JSON shape stable: a nil slice marshals as null.
func listedOrEmpty(items []expiryAgent) []expiryAgent {
	if items == nil {
		return []expiryAgent{}
	}
	return items
}
