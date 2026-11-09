package server

import (
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// loginActions are the audit actions the login panel shows. The filter is an
// allowlist rather than a "action LIKE auth%" pattern so a future audit action
// cannot leak into a view operators treat as authentication-only.
var loginActions = []string{"login", "login_failed", "logout"}

// loginEvent is the panel row: the audit fields an operator needs plus the
// country cached for the IP. Deliberately narrow — the full audit row carries
// agent/task fields that mean nothing for a sign-in.
type loginEvent struct {
	ID        uint   `json:"id"`
	User      string `json:"user"`
	Action    string `json:"action"`
	IP        string `json:"ip"`
	Country   string `json:"country"`
	City      string `json:"city,omitempty"`
	Success   bool   `json:"success"`
	Details   string `json:"details"`
	CreatedAt string `json:"created_at"`
}

// handleGetLoginEvents lists sign-in/out events with the IP, the timestamp and
// the country resolved for that IP.
//
// Reads go through auditTenantScope: an unscoped admin sees everything, a
// tenant-scoped operator sees only their tenant's rows (legacy tenant_id = 0
// rows stay visible to unscoped admins, as everywhere else in the audit view).
func (s *Server) handleGetLoginEvents(c *gin.Context) {
	p := parsePagination(c, 50, 200)
	user := c.DefaultQuery("user", "")
	result := c.DefaultQuery("result", "")

	query := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).
		Where("action IN ?", loginActions)

	if user != "" {
		query = query.Where("\"user\" LIKE ? ESCAPE '\\'", "%"+escapeLike(user)+"%")
	}
	switch result {
	case "ok":
		query = query.Where("success = ?", true)
	case "failed":
		query = query.Where("success = ?", false)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count login events")
		return
	}

	var rows []db.AuditLog
	if err := query.Order("created_at DESC").Limit(p.PageSize).Offset(p.Offset).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query login events")
		return
	}

	// Resolve countries with a per-request cap: the first load resolves the
	// uncached IPs on the page (bounded), later loads hit the cache.
	ctx := c.Request.Context()
	budget := loginGeoPerRequest
	events := make([]loginEvent, 0, len(rows))
	for _, row := range rows {
		country, city := "", ""
		if budget > 0 && row.IP != "" {
			country, city = s.loginCountry(ctx, row.IP)
			budget--
		} else if row.IP != "" {
			// Over budget: read the cache only (no outbound call).
			country, city = s.cachedLoginCountry(ctx, row.IP)
		}
		events = append(events, loginEvent{
			ID:        row.ID,
			User:      row.User,
			Action:    row.Action,
			IP:        row.IP,
			Country:   country,
			City:      city,
			Success:   row.Success,
			Details:   row.Details,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	respond(c, gin.H{
		"events":    events,
		"total":     total,
		"page":      p.Page,
		"page_size": p.PageSize,
	})
}
