package server

import (
	"encoding/json"
	"fmt"
	htmlesc "html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// reportAgentIDs scopes rows that carry only an agent_id (credentials,
// findings, network hosts, SOCKS sessions) to the caller's tenant through
// the owning implant — those tables have no tenant_id column of their own.
func (s *Server) reportAgentIDs(c *gin.Context) *gorm.DB {
	return s.tenantScope(s.db.Model(&db.Implant{}).Select("id"), c)
}

// reportUsernames scopes audit rows (owned by operator username) to the
// caller's tenant, mirroring the dashboard aggregate pattern.
func (s *Server) reportUsernames(c *gin.Context) *gorm.DB {
	return s.tenantScope(s.db.Model(&db.User{}).Select("username"), c)
}

// reportSuccessRate computes the real task success rate (completed /
// completed+failed) within a time range. It returns "N/A" when no terminal
// task exists in range — a nominal "100%" would be a fabricated statistic.
// A query failure returns an error so callers can mark the report partial
// instead of silently showing "N/A" for a broken count.
func (s *Server) reportSuccessRate(c *gin.Context, start, end time.Time) (string, error) {
	var completed, failed int64
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).
		Where("created_at BETWEEN ? AND ? AND status = ?", start, end, "completed").
		Count(&completed).Error; err != nil {
		return "", err
	}
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).
		Where("created_at BETWEEN ? AND ? AND status = ?", start, end, "failed").
		Count(&failed).Error; err != nil {
		return "", err
	}
	if completed+failed == 0 {
		return "N/A", nil
	}
	return fmt.Sprintf("%.1f%%", float64(completed)/float64(completed+failed)*100), nil
}

// handleReportPage renders the report generator page
func (s *Server) handleReportPage(c *gin.Context) {
	stats := s.getNavStats(c)

	// Get summary data. A failed count must not read as a zero on the
	// page, so every failure flips PartialStats for the template.
	partialStats := false
	var totalAgents, onlineAgents, totalTasks, completedTasks, totalCredentials, totalAudits int64
	countOrMark := func(model interface{}, query func(*gorm.DB) *gorm.DB, dst *int64, label string) {
		q := s.db.Model(model)
		if query != nil {
			q = query(q)
		}
		if err := q.Count(dst).Error; err != nil {
			slog.Error("Report page: failed to count", "counter", label, "err", err)
			partialStats = true
		}
	}
	countOrMark(&db.Implant{}, nil, &totalAgents, "agents")
	countOrMark(&db.Implant{}, func(q *gorm.DB) *gorm.DB { return q.Where("status = ?", "online") }, &onlineAgents, "online agents")
	countOrMark(&db.Task{}, nil, &totalTasks, "tasks")
	countOrMark(&db.Task{}, func(q *gorm.DB) *gorm.DB { return q.Where("status = ?", "completed") }, &completedTasks, "completed tasks")
	countOrMark(&db.CredentialEntry{}, nil, &totalCredentials, "credentials")
	countOrMark(&db.AuditLog{}, nil, &totalAudits, "audit logs")

	// Get date range
	var firstAgent db.Implant
	var startDate time.Time
	if err := s.db.Order("created_at asc").First(&firstAgent).Error; err == nil {
		startDate = firstAgent.CreatedAt
	} else {
		startDate = time.Now()
	}

	data := gin.H{
		"Title":          "ForgeC2 - Report Generator",
		"ActiveNav":      "report",
		"Stats":          stats,
		"TotalAgents":    totalAgents,
		"OnlineAgents":   onlineAgents,
		"TotalTasks":     totalTasks,
		"CompletedTasks": completedTasks,
		"TotalCreds":     totalCredentials,
		"TotalAudits":    totalAudits,
		"PartialStats":   partialStats,
		"StartDate":      startDate.Format("2006-01-02"),
		"EndDate":        time.Now().Format("2006-01-02"),
	}
	s.renderPageOrJSON(c, data)
}

