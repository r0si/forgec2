package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// handleBuildEffectiveness correlates successful builds with implants that
// checked in afterwards on the same listener, giving operators a per-build
// "did this payload actually run?" signal (deployed / still online).
//
// The match is a heuristic: an implant counts toward a build when it was
// created within the attribution window after the build finished, on the same
// listener. Builds without a listener (0) fall back to any-listener matching.
// GET /api/builds/effectiveness?days=30&window_hours=72
func (s *Server) handleBuildEffectiveness(c *gin.Context) {
	days := 30
	if v := atoiDefault(c.Query("days"), 30); v >= 1 && v <= 365 {
		days = v
	}
	windowHours := 72
	if v := atoiDefault(c.Query("window_hours"), 72); v >= 1 && v <= 24*30 {
		windowHours = v
	}
	window := time.Duration(windowHours) * time.Hour
	since := time.Now().AddDate(0, 0, -days)

	var builds []struct {
		ID         uint      `json:"id"`
		Platform   string    `json:"platform"`
		Format     string    `json:"format"`
		Filename   string    `json:"filename"`
		C2URL      string    `json:"c2_url"`
		ListenerID uint      `json:"listener_id"`
		User       string    `json:"user"`
		CreatedAt  time.Time `json:"created_at"`
	}
	if err := s.db.Table("build_logs").
		Select("id, platform, format, filename, c2_url, listener_id, \"user\", created_at").
		Where("status = ? AND created_at >= ?", "success", since).
		Order("created_at desc").Limit(500).Scan(&builds).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}

	type implantRow struct {
		ID         string    `json:"id"`
		Hostname   string    `json:"hostname"`
		Status     string    `json:"status"`
		ListenerID uint      `json:"listener_id"`
		Version    string    `json:"version"`
		CreatedAt  time.Time `json:"created_at"`
	}
	var implants []implantRow
	implantQ := s.db.Table("implants").
		Select("id, hostname, status, listener_id, version, created_at").
		Where("created_at >= ?", since)
	implantQ = s.tenantScope(implantQ, c)
	// Bound the in-memory O(builds × implants) join below.
	if err := implantQ.Order("created_at desc").Limit(AgentQueryLimit).Find(&implants).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}

	buildRows := make([]map[string]interface{}, 0, len(builds))
	totalDeployed := 0
	totalOnline := 0

	for _, b := range builds {
		deployedAgents := make([]map[string]string, 0)
		onlineNow := 0
		for _, im := range implants {
			if im.CreatedAt.Before(b.CreatedAt) || im.CreatedAt.After(b.CreatedAt.Add(window)) {
				continue
			}
			if b.ListenerID != 0 && im.ListenerID != 0 && im.ListenerID != b.ListenerID {
				continue
			}
			deployedAgents = append(deployedAgents, map[string]string{
				"id":       im.ID,
				"hostname": im.Hostname,
				"status":   im.Status,
				"version":  im.Version,
			})
			if im.Status == "online" {
				onlineNow++
			}
		}
		entry := map[string]interface{}{
			"id":           b.ID,
			"platform":     b.Platform,
			"format":       b.Format,
			"filename":     b.Filename,
			"c2_url":       b.C2URL,
			"user":         b.User,
			"created_at":   b.CreatedAt,
			"deployed":     len(deployedAgents),
			"online_now":   onlineNow,
			"agents":       deployedAgents,
			"window_hours": windowHours,
		}
		buildRows = append(buildRows, entry)
		if len(deployedAgents) > 0 {
			totalDeployed++
		}
		totalOnline += onlineNow
	}

	var successCount, failedCount int64
	s.db.Table("build_logs").Where("status = ? AND created_at >= ?", "success", since).Count(&successCount)
	s.db.Table("build_logs").Where("status = ? AND created_at >= ?", "failed", since).Count(&failedCount)

	respond(c, gin.H{
		"builds":          buildRows,
		"window_hours":    windowHours,
		"total_success":   successCount,
		"total_failed":    failedCount,
		"deployed_builds": totalDeployed,
		"online_now":      totalOnline,
	})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	neg := false
	i := 0
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		i++
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return def
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return def
		}
	}
	if neg {
		return -n
	}
	return n
}
