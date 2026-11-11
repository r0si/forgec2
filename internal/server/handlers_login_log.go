package server

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

	query, rejection := s.loginEventQuery(c)
	if rejection != "" {
		respondError(c, http.StatusBadRequest, rejection)
		return
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

// loginEventQuery applies the login-log filters shared by the page and the
// CSV export. The action allowlist is part of the query, not a caller's
// responsibility: a future audit action must not leak into a view operators
// treat as authentication-only.
func (s *Server) loginEventQuery(c *gin.Context) (*gorm.DB, string) {
	query := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c)
	// The row set is either the sign-in/out events or - when the operator asks
	// for them - the "first sign-in from this address" rows. Both are facts
	// about a sign-in, but the new-address rows are not sign-in/out events, so
	// the allowlist is swapped rather than widened: a new-address sign-in
	// would otherwise appear twice, once as the sign-in and once as the
	// anomaly.
	if loginNewIPOnly(c) {
		query = query.Where("action = ?", "login_new_ip")
	} else {
		query = query.Where("action IN ?", loginActions)
	}
	if user := strings.TrimSpace(c.Query("user")); user != "" {
		query = query.Where(`"user" LIKE ? ESCAPE '\'`, "%"+escapeLike(user)+"%")
	}
	switch strings.ToLower(strings.TrimSpace(c.Query("result"))) {
	case "ok":
		query = query.Where("success = ?", true)
	case "failed":
		query = query.Where("success = ?", false)
	}

	// The same bounds parser the audit and task exports use, so "24h" cannot
	// mean a different window in two security views.
	var sinceAt, untilAt time.Time
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		at, ok := parseTimeBound(raw, time.Now())
		if !ok {
			return nil, formatTimeBoundError("since")
		}
		sinceAt = at
		query = query.Where("created_at >= ?", comparableLocalTime(at))
	}
	if raw := strings.TrimSpace(c.Query("until")); raw != "" {
		at, ok := parseTimeBound(raw, time.Now())
		if !ok {
			return nil, formatTimeBoundError("until")
		}
		untilAt = at
		query = query.Where("created_at <= ?", comparableLocalTime(at))
	}
	if !sinceAt.IsZero() && !untilAt.IsZero() && sinceAt.After(untilAt) {
		return nil, "since must not be after until"
	}
	return query, ""
}

// loginNewIPOnly reports whether the caller asked for the sign-ins that came
// from an address the account had never used before - the anomalous-login
// signal the dashboard counts.
func loginNewIPOnly(c *gin.Context) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query("new_ip"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// loginRegionStat is one region bucket (country + city) of the 24h login
// rollup. An empty Country is "not resolved yet", not an error: the panel
// labels it, and City is empty for loopback and for providers that only know
// the country.
type loginRegionStat struct {
	Country string `json:"country"`
	City    string `json:"city,omitempty"`
	Failed  int64  `json:"failed"`
	OK      int64  `json:"ok"`
}

// loginEventSummary aggregates the last 24 hours of auth activity by region
// (country and city), not just country: two sign-ins from the same country but
// different cities are different facts, and the panel shows the city anyway.
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

	type regionKey struct{ country, city string }
	byRegion := map[regionKey]*loginRegionStat{}
	var failedTotal, okTotal int64
	ctx := c.Request.Context()
	budget := loginGeoPerRequest
	for _, row := range counts {
		failedTotal += row.Failed
		okTotal += row.OK
		var country, city string
		if row.IP != "" {
			if budget > 0 {
				country, city = s.loginCountry(ctx, row.IP)
				budget--
			} else {
				country, city = s.cachedLoginCountry(ctx, row.IP)
			}
		}
		key := regionKey{country: country, city: city}
		stat := byRegion[key]
		if stat == nil {
			stat = &loginRegionStat{Country: country, City: city}
			byRegion[key] = stat
		}
		stat.Failed += row.Failed
		stat.OK += row.OK
	}

	// New-IP sign-ins are the "anomalous login" signal: the first sign-in of
	// an account from an address it has never used. Counted over the same
	// window as the rollup, so both numbers describe the same period.
	var newIP int64
	if err := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).
		Where("action = ?", "login_new_ip").
		Where("created_at >= ?", since).
		Count(&newIP).Error; err != nil {
		// Advisory: a failed count must not hide the rows or the rollup.
		slog.Warn("Login new-IP count failed", "err", err)
	}

	ordered := make([]loginRegionStat, 0, len(byRegion))
	for _, stat := range byRegion {
		ordered = append(ordered, *stat)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Failed != ordered[j].Failed {
			return ordered[i].Failed > ordered[j].Failed
		}
		// Unresolved IPs sort last: an empty country is "not known yet", and
		// putting it first would make the list lead with the least useful row.
		if (ordered[i].Country == "") != (ordered[j].Country == "") {
			return ordered[j].Country == ""
		}
		if ordered[i].Country != ordered[j].Country {
			return ordered[i].Country < ordered[j].Country
		}
		return ordered[i].City < ordered[j].City
	})

	return gin.H{
		"window_hours": 24,
		"failed":       failedTotal,
		"ok":           okTotal,
		"by_region":    ordered,
		"distinct_ips": len(counts),
		"new_ip":       newIP,
	}
}
