package server

import (
	"log/slog"
	"net/http"
	"sort"
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
		"summary":   s.loginEventSummary(c),
	})
}

// loginEventSummary aggregates the last 24 hours of auth activity by country.
//
// Operators open the login log to answer one question first: "is someone
// hammering us, and from where?" A page of newest-first rows answers it only
// by reading every row, so the panel gets a 24h rollup alongside the table.
func (s *Server) loginEventSummary(c *gin.Context) gin.H {
	since := time.Now().Add(-24 * time.Hour)

	type ipCount struct {
		IP     string
		Failed int64
		OK     int64
	}
	var counts []ipCount
	// One grouped query over the window, capped: a login log covering a
	// thousand distinct IPs must not turn the panel into a full scan.
	if err := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).
		Select("ip, SUM(CASE WHEN success THEN 0 ELSE 1 END) AS failed, SUM(CASE WHEN success THEN 1 ELSE 0 END) AS ok").
		Where("action IN ?", loginActions).
		Where("created_at >= ?", since).
		Group("ip").
		Order("failed DESC").
		Limit(20).
		Scan(&counts).Error; err != nil {
		// The rollup is advisory: a failed aggregate must not hide the rows.
		slog.Warn("Login event summary failed", "err", err)
		return gin.H{}
	}

	type countryStat struct {
		Country string `json:"country"`
		Failed  int64  `json:"failed"`
		OK      int64  `json:"ok"`
	}
	byCountry := map[string]*countryStat{}
	var failedTotal, okTotal int64
	ctx := c.Request.Context()
	budget := loginGeoPerRequest
	for _, row := range counts {
		failedTotal += row.Failed
		okTotal += row.OK
		country := "unknown"
		if row.IP != "" {
			var countryResolved string
			if budget > 0 {
				countryResolved, _ = s.loginCountry(ctx, row.IP)
				budget--
			} else {
				countryResolved, _ = s.cachedLoginCountry(ctx, row.IP)
			}
			if countryResolved != "" {
				country = countryResolved
			}
		}
		stat := byCountry[country]
		if stat == nil {
			stat = &countryStat{Country: country}
			byCountry[country] = stat
		}
		stat.Failed += row.Failed
		stat.OK += row.OK
	}

	ordered := make([]countryStat, 0, len(byCountry))
	for _, stat := range byCountry {
		ordered = append(ordered, *stat)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Failed != ordered[j].Failed {
			return ordered[i].Failed > ordered[j].Failed
		}
		return ordered[i].Country < ordered[j].Country
	})

	return gin.H{
		"window_hours": 24,
		"failed":       failedTotal,
		"ok":           okTotal,
		"by_country":   ordered,
		"distinct_ips": len(counts),
	}
}