// handleGenerateReport generates a comprehensive report
func (s *Server) handleGenerateReport(c *gin.Context) {
	var req struct {
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Include   struct {
			Agents      bool `json:"agents"`
			Tasks       bool `json:"tasks"`
			Creds       bool `json:"creds"`
			Screenshots bool `json:"screenshots"`
			Audit       bool `json:"audit"`
		} `json:"include"`
		Format string `json:"format"` // html, json
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	// Validate format whitelist
	if req.Format == "" {
		req.Format = "html"
	}
	if req.Format != "html" && req.Format != "json" {
		respondError(c, http.StatusBadRequest, "invalid format: must be 'html' or 'json'")
		return
	}

	// Parse dates
	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		startDate = time.Now().AddDate(0, -1, 0) // Default: last month
	}
	endDate, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil {
		endDate = time.Now()
	}
	endDate = endDate.Add(24*time.Hour - 1*time.Second) // End of day

	// Validate date range
	if startDate.After(endDate) {
		respondError(c, http.StatusBadRequest, "start_date must be before end_date")
		return
	}
	// Cap date range to 365 days
	if endDate.Sub(startDate) > MaxReportDateRange {
		respondError(c, http.StatusBadRequest, "date range cannot exceed 365 days")
		return
	}

	// Build report
	report := gin.H{
		"title":       "ForgeC2 Action Report",
		"generated":   time.Now().Format("2006-01-02 15:04:05"),
		"date_range":  fmt.Sprintf("%s to %s", req.StartDate, req.EndDate),
		"summary":     gin.H{},
		"agents":      []gin.H{},
		"tasks":       []gin.H{},
		"credentials": []gin.H{},
		"audit":       []gin.H{},
	}

	// Summary
	var agentCount, taskCount, credCount, auditCount int64
	if req.Include.Agents {
		if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("created_at BETWEEN ? AND ?", startDate, endDate).Count(&agentCount).Error; err != nil {
			markReportSectionPartial(report, "agents", err)
		}
	}
	if req.Include.Tasks {
		if err := s.tenantScope(s.db.Model(&db.Task{}), c).Where("created_at BETWEEN ? AND ?", startDate, endDate).Count(&taskCount).Error; err != nil {
			markReportSectionPartial(report, "tasks", err)
		}
	}
	if req.Include.Creds {
		if err := s.db.Model(&db.CredentialEntry{}).Where("agent_id IN (?) AND created_at BETWEEN ? AND ?", s.reportAgentIDs(c), startDate, endDate).Count(&credCount).Error; err != nil {
			markReportSectionPartial(report, "credentials", err)
		}
	}
	if req.Include.Audit {
		if err := s.db.Model(&db.AuditLog{}).Where("user IN (?) AND created_at BETWEEN ? AND ?", s.reportUsernames(c), startDate, endDate).Count(&auditCount).Error; err != nil {
			markReportSectionPartial(report, "audit", err)
		}
	}

	successRate, rateErr := s.reportSuccessRate(c, startDate, endDate)
	if rateErr != nil {
		markReportSectionPartial(report, "success_rate", rateErr)
		successRate = "N/A"
	}
	report["summary"] = gin.H{
		"total_agents": agentCount,
		"total_tasks":  taskCount,
		"total_creds":  credCount,
		"total_audits": auditCount,
		"success_rate": successRate,
	}

	// Agents
	if req.Include.Agents {
		var agents []db.Implant
		if err := s.tenantScope(s.db, c).Where("created_at BETWEEN ? AND ?", startDate, endDate).Order("created_at desc").Limit(5000).Find(&agents).Error; err != nil {
			markReportSectionPartial(report, "agents", err)
		}
		agentList := make([]gin.H, 0, len(agents))
		for _, a := range agents {
			agentList = append(agentList, gin.H{
				"id":       a.ID,
				"hostname": a.Hostname,
				"os":       a.OS,
				"ip":       a.IP,
				"user":     a.Username,
				"status":   a.Status,
				"created":  a.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["agents"] = agentList
	}

	// Tasks
	if req.Include.Tasks {
		var tasks []db.Task
		if err := s.tenantScope(s.db, c).Where("created_at BETWEEN ? AND ?", startDate, endDate).Order("created_at desc").Limit(100).Find(&tasks).Error; err != nil {
			markReportSectionPartial(report, "tasks", err)
		}
		taskList := make([]gin.H, 0, len(tasks))
		for _, t := range tasks {
			taskList = append(taskList, gin.H{
				"id":      t.ID,
				"agent":   t.AgentID,
				"type":    t.Type,
				"command": t.Command,
				"status":  t.Status,
				"created": t.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["tasks"] = taskList
	}

	// Credentials
	if req.Include.Creds {
		var creds []db.CredentialEntry
		if err := s.db.Where("agent_id IN (?) AND created_at BETWEEN ? AND ?", s.reportAgentIDs(c), startDate, endDate).Order("created_at desc").Limit(100).Find(&creds).Error; err != nil {
			markReportSectionPartial(report, "credentials", err)
		}
		credList := make([]gin.H, 0, len(creds))
		for _, c := range creds {
			credList = append(credList, gin.H{
				"id":       c.ID,
				"agent":    c.AgentID,
				"type":     c.Type,
				"username": c.Username,
				"source":   c.Source,
				"created":  c.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["credentials"] = credList
	}

	// Audit
	if req.Include.Audit {
		var audits []db.AuditLog
		if err := s.db.Where("user IN (?) AND created_at BETWEEN ? AND ?", s.reportUsernames(c), startDate, endDate).Order("created_at desc").Limit(100).Find(&audits).Error; err != nil {
			markReportSectionPartial(report, "audit", err)
		}
		auditList := make([]gin.H, 0, len(audits))
		for _, a := range audits {
			auditList = append(auditList, gin.H{
				"id":      a.ID,
				"user":    a.User,
				"action":  a.Action,
				"details": a.Details,
				"success": a.Success,
				"created": a.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["audit"] = auditList
	}

	// Generate output
	if req.Format == "json" {
		respond(c, report)
		return
	}

	// Generate HTML report
	html := generateHTMLReport(report)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

// markReportSectionPartial records a section that failed to load. A report
// with empty sections used to be indistinguishable from a report with no
// data; consumers (HTML banner, JSON clients) must be able to tell the two
// apart, so every swallowed section error lands here.
func markReportSectionPartial(report gin.H, section string, err error) {
	slog.Error("Report section failed; marking the report partial", "section", section, "err", err)
	report["partial"] = true
	seen := map[string]bool{}
	out := []string{}
	for _, s := range reportSectionFailures(report) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if !seen[section] {
		out = append(out, section)
	}
	report["partial_failures"] = out
}

// reportSectionFailures extracts the recorded partial-failure section list.
func reportSectionFailures(report gin.H) []string {
	switch v := report["partial_failures"].(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// reportMap safely extracts a nested gin.H (empty on shape mismatch instead
// of panicking the handler goroutine).
func reportMap(report gin.H, key string) gin.H {
	if m, ok := report[key].(gin.H); ok {
		return m
	}
	if m, ok := report[key].(map[string]interface{}); ok {
		return gin.H(m)
	}
	return gin.H{}
}

// reportList safely extracts a []gin.H section (nil on shape mismatch).
func reportList(report gin.H, key string) []gin.H {
	if l, ok := report[key].([]gin.H); ok {
		return l
	}
	if l, ok := report[key].([]map[string]interface{}); ok {
		out := make([]gin.H, 0, len(l))
		for _, m := range l {
			out = append(out, gin.H(m))
		}
		return out
	}
	return nil
}

func reportCount(summary gin.H, key string) int64 {
	if n, ok := toInt64(summary[key]); ok {
		return n
	}
	return 0
}

func reportFlag(m gin.H, key string) bool {
	b, _ := m[key].(bool)
	return b
}

// reportPartialBanner renders the "this report is incomplete" banner for a
// partial report. A reader must never mistake a failed section for an empty
// one, so the banner names the sections that failed.
func reportPartialBanner(report gin.H) string {
	if !reportFlag(report, "partial") {
		return ""
	}
	failures := reportSectionFailures(report)
	if len(failures) == 0 {
		failures = []string{"unknown"}
	}
	return `<div class="partial-banner"><strong>This report is incomplete.</strong><span>Failed sections: ` +
		strings.Join(failures, ", ") +
		`. Their figures are missing, not zero — re-run the report after resolving the errors shown in the server log.</span></div>`
}

// generateHTMLReport creates a formatted HTML report
func generateHTMLReport(report gin.H) string {
	summary := reportMap(report, "summary")
	agents := reportList(report, "agents")
	tasks := reportList(report, "tasks")
	creds := reportList(report, "credentials")
	audits := reportList(report, "audit")

	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>%s</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 40px; background: #f8fafc; color: #1e293b; }
        .container { max-width: 1200px; margin: 0 auto; background: white; padding: 40px; border-radius: 12px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
        h1 { color: #4f46e5; border-bottom: 3px solid #4f46e5; padding-bottom: 10px; }
        h2 { color: #334155; margin-top: 40px; border-bottom: 2px solid #e2e8f0; padding-bottom: 8px; }
        .meta { color: #64748b; font-size: 14px; margin-bottom: 30px; }
        .stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 20px; margin: 30px 0; }
        .stat-card { background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%); color: white; padding: 20px; border-radius: 10px; }
        .stat-value { font-size: 32px; font-weight: bold; }
        .stat-label { font-size: 12px; opacity: 0.9; text-transform: uppercase; letter-spacing: 1px; }
        table { width: 100%%; border-collapse: collapse; margin: 20px 0; }
        th { background: #f1f5f9; color: #475569; font-weight: 600; text-align: left; padding: 12px; border-bottom: 2px solid #e2e8f0; }
        td { padding: 10px 12px; border-bottom: 1px solid #f1f5f9; }
        tr:hover { background: #f8fafc; }
        .badge { display: inline-block; padding: 4px 8px; border-radius: 4px; font-size: 12px; font-weight: 500; }
        .badge-success { background: #dcfce7; color: #166534; }
        .badge-failed { background: #fee2e2; color: #991b1b; }
        .badge-pending { background: #fef3c7; color: #92400e; }
        .partial-banner { margin: 0 0 24px; padding: 16px 20px; border-radius: 10px; background: #fef2f2; border: 1px solid #fecaca; color: #991b1b; }
        .partial-banner strong { display: block; margin-bottom: 6px; font-size: 15px; }
        .partial-banner span { font-size: 13px; opacity: 0.9; }
    </style>
</head>
<body>
    <div class="container">
        <h1>馃洝锔?%s</h1>
        <div class="meta">
            Generated: %s | Date Range: %s
        </div>
        %s

        <h2>馃搳 Summary</h2>
        <div class="stats">
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">Total Agents</div>
            </div>
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">Tasks Executed</div>
            </div>
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">Credentials Found</div>
            </div>
            <div class="stat-card">
                <div class="stat-value">%d</div>
                <div class="stat-label">Audit Events</div>
            </div>
        </div>

`, report["title"], report["title"], report["generated"], report["date_range"],
		reportPartialBanner(report),
		reportCount(summary, "total_agents"), reportCount(summary, "total_tasks"),
		reportCount(summary, "total_creds"), reportCount(summary, "total_audits"))

	// Agents table
	if len(agents) > 0 {
		html += `<h2>馃 Agents</h2>
<table>
    <tr><th>Hostname</th><th>OS</th><th>IP</th><th>User</th><th>Status</th><th>Created</th></tr>
`
		for _, a := range agents {
			html += fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><span class=\"badge badge-%s\">%s</span></td><td>%s</td></tr>\n",
				htmlesc.EscapeString(fmt.Sprint(a["hostname"])), htmlesc.EscapeString(fmt.Sprint(a["os"])), htmlesc.EscapeString(fmt.Sprint(a["ip"])), htmlesc.EscapeString(fmt.Sprint(a["user"])),
				func() string {
					if a["status"] == "online" {
						return "success"
					}
					return "pending"
				}(),
				htmlesc.EscapeString(fmt.Sprint(a["status"])), htmlesc.EscapeString(fmt.Sprint(a["created"])))
		}
		html += "</table>\n"
	}

	// Tasks table
	if len(tasks) > 0 {
		html += `<h2>馃搵 Tasks</h2>
<table>
    <tr><th>Type</th><th>Command</th><th>Agent</th><th>Status</th><th>Created</th></tr>
`
		for _, t := range tasks {
			statusBadge := "pending"
			if t["status"] == "completed" {
				statusBadge = "success"
			} else if t["status"] == "failed" || t["status"] == "error" {
				statusBadge = "failed"
			}
			html += fmt.Sprintf("<tr><td>%s</td><td><code>%s</code></td><td>%s</td><td><span class=\"badge badge-%s\">%s</span></td><td>%s</td></tr>\n",
				htmlesc.EscapeString(fmt.Sprint(t["type"])), htmlesc.EscapeString(fmt.Sprint(t["command"])), htmlesc.EscapeString(fmt.Sprint(t["agent"])), statusBadge, htmlesc.EscapeString(fmt.Sprint(t["status"])), htmlesc.EscapeString(fmt.Sprint(t["created"])))
		}
		html += "</table>\n"
	}

	// Credentials table
	if len(creds) > 0 {
		html += `<h2>馃攽 Credentials</h2>
<table>
    <tr><th>Type</th><th>Username</th><th>Source</th><th>Agent</th><th>Created</th></tr>
`
		for _, c := range creds {
			html += fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
				htmlesc.EscapeString(fmt.Sprint(c["type"])), htmlesc.EscapeString(fmt.Sprint(c["username"])), htmlesc.EscapeString(fmt.Sprint(c["source"])), htmlesc.EscapeString(fmt.Sprint(c["agent"])), htmlesc.EscapeString(fmt.Sprint(c["created"])))
		}
		html += "</table>\n"
	}

	// Audit table
	if len(audits) > 0 {
		html += `<h2>馃摑 Audit Log</h2>
<table>
    <tr><th>User</th><th>Action</th><th>Details</th><th>Success</th><th>Created</th></tr>
`
		for _, a := range audits {
			successBadge := "success"
			if !reportFlag(a, "success") {
				successBadge = "failed"
			}
			html += fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td><span class=\"badge badge-%s\">%v</span></td><td>%s</td></tr>\n",
				htmlesc.EscapeString(fmt.Sprint(a["user"])), htmlesc.EscapeString(fmt.Sprint(a["action"])), htmlesc.EscapeString(fmt.Sprint(a["details"])), successBadge, a["success"], htmlesc.EscapeString(fmt.Sprint(a["created"])))
		}
		html += "</table>\n"
	}

	html += `
        <div style="margin-top: 40px; padding-top: 20px; border-top: 2px solid #e2e8f0; color: #64748b; font-size: 12px; text-align: center;">
            Generated by ForgeC2 Professional Red Team Framework
        </div>
    </div>
</body>
</html>`

	return html
}

func parseReportDates(c *gin.Context) (time.Time, time.Time, error) {
	startStr := c.Query("start")
	endStr := c.Query("end")
	var startDate, endDate time.Time
	var parseErr error
	if startStr != "" {
		startDate, parseErr = time.Parse("2006-01-02", startStr)
		if parseErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", parseErr)
		}
	}
	if endStr != "" {
		endDate, parseErr = time.Parse("2006-01-02", endStr)
		if parseErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", parseErr)
		}
		endDate = endDate.Add(24*time.Hour - time.Second)
	}
	if startDate.IsZero() {
		startDate = time.Now().AddDate(0, -1, 0)
	}
	if endDate.IsZero() {
		endDate = time.Now()
	}
	return startDate, endDate, nil
}

func (s *Server) handleAPIGetReportAgents(c *gin.Context) {
	startDate, endDate, err := parseReportDates(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	var agents []db.Implant
	if err := s.tenantScope(s.db, c).Where("created_at BETWEEN ? AND ?", startDate, endDate).Order("created_at desc").Find(&agents).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to load report section")
		return
	}
	agentList := make([]gin.H, 0, len(agents))
	for _, a := range agents {
		agentList = append(agentList, gin.H{
			"id": a.ID, "hostname": a.Hostname, "os": a.OS, "ip": a.IP,
			"user": a.Username, "status": a.Status, "created": a.CreatedAt.Format("2006-01-02 15:04:05"),
			"version": a.Version, "integrity": a.Integrity,
		})
	}
	respond(c, gin.H{"agents": agentList})
}

func (s *Server) handleAPIGetReportTasks(c *gin.Context) {
	startDate, endDate, err := parseReportDates(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	var tasks []db.Task
	if err := s.tenantScope(s.db, c).Where("created_at BETWEEN ? AND ?", startDate, endDate).Order("created_at desc").Limit(200).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to load report section")
		return
	}
	var completed, failed, pending int
	taskList := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		entry := gin.H{
			"id": t.ID, "agent": t.AgentID, "type": t.Type,
			"command": t.Command, "status": t.Status, "created": t.CreatedAt.Format("2006-01-02 15:04:05"),
		}
		taskList = append(taskList, entry)
		switch t.Status {
		case "completed":
			completed++
		case "failed", "error":
			failed++
		default:
			pending++
		}
	}
	respond(c, gin.H{"stats": gin.H{
		"total":     len(tasks),
		"completed": completed,
		"failed":    failed,
		"pending":   pending,
	}, "tasks": taskList})
}

func (s *Server) handleAPIGetReportCredentials(c *gin.Context) {
	startDate, endDate, err := parseReportDates(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	var creds []db.CredentialEntry
	if err := s.db.Where("agent_id IN (?) AND created_at BETWEEN ? AND ?", s.reportAgentIDs(c), startDate, endDate).Order("created_at desc").Limit(100).Find(&creds).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "report creds query failed")
		return
	}
	credList := make([]gin.H, 0, len(creds))
	for _, c := range creds {
		credList = append(credList, gin.H{
			"id": c.ID, "agent": c.AgentID, "type": c.Type,
			"username": c.Username, "source": c.Source, "host": c.Domain,
			"created": c.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	respond(c, gin.H{"credentials": credList})
}

func (s *Server) handleAPIGetReportNetwork(c *gin.Context) {
	var hosts []db.NetworkHost
	if err := s.db.Where("agent_id IN (?)", s.reportAgentIDs(c)).Order("last_seen desc").Limit(100).Find(&hosts).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "report network hosts query failed")
		return
	}
	hostList := make([]gin.H, 0, len(hosts))
	for _, h := range hosts {
		hostList = append(hostList, gin.H{
			"id": h.ID, "agent": h.AgentID, "ip": h.IP,
			"hostname": h.Hostname, "os": h.OS, "services": h.Services,
			"last_seen": h.LastSeen.Format("2006-01-02 15:04:05"),
		})
	}
	respond(c, gin.H{"listeners": hostList})
}

func (s *Server) handleAPIGetReportFindings(c *gin.Context) {
	startDate, endDate, err := parseReportDates(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	var creds []db.CredentialEntry
	if err := s.db.Where("agent_id IN (?) AND created_at BETWEEN ? AND ?", s.reportAgentIDs(c), startDate, endDate).Order("created_at desc").Limit(50).Find(&creds).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "report findings query failed")
		return
	}
	findings := make([]gin.H, 0)
	for _, c := range creds {
		findings = append(findings, gin.H{
			"severity": "medium",
			"title":    fmt.Sprintf("Credential Found: %s", c.Type),
			"detail":   fmt.Sprintf("Username %s found on %s via %s", c.Username, c.Domain, c.Source),
			"source":   c.AgentID,
			"created":  c.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	var tasks []db.Task
	if err := s.tenantScope(s.db, c).Where("status = ? AND created_at BETWEEN ? AND ?", "failed", startDate, endDate).Order("created_at desc").Limit(50).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "report failed tasks query failed")
		return
	}
	for _, t := range tasks {
		findings = append(findings, gin.H{
			"severity": "low",
			"title":    fmt.Sprintf("Task Failed: %s", t.Type),
			"detail":   fmt.Sprintf("Task %s on agent %s: %s", t.Type, t.AgentID, t.Error),
			"source":   t.AgentID,
			"created":  t.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	respond(c, gin.H{"findings": findings})
}

func (s *Server) handleAPIGetReportHistory(c *gin.Context) {
	var reports []db.GeneratedReport
	if err := s.db.Order("created_at desc").Limit(20).Find(&reports).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "report history query failed")
		return
	}
	reportList := make([]gin.H, 0, len(reports))
	for _, r := range reports {
		var sections []string
		if r.Sections != "" {
			if err := json.Unmarshal([]byte(r.Sections), &sections); err != nil {
				slog.Error("Report: failed to unmarshal sections", "error", err)
			}
		}
		reportList = append(reportList, gin.H{
			"id": r.ID, "name": r.Name, "template": r.Template,
			"format": r.Format, "sections": sections,
			"created": r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	respond(c, gin.H{"reports": reportList})
}

// handleAPIExportReportHTML renders the operation report as a self-contained
// HTML document. Named "html" (not "pdf") so the endpoint's contract matches
// what it actually returns: a printable HTML report an operator can save to
// PDF via the browser.
func (s *Server) handleAPIExportReportHTML(c *gin.Context) {
	if !s.requireExportScope(c, "report_export", "report") {
		return
	}
	startDate, endDate, err := parseReportDates(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	template := c.DefaultQuery("template", "technical")

	req := struct {
		StartDate string   `json:"start_date"`
		EndDate   string   `json:"end_date"`
		Sections  []string `json:"sections"`
		Template  string   `json:"template"`
		Format    string   `json:"format"`
	}{
		StartDate: startDate.Format("2006-01-02"),
		EndDate:   endDate.Format("2006-01-02"),
		Template:  template,
		Format:    "html",
	}

	switch template {
	case "technical":
		req.Sections = []string{"summary", "agents", "tasks", "credentials", "network", "findings", "recommendations"}
	case "executive":
		req.Sections = []string{"overview", "recommendations"}
	default:
		req.Sections = []string{"summary", "agents", "tasks", "credentials", "network"}
	}

	report := s.buildReportData(c, req.StartDate, req.EndDate, req.Sections)
	html := generateHTMLReport(report)
	s.LogAuditRecord(c, "report_export", "report", "", "report exported as HTML ("+template+")", true, nil)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

// handleAPIGetGeneratedReport returns the full content of one generated
// report row (used by the AI-report viewer on the Report page).
func (s *Server) handleAPIGetGeneratedReport(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid report id")
		return
	}
	var r db.GeneratedReport
	if err := s.db.First(&r, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "report not found")
		return
	}
	respond(c, gin.H{"report": gin.H{
		"id":       r.ID,
		"name":     r.Name,
		"template": r.Template,
		"format":   r.Format,
		"content":  r.Content,
		"created":  r.CreatedAt.Format("2006-01-02 15:04:05"),
	}})
}

// handleAPIExportGeneratedReport streams one stored report row as a file
// attachment. The Report page's per-row download button targets this; without
// it that button 404s, because handleAPIGetGeneratedReport is a JSON read and
// there was no file endpoint for a single stored report.
//
// Bulk exports still go through requireExportScope so API-key callers must
// hold the bulk_export scope.
// GET /api/report/generated/:id/download
func (s *Server) handleAPIExportGeneratedReport(c *gin.Context) {
	if !s.requireExportScope(c, "report_export", "report") {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid report id")
		return
	}
	var r db.GeneratedReport
	if err := s.db.First(&r, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "report not found")
		return
	}

	ext := r.Format
	if ext == "" {
		ext = "html"
	}
	// The stored name is operator-controlled text; keep it out of the header.
	// The id alone is unambiguous and safe.
	filename := fmt.Sprintf("report-%d.%s", r.ID, ext)

	s.LogAuditRecord(c, "report_export", "report", fmt.Sprintf("%d", r.ID),
		fmt.Sprintf("Downloaded generated report %d (%d bytes)", r.ID, len(r.Content)), true, nil)

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(r.Content))
}

func (s *Server) handleAPIDeleteReport(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid report id")
		return
	}
	if err := s.db.Delete(&db.GeneratedReport{}, id).Error; err != nil {
		slog.Error("Failed to delete report", "id", id, "error", err)
		respondError(c, http.StatusInternalServerError, "failed to delete report")
		return
	}
	respond(c, gin.H{})
}

// buildReportData constructs a gin.H report matching the format expected by generateHTMLReport
func (s *Server) buildReportData(c *gin.Context, startDate, endDate string, sections []string) gin.H {
	start, _ := time.Parse("2006-01-02", startDate)
	end, _ := time.Parse("2006-01-02", endDate)
	end = end.Add(24*time.Hour - time.Second)

	report := gin.H{
		"title":       "ForgeC2 Action Report",
		"generated":   time.Now().Format("2006-01-02 15:04:05"),
		"date_range":  fmt.Sprintf("%s to %s", startDate, endDate),
		"summary":     gin.H{},
		"agents":      []gin.H{},
		"tasks":       []gin.H{},
		"credentials": []gin.H{},
		"audit":       []gin.H{},
	}

	sectionSet := make(map[string]bool, len(sections))
	for _, s := range sections {
		sectionSet[s] = true
	}

	var agentCount, taskCount, credCount, auditCount int64
	if sectionSet["agents"] || sectionSet["summary"] {
		if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("created_at BETWEEN ? AND ?", start, end).Count(&agentCount).Error; err != nil {
			markReportSectionPartial(report, "agents", err)
		}
	}
	if sectionSet["tasks"] || sectionSet["summary"] {
		if err := s.tenantScope(s.db.Model(&db.Task{}), c).Where("created_at BETWEEN ? AND ?", start, end).Count(&taskCount).Error; err != nil {
			markReportSectionPartial(report, "tasks", err)
		}
	}
	if sectionSet["credentials"] || sectionSet["summary"] {
		if err := s.db.Model(&db.CredentialEntry{}).Where("agent_id IN (?) AND created_at BETWEEN ? AND ?", s.reportAgentIDs(c), start, end).Count(&credCount).Error; err != nil {
			markReportSectionPartial(report, "credentials", err)
		}
	}
	if sectionSet["audit"] || sectionSet["summary"] {
		if err := s.db.Model(&db.AuditLog{}).Where("user IN (?) AND created_at BETWEEN ? AND ?", s.reportUsernames(c), start, end).Count(&auditCount).Error; err != nil {
			markReportSectionPartial(report, "audit", err)
		}
	}

	successRate, rateErr := s.reportSuccessRate(c, start, end)
	if rateErr != nil {
		markReportSectionPartial(report, "success_rate", rateErr)
		successRate = "N/A"
	}
	report["summary"] = gin.H{
		"total_agents": agentCount,
		"total_tasks":  taskCount,
		"total_creds":  credCount,
		"total_audits": auditCount,
		"success_rate": successRate,
	}

	if sectionSet["agents"] {
		var agents []db.Implant
		if err := s.tenantScope(s.db, c).Where("created_at BETWEEN ? AND ?", start, end).Order("created_at desc").Find(&agents).Error; err != nil {
			markReportSectionPartial(report, "agents", err)
		}
		agentList := make([]gin.H, 0, len(agents))
		for _, a := range agents {
			agentList = append(agentList, gin.H{
				"id": a.ID, "hostname": a.Hostname, "os": a.OS, "ip": a.IP,
				"user": a.Username, "status": a.Status, "created": a.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["agents"] = agentList
	}

	if sectionSet["tasks"] {
		var tasks []db.Task
		if err := s.tenantScope(s.db, c).Where("created_at BETWEEN ? AND ?", start, end).Order("created_at desc").Limit(100).Find(&tasks).Error; err != nil {
			markReportSectionPartial(report, "tasks", err)
		}
		taskList := make([]gin.H, 0, len(tasks))
		for _, t := range tasks {
			taskList = append(taskList, gin.H{
				"id": t.ID, "agent": t.AgentID, "type": t.Type,
				"command": t.Command, "status": t.Status, "created": t.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["tasks"] = taskList
	}

	if sectionSet["credentials"] {
		var creds []db.CredentialEntry
		if err := s.db.Where("agent_id IN (?) AND created_at BETWEEN ? AND ?", s.reportAgentIDs(c), start, end).Order("created_at desc").Limit(100).Find(&creds).Error; err != nil {
			markReportSectionPartial(report, "credentials", err)
		}
		credList := make([]gin.H, 0, len(creds))
		for _, c := range creds {
			credList = append(credList, gin.H{
				"id": c.ID, "agent": c.AgentID, "type": c.Type,
				"username": c.Username, "source": c.Source, "created": c.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["credentials"] = credList
	}

	if sectionSet["audit"] {
		var audits []db.AuditLog
		if err := s.db.Where("user IN (?) AND created_at BETWEEN ? AND ?", s.reportUsernames(c), start, end).Order("created_at desc").Limit(100).Find(&audits).Error; err != nil {
			markReportSectionPartial(report, "audit", err)
		}
		auditList := make([]gin.H, 0, len(audits))
		for _, a := range audits {
			auditList = append(auditList, gin.H{
				"id": a.ID, "user": a.User, "action": a.Action,
				"details": a.Details, "success": a.Success, "created": a.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}
		report["audit"] = auditList
	}

	return report
}
