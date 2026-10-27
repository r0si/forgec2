package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/gin-gonic/gin"
)

const (
	offlineHookSemSize = 8
	offlineHookTimeout = 30 * time.Second
	// statusUpdateChunkSize bounds one IN-list status flip. SQLite rejects a
	// statement with more parameters than SQLITE_MAX_VARIABLE_NUMBER (32766
	// by default), and the paged sweep can collect that many IDs in a single
	// pass on a very large fleet.
	statusUpdateChunkSize = 1000
)

type MonitorCollector struct {
	server         *Server
	mu             sync.Mutex
	lastMetrics    db.SystemMetric
	metricsHistory []db.SystemMetric
	hookSem        chan struct{}
}

func NewMonitorCollector(s *Server) *MonitorCollector {
	return &MonitorCollector{
		server:         s,
		metricsHistory: make([]db.SystemMetric, 0, 60),
		hookSem:        make(chan struct{}, offlineHookSemSize),
	}
}

func (m *MonitorCollector) Start() {
	// Collect initial metrics immediately
	metrics := m.collectSystemMetrics()
	m.acceptMetrics(metrics)

	// Start periodic collection
	m.server.wg.Add(2)
	go m.collectMetrics()
	go m.checkAlerts()
}

// acceptMetrics records a sample in lastMetrics/history only when it contains
// at least one real reading; a tick where every OS-backed syscall failed is
// discarded so a hardware read failure never looks like a healthy 0%.
func (m *MonitorCollector) acceptMetrics(metrics db.SystemMetric) {
	if math.IsNaN(metrics.CPULoad) && math.IsNaN(metrics.DiskUsed) {
		return
	}
	m.mu.Lock()
	m.lastMetrics = metrics
	m.metricsHistory = append(m.metricsHistory, metrics)
	if len(m.metricsHistory) > 60 {
		m.metricsHistory = m.metricsHistory[len(m.metricsHistory)-60:]
	}
	m.mu.Unlock()
}

func (m *MonitorCollector) collectMetrics() {
	defer m.server.wg.Done()
	ticker := time.NewTicker(MonitorMetricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.server.ctx.Done():
			return
		case <-ticker.C:
			metrics := m.collectSystemMetrics()
			m.acceptMetrics(metrics)
			if math.IsNaN(metrics.CPULoad) && math.IsNaN(metrics.DiskUsed) {
				slog.Warn("Monitor: skipping metrics persistence (all OS probes failed)")
				continue
			}
			if err := m.server.db.Create(&metrics).Error; err != nil {
				slog.Error("Failed to persist system metrics", "error", err)
			}
		}
	}
}

func (m *MonitorCollector) collectSystemMetrics() db.SystemMetric {
	var metrics db.SystemMetric

	if cpu, ok := m.getCPULoad(); ok {
		metrics.CPULoad = cpu
	} else {
		metrics.CPULoad = math.NaN()
	}
	memStats := m.getMemoryStats()
	metrics.MemoryUsed = memStats.used
	metrics.MemoryTotal = memStats.total
	if used, total, ok := m.getDiskStats(); ok {
		metrics.DiskUsed = used
		metrics.DiskTotal = total
	} else {
		metrics.DiskUsed = math.NaN()
		metrics.DiskTotal = math.NaN()
	}

	hostname, _ := os.Hostname()
	metrics.Hostname = hostname
	metrics.CreatedAt = time.Now()

	return metrics
}

func (m *MonitorCollector) getMemoryStats() struct{ used, total float64 } {
	var mstats runtime.MemStats
	runtime.ReadMemStats(&mstats)
	return struct{ used, total float64 }{float64(mstats.Alloc), float64(mstats.Sys)}
}

func (m *MonitorCollector) checkAlerts() {
	defer m.server.wg.Done()
	ticker := time.NewTicker(MonitorAlertInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.server.ctx.Done():
			return
		case <-ticker.C:
			m.checkSystemAlerts()
			m.checkAgentAlerts()
		}
	}
}

func (m *MonitorCollector) checkSystemAlerts() {
	m.mu.Lock()
	metrics := m.lastMetrics
	m.mu.Unlock()

	var rules []db.AlertRule
	if err := m.server.db.Where("enabled = ? AND type IN ?", true, []string{"cpu_high", "memory_high", "disk_high"}).Limit(200).Find(&rules).Error; err != nil {
		slog.Error("Monitor: failed to query system alert rules", "err", err)
	}

	for _, rule := range rules {
		var trigger bool
		var value float64

		switch rule.Type {
		case "cpu_high":
			value = metrics.CPULoad
			// A NaN reading means the OS probe failed; never evaluate (or
			// silently pass) an alert against fabricated data.
			trigger = !math.IsNaN(value) && value > rule.Threshold
		case "memory_high":
			if metrics.MemoryTotal > 0 {
				value = (metrics.MemoryUsed / metrics.MemoryTotal) * 100
				trigger = value > rule.Threshold
			}
		case "disk_high":
			if metrics.DiskTotal > 0 && !math.IsNaN(metrics.DiskUsed) {
				value = (metrics.DiskUsed / metrics.DiskTotal) * 100
				trigger = value > rule.Threshold
			}
		}

		if trigger {
			m.triggerAlert(&rule, "system", metrics.Hostname,
				fmt.Sprintf("%.1f%%", value),
				map[string]interface{}{"value": value, "threshold": rule.Threshold})
		}
	}
}

func (m *MonitorCollector) checkAgentAlerts() {
	var rules []db.AlertRule
	if err := m.server.db.Where("enabled = ? AND type = ?", true, "agent_offline").Limit(200).Find(&rules).Error; err != nil {
		slog.Error("Monitor: failed to query agent offline rules", "err", err)
	}

	now := time.Now()
	var staleIDs []string
	var offlineIDs []string
	var statusEvents []db.AgentStatusEvent
	// Page through the online/stale agents so the sweep stays complete past
	// one page: a single capped query would silently skip the tail of a large
	// fleet, leaving those agents unmarked (and unbroadcast) until they
	// re-check in. Oldest last_seen first so a partial pass still reaps the
	// worst offenders; the set is stable within a tick — statuses only flip
	// after the loop — so offset pages do not shift under us.
	for offset := 0; ; offset += agentReapPageSize {
		var agents []db.Implant
		if err := m.server.db.Where("status IN ?", []string{"online", "stale"}).
			Order("last_seen ASC, id ASC").
			Limit(agentReapPageSize).Offset(offset).
			Find(&agents).Error; err != nil {
			slog.Error("Monitor: failed to query agents for alert check", "err", err)
			return
		}
		if len(agents) == 0 {
			break
		}
		for _, agent := range agents {
			offlineFor := now.Sub(agent.LastSeen)
			// Per-agent thresholds: long-sleep agents must not flap between
			// check-ins (see offlineThresholdFor).
			offlineThreshold := m.server.offlineThresholdFor(agent)
			staleThreshold := m.server.staleThresholdFor(agent)
			switch {
			case offlineFor > staleThreshold:
				offlineIDs = append(offlineIDs, agent.ID)
				m.server.broadcastAgentOffline(agent, "offline")
				statusEvents = append(statusEvents, db.AgentStatusEvent{AgentID: agent.ID, Status: "offline", Timestamp: now})
				if m.server.pluginManager != nil {
					select {
					case m.hookSem <- struct{}{}:
						go func(a db.Implant) {
							defer func() {
								<-m.hookSem
								if r := recover(); r != nil {
									slog.Error("Plugin hook panicked (agent offline)", "agent", a.ID, "recover", r)
								}
							}()
							ctx, cancel := context.WithTimeout(context.Background(), offlineHookTimeout)
							defer cancel()
							if err := m.server.pluginManager.ExecuteHook(ctx, plugin.Event{
								Type:      plugin.EventAgentDisconnect,
								Timestamp: time.Now(),
								AgentID:   a.ID,
								Payload: map[string]interface{}{
									"hostname":            a.Hostname,
									"ip":                  a.IP,
									"offline_for_seconds": now.Sub(a.LastSeen).Seconds(),
								},
							}); err != nil {
								slog.Warn("Hook errors on agent_disconnect event", "agent_id", a.ID, "err", err)
							}
						}(agent)
					default:
						slog.Warn("Monitor: offline hook backlog full, skipping agent", "agent", agent.ID)
					}
				}
			case offlineFor > offlineThreshold && agent.Status == "online":
				staleIDs = append(staleIDs, agent.ID)
				statusEvents = append(statusEvents, db.AgentStatusEvent{AgentID: agent.ID, Status: "stale", Timestamp: now})
			}
			for _, rule := range rules {
				threshold := time.Duration(rule.Threshold) * time.Second
				if threshold <= 0 {
					threshold = m.server.offlineThreshold()
				}
				if offlineFor > threshold {
					m.triggerAlert(&rule, agent.ID, agent.Hostname,
						offlineFor.String(),
						map[string]interface{}{"agent_id": agent.ID, "hostname": agent.Hostname})
				}
			}
		}
		if len(agents) < agentReapPageSize {
			break
		}
	}
	// One batched insert (a single transaction) instead of an auto-commit per
	// agent, so a mass-drop tick does not take thousands of WAL commits.
	if len(statusEvents) > 0 {
		if err := m.server.db.Create(&statusEvents).Error; err != nil {
			slog.Error("Monitor: failed to record agent status events", "count", len(statusEvents), "err", err)
		}
	}
	m.flipAgentStatus(staleIDs, "stale")
	m.flipAgentStatus(offlineIDs, "offline")
}

// flipAgentStatus applies a bulk status flip in bounded IN-list chunks. A
// chunk that fails is logged and retried on the next tick: the agents stay in
// the sweep filter (status IN online/stale) until their row actually flips, so
// a transient error cannot permanently strand them.
func (m *MonitorCollector) flipAgentStatus(ids []string, status string) {
	for start := 0; start < len(ids); start += statusUpdateChunkSize {
		end := min(start+statusUpdateChunkSize, len(ids))
		if err := m.server.db.Model(&db.Implant{}).Where("id IN ?", ids[start:end]).Update("status", status).Error; err != nil {
			slog.Error("Monitor: failed to flip agents", "status", status, "chunk", start, "count", end-start, "err", err)
		}
	}
}

// alertTenantFor resolves which tenant an alert belongs to. An agent-sourced
// alert follows the agent, so the alert lands in the feed of the tenant that
// owns the host and nobody else sees its hostname. Server-wide alerts (source
// "system") have no agent and fall back to the rule's tenant, which keeps a
// tenant-scoped rule from announcing server health to the whole fleet while
// still letting legacy (tenant 0) rules raise global alerts.
func (m *MonitorCollector) alertTenantFor(source string, rule *db.AlertRule) uint {
	if source != "" && source != "system" {
		var agent db.Implant
		if err := m.server.db.Select("tenant_id").First(&agent, "id = ?", source).Error; err == nil {
			return agent.TenantID
		}
	}
	if rule != nil {
		return rule.TenantID
	}
	return 0
}

func (m *MonitorCollector) triggerAlert(rule *db.AlertRule, source, sourceName, value string, details map[string]interface{}) {
	var existingAlert db.Alert
	result := m.server.db.Where("rule_id = ? AND source = ? AND status = ?", rule.ID, source, "active").First(&existingAlert)

	if result.Error == nil {
		return
	}

	detailsJSON, ok := marshalJSONSafe(details)
	if !ok {
		slog.Error("Failed to marshal alert details", "rule", rule.ID, "source", source)
		return
	}

	alert := db.Alert{
		RuleID:     rule.ID,
		Type:       rule.Type,
		Severity:   m.getSeverity(rule.Type),
		Title:      rule.Name,
		Message:    fmt.Sprintf("%s: %s", rule.Name, value),
		Source:     source,
		SourceName: sourceName,
		Status:     "active",
		Details:    string(detailsJSON),
		TenantID:   m.alertTenantFor(source, rule),
	}

	if err := m.server.db.Create(&alert).Error; err != nil {
		slog.Error("Failed to create alert", "err", err)
		return
	}

	// Fan the alert out as a notification too (persisted + routed to any
	// configured Discord/Telegram/webhook channels).
	m.server.DispatchNotification(&db.Notification{
		Type:     rule.Type,
		Title:    rule.Name,
		Message:  alert.Message,
		AgentID:  source,
		Severity: alert.Severity,
	})

	m.server.triggerWebhooks(Event{
		Type:      EventType("alert." + rule.Type),
		AgentID:   source,
		AgentHost: sourceName,
		Timestamp: time.Now(),
		Data:      details,
	})

	slog.Warn("Alert triggered", "type", rule.Type, "source", source, "message", alert.Message)
}

func (m *MonitorCollector) getSeverity(ruleType string) string {
	switch ruleType {
	case "agent_offline", "cpu_high", "memory_high", "disk_high":
		return "critical"
	case "credential_found", "agent_online":
		return "info"
	default:
		return "warning"
	}
}

func (m *MonitorCollector) GetLatestMetrics() db.SystemMetric {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastMetrics
}

func (m *MonitorCollector) GetMetricsHistory() []db.SystemMetric {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]db.SystemMetric{}, m.metricsHistory...)
}

func (s *Server) handleGetSystemMetrics(c *gin.Context) {
	if s.monitorCollector == nil {
		respondError(c, http.StatusServiceUnavailable, "monitor collector not initialized")
		return
	}

	metrics := s.monitorCollector.GetLatestMetrics()

	// Safe percent calculations to avoid NaN/Infinity
	memPercent := 0.0
	if metrics.MemoryTotal > 0 {
		memPercent = math.Max(0, math.Min(100, (metrics.MemoryUsed/metrics.MemoryTotal)*100))
	}

	resp := gin.H{
		"memory": map[string]float64{
			"used":    metrics.MemoryUsed,
			"total":   metrics.MemoryTotal,
			"percent": memPercent,
		},
		"hostname":  metrics.Hostname,
		"timestamp": metrics.CreatedAt,
	}

	// Omit OS-backed readings that failed to collect (NaN) so a probe failure
	// is visible as missing data instead of a fabricated healthy 0%.
	if !math.IsNaN(metrics.CPULoad) {
		resp["cpu"] = metrics.CPULoad
	}
	if !math.IsNaN(metrics.DiskUsed) && !math.IsNaN(metrics.DiskTotal) {
		diskPercent := 0.0
		if metrics.DiskTotal > 0 {
			diskPercent = math.Max(0, math.Min(100, (metrics.DiskUsed/metrics.DiskTotal)*100))
		}
		resp["disk"] = map[string]float64{
			"used":    metrics.DiskUsed,
			"total":   metrics.DiskTotal,
			"percent": diskPercent,
		}
	}

	respond(c, resp)
}

func (s *Server) handleGetMetricsHistory(c *gin.Context) {
	if s.monitorCollector == nil {
		respondError(c, http.StatusServiceUnavailable, "monitor collector not initialized")
		return
	}

	history := s.monitorCollector.GetMetricsHistory()
	respond(c, history)
}

// loadAlertRuleScoped resolves an alert rule within the caller's tenant. Rules
// gate every alert in the feed and name the thresholds an operator watches, so
// an unscoped read/write would let one tenant retune another tenant's alerting.
func (s *Server) loadAlertRuleScoped(c *gin.Context, dest *db.AlertRule, id string) bool {
	if err := s.tenantScope(s.db, c).First(dest, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, "rule not found")
		return false
	}
	return true
}

// loadAlertScoped resolves an alert within the caller's tenant. Alerts embed the
// alerting host's hostname, so acknowledging or resolving a foreign one would
// disclose that host exists.
func (s *Server) loadAlertScoped(c *gin.Context, dest *db.Alert, id string) bool {
	if err := s.tenantScope(s.db, c).First(dest, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, "alert not found")
		return false
	}
	return true
}

func (s *Server) handleGetAlerts(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	severity := c.DefaultQuery("severity", "")

	query := s.tenantScope(s.db.Model(&db.Alert{}), c)

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if severity != "" {
		query = query.Where("severity = ?", severity)
	}

	var alerts []db.Alert
	if err := query.Preload("Rule").Order("created_at DESC").Limit(100).Find(&alerts).Error; err != nil {
		slog.Error("Failed to query alerts", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to query alerts")
		return
	}

	respond(c, gin.H{"alerts": alerts})
}

func (s *Server) handleGetAlertStats(c *gin.Context) {
	var stats struct {
		Active   int64 `json:"active"`
		Critical int64 `json:"critical"`
		Warning  int64 `json:"warning"`
		Info     int64 `json:"info"`
	}

	// The aggregate is raw SQL, and Raw().Scan() ignores any Where appended
	// afterwards, so the tenant predicate has to be part of the statement
	// itself. tenantScope cannot express it here.
	tid, ok := s.resolveTenant(c)
	if !ok {
		// Fail closed, same rule as tenantScope: a broken tenant lookup must
		// not fall back to fleet-wide counters.
		respondError(c, http.StatusUnauthorized, "tenant context unavailable")
		return
	}
	stmt := `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0) as active,
			COALESCE(SUM(CASE WHEN status = 'active' AND severity = 'critical' THEN 1 ELSE 0 END), 0) as critical,
			COALESCE(SUM(CASE WHEN status = 'active' AND severity = 'warning' THEN 1 ELSE 0 END), 0) as warning,
			COALESCE(SUM(CASE WHEN status = 'active' AND severity = 'info' THEN 1 ELSE 0 END), 0) as info
		FROM alerts`
	var args []interface{}
	if tid != 0 {
		stmt += ` WHERE tenant_id = ?`
		args = append(args, tid)
	}

	if err := s.db.Raw(stmt, args...).Scan(&stats).Error; err != nil {
		slog.Error("Failed to query alert stats", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to query alert stats")
		return
	}

	respond(c, stats)
}

func (s *Server) handleGetAlertRules(c *gin.Context) {
	p := parsePagination(c, 50, 200)
	var total int64
	if err := s.tenantScope(s.db.Model(&db.AlertRule{}), c).Count(&total).Error; err != nil {
		slog.Error("Failed to count alert rules", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to count alert rules")
		return
	}
	var rules []db.AlertRule
	if err := s.tenantScope(s.db, c).Order("created_at DESC").Offset(p.Offset).Limit(p.PageSize).Find(&rules).Error; err != nil {
		slog.Error("Failed to query alert rules", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to query alert rules")
		return
	}
	respond(c, gin.H{"rules": rules, "total": total, "page": p.Page, "page_size": p.PageSize})
}

func (s *Server) handleCreateAlertRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	// Bind an explicit allowlist instead of the model: binding db.AlertRule
	// directly let a caller set id and tenant_id (and anything added to the
	// model later) straight from the request body.
	//
	// Enabled is a pointer so an omitted field keeps the model's
	// "default:true" semantics instead of silently creating a disarmed rule.
	var req struct {
		Name        string  `json:"name"`
		Type        string  `json:"type"`
		Threshold   float64 `json:"threshold"`
		Enabled     *bool   `json:"enabled"`
		Description string  `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Alert rule operation"))
		return
	}

	if req.Name == "" || req.Type == "" {
		respondError(c, http.StatusBadRequest, "name and type are required")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	rule := db.AlertRule{
		TenantID:    s.currentTenantID(c),
		Name:        req.Name,
		Type:        req.Type,
		Threshold:   req.Threshold,
		Enabled:     enabled,
		Description: req.Description,
	}

	if err := s.db.Create(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Alert rule operation"))
		return
	}
	// AlertRule.Enabled carries `gorm:"default:true"`, and GORM substitutes the
	// column default for a zero value on insert — so an explicit
	// enabled:false was silently stored as true and the operator could never
	// create a disarmed rule. Write the flag back when it was deliberately
	// given as false.
	if req.Enabled != nil && !enabled {
		if err := s.db.Model(&db.AlertRule{}).Where("id = ?", rule.ID).Update("enabled", false).Error; err != nil {
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "Alert rule operation"))
			return
		}
		rule.Enabled = false
	}

	respond(c, gin.H{"rule": rule})
}

func (s *Server) handleUpdateAlertRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var rule db.AlertRule
	if !s.loadAlertRuleScoped(c, &rule, id) {
		return
	}

	var updates struct {
		Name        string  `json:"name"`
		Threshold   float64 `json:"threshold"`
		Enabled     bool    `json:"enabled"`
		Description string  `json:"description"`
	}

	if err := c.ShouldBindJSON(&updates); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Alert rule operation"))
		return
	}

	if updates.Name != "" {
		rule.Name = updates.Name
	}
	if updates.Description != "" {
		rule.Description = updates.Description
	}
	rule.Threshold = updates.Threshold
	rule.Enabled = updates.Enabled

	if err := s.db.Save(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Alert rule operation"))
		return
	}

	respond(c, gin.H{"rule": rule})
}

func (s *Server) handleDeleteAlertRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")

	res := s.tenantScope(s.db, c).Delete(&db.AlertRule{}, "id = ?", id)
	if res.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(res.Error, "Alert rule operation"))
		return
	}
	// An id outside the caller's tenant deletes nothing; reporting success
	// there would tell an operator their rule is gone while it is still armed.
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "rule not found")
		return
	}

	respond(c, gin.H{})
}

func (s *Server) handleAcknowledgeAlert(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var alert db.Alert
	if !s.loadAlertScoped(c, &alert, id) {
		return
	}

	alert.Status = "acknowledged"
	if err := s.db.Save(&alert).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Alert rule operation"))
		return
	}

	respond(c, gin.H{"alert": alert})
}

func (s *Server) handleResolveAlert(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var alert db.Alert
	if !s.loadAlertScoped(c, &alert, id) {
		return
	}

	alert.Status = "resolved"
	if err := s.db.Save(&alert).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Alert rule operation"))
		return
	}

	respond(c, gin.H{"alert": alert})
}

func (s *Server) handleGetAgentStatus(c *gin.Context) {
	var stats struct {
		Total   int64 `json:"total"`
		Online  int64 `json:"online"`
		Stale   int64 `json:"stale"`
		Offline int64 `json:"offline"`
	}

	// Per-agent thresholds (long-sleep agents use 3x their interval), counted
	// in Go so the math stays identical across sqlite/postgres. Column-light:
	// only what the status computation needs.
	var rows []struct {
		LastSeen        time.Time
		CurrentInterval int
	}
	if err := s.db.Model(&db.Implant{}).Select("last_seen, current_interval").Find(&rows).Error; err != nil {
		slog.Error("Failed to query agent status stats", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to query agent status stats")
		return
	}
	base := s.offlineThreshold()
	now := time.Now()
	for _, r := range rows {
		stats.Total++
		threshold := base
		if r.CurrentInterval > 0 {
			if d := 3 * time.Duration(r.CurrentInterval) * time.Second; d > threshold {
				threshold = d
			}
		}
		since := now.Sub(r.LastSeen)
		switch {
		case since < threshold:
			stats.Online++
		case since < threshold*StaleThresholdMultiplier:
			stats.Stale++
		default:
			stats.Offline++
		}
	}

	respond(c, stats)
}

func (s *Server) TriggerAlertForEvent(evt Event) {
	if s.monitorCollector == nil {
		return
	}

	var rules []db.AlertRule
	switch evt.Type {
	case EventCredentialFound:
		if err := s.db.Where("enabled = ? AND type = ?", true, "credential_found").Limit(200).Find(&rules).Error; err != nil {
			slog.Error("Monitor: failed to query credential_found rules", "err", err)
		}
	case EventImplantCheckin:
		if err := s.db.Where("enabled = ? AND type = ?", true, "agent_online").Limit(200).Find(&rules).Error; err != nil {
			slog.Error("Monitor: failed to query agent_online rules", "err", err)
		}
	case EventImplantDisconnect:
		if err := s.db.Where("enabled = ? AND type = ?", true, "agent_offline").Limit(200).Find(&rules).Error; err != nil {
			slog.Error("Monitor: failed to query agent_offline rules", "err", err)
		}
	}

	for _, rule := range rules {
		s.monitorCollector.triggerAlert(&rule, evt.AgentID, evt.AgentHost,
			string(evt.Type), evt.Data)
	}
}

func (s *Server) handleScreenMonitorPage(c *gin.Context) {
	id := c.Param("id")

	// Tenant scope: this page renders agent identity/hostname, and the JSON
	// branch exposes the same fields — an unscoped read was a cross-tenant
	// disclosure (mirrors the shell page fix).
	var agent db.Implant
	q := s.tenantScope(s.db.Model(&db.Implant{}), c)
	if err := q.First(&agent, "id = ?", id).Error; err != nil {
		c.Redirect(http.StatusFound, "/agents")
		return
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - Screen Monitoring",
		"Agent":     agent,
		"ActiveNav": "agents",
		"Online":    time.Since(agent.LastSeen) < s.offlineThreshold(),
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleStartScreenMonitor(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")

	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	var request struct {
		Interval string `json:"interval" form:"interval"`
		Quality  string `json:"quality" form:"quality"`
		Width    string `json:"width" form:"width"`
		Mime     string `json:"mime" form:"mime"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBind(&request); err != nil {
			respondError(c, http.StatusBadRequest, "invalid screen monitor settings")
			return
		}
	}

	interval := strings.TrimSpace(request.Interval)
	if interval == "" {
		interval = c.Query("interval")
	}
	quality := strings.TrimSpace(strings.ToLower(request.Quality))
	if quality == "" {
		quality = strings.TrimSpace(strings.ToLower(c.Query("quality")))
	}
	widthStr := strings.TrimSpace(request.Width)
	if widthStr == "" {
		widthStr = strings.TrimSpace(c.Query("width"))
	}
	if interval == "" {
		interval = "5"
	}
	if quality == "" {
		quality = "medium"
	}
	intervalSeconds, err := strconv.Atoi(interval)
	if err != nil || intervalSeconds < 1 || intervalSeconds > 60 {
		respondError(c, http.StatusBadRequest, "interval must be between 1 and 60 seconds")
		return
	}
	if quality != "low" && quality != "medium" && quality != "high" {
		qualityValue, qualityErr := strconv.Atoi(quality)
		if qualityErr != nil || qualityValue < 1 || qualityValue > 100 {
			respondError(c, http.StatusBadRequest, "quality must be low, medium, high, or a value from 1 to 100")
			return
		}
		quality = strconv.Itoa(qualityValue)
	}
	interval = strconv.Itoa(intervalSeconds)

	// Width is spliced into the agent's CSV stream command ("interval,quality
	// [,width][,mime]"), so an arbitrary string was parameter injection into
	// the agent-side parser. Validate and canonicalize it here.
	width := 0
	if widthStr != "" {
		widthValue, widthErr := strconv.Atoi(widthStr)
		if widthErr != nil || widthValue < MinScreenMonitorWidth || widthValue > MaxScreenMonitorWidth {
			respondError(c, http.StatusBadRequest,
				fmt.Sprintf("width must be an integer between %d and %d", MinScreenMonitorWidth, MaxScreenMonitorWidth))
			return
		}
		width = widthValue
	}

	mimeVal := strings.ToLower(strings.TrimSpace(request.Mime))
	if mimeVal == "" {
		mimeVal = strings.ToLower(strings.TrimSpace(c.Query("mime")))
	}
	if mimeVal != "" && mimeVal != "webp" && mimeVal != "jpeg" && mimeVal != "png" {
		respondError(c, http.StatusBadRequest, "mime must be webp, jpeg, or png")
		return
	}
	if mimeVal != "" && width == 0 {
		// The agent parses the command positionally
		// ("interval,quality[,width][,mime]"), so a mime without a width
		// would land in the width slot.
		respondError(c, http.StatusBadRequest, "mime requires width")
		return
	}

	monitorKey := strings.ToLower(id)
	// Single-flight reservation, then release the registry lock before any DB
	// write: issueAgentTask runs plugin hooks and audit writes, and holding
	// screenMonitorMu across them would stall frame ingest (which takes the
	// same lock on every screen_frame result).
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	if _, active := s.screenMonitorImplants[monitorKey]; active {
		s.screenMonitorMu.Unlock()
		respond(c, gin.H{
			"active":         true,
			"already_active": true,
			"message":        "Screen stream is already active",
		})
		return
	}
	// Reap dead streams first: a crashed agent must not hold a slot (or be
	// reported as still streaming) forever.
	if dropped := s.sweepScreenMonitorsLocked(time.Now()); dropped > 0 {
		slog.Info("Dropped stale screen monitoring entries", "dropped", dropped)
	}
	if len(s.screenMonitorImplants) >= MaxScreenMonitors {
		s.screenMonitorMu.Unlock()
		respondError(c, http.StatusTooManyRequests, "screen monitor limit reached")
		return
	}
	if op := s.screenMonitorOps[monitorKey]; op != "" {
		s.screenMonitorMu.Unlock()
		respondError(c, http.StatusConflict, "screen monitor "+op+" already in progress for this agent")
		return
	}
	s.screenMonitorOps[monitorKey] = "start"
	s.screenMonitorMu.Unlock()

	streamCmd := interval + "," + quality
	if width > 0 {
		streamCmd += "," + strconv.Itoa(width)
	}
	if mimeVal != "" {
		streamCmd += "," + mimeVal
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "screen_stream_start", Command: streamCmd})
	if task == nil {
		s.screenMonitorMu.Lock()
		delete(s.screenMonitorOps, monitorKey)
		s.screenMonitorMu.Unlock()
		return
	}
	s.screenMonitorMu.Lock()
	delete(s.screenMonitorOps, monitorKey)
	s.screenMonitorImplants[monitorKey] = time.Now()
	s.screenMonitorMu.Unlock()

	s.broadcastTaskUpdate(id, *task)

	s.LogAuditRecord(c, "screen_monitor_start", "agent", id, "Started screen monitoring", true, nil)
	slog.Info("Screen monitoring started", "agent_id", id, "task_id", task.ID)
	respond(c, gin.H{
		"active":   true,
		"task_id":  task.ID,
		"interval": intervalSeconds,
		"quality":  quality,
		"message":  "Screen stream started",
	})
}

// ensureScreenMonitorMapsLocked initializes the monitor maps for bare test
// servers that construct Server literals. Callers must hold screenMonitorMu.
func (s *Server) ensureScreenMonitorMapsLocked() {
	if s.screenMonitorImplants == nil {
		s.screenMonitorImplants = make(map[string]time.Time)
	}
	if s.screenMonitorOps == nil {
		s.screenMonitorOps = make(map[string]string)
	}
}

type cachedScreenFrame struct {
	data       string
	mime       string
	capturedAt time.Time
}

func (s *Server) handleStopScreenMonitor(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	monitorKey := strings.ToLower(id)
	// Single-flight reservation, same as start: never hold screenMonitorMu
	// across task creation/broadcast/audit.
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	if _, active := s.screenMonitorImplants[monitorKey]; !active {
		s.screenMonitorMu.Unlock()
		respond(c, gin.H{
			"active":  false,
			"message": "Screen stream is already stopped",
		})
		return
	}
	if op := s.screenMonitorOps[monitorKey]; op != "" {
		s.screenMonitorMu.Unlock()
		respondError(c, http.StatusConflict, "screen monitor "+op+" already in progress for this agent")
		return
	}
	s.screenMonitorOps[monitorKey] = "stop"
	s.screenMonitorMu.Unlock()

	stopTask := s.issueAgentTask(c, id, TaskSpec{Type: "screen_stream_stop"})
	if stopTask == nil {
		s.screenMonitorMu.Lock()
		delete(s.screenMonitorOps, monitorKey)
		s.screenMonitorMu.Unlock()
		return
	}
	s.screenMonitorMu.Lock()
	delete(s.screenMonitorImplants, monitorKey)
	delete(s.screenMonitorOps, monitorKey)
	s.screenMonitorMu.Unlock()

	s.broadcastTaskUpdate(id, *stopTask)

	s.LogAuditRecord(c, "screen_monitor_stop", "agent", id, "Stopped screen monitoring", true, nil)
	slog.Info("Screen monitoring stopped", "agent_id", id)
	respond(c, gin.H{"active": false, "task_id": stopTask.ID})
}

// screenMonitorStaleAfter is the heartbeat timeout of a screen stream: every
// frame refreshes the entry, so anything older than this belongs to a stream
// that died without a stop (agent crash, console closed).
//
// Without this maintenance the entry stayed forever, which meant
// IsScreenMonitoring() kept returning true for a dead stream and saveScreenshot
// silently refused to persist that agent's screenshots from then on - the
// operator just saw screenshots stop appearing. Stale entries also counted
// against MaxScreenMonitors, eventually refusing new streams.
const screenMonitorStaleAfter = 2 * time.Minute

// sweepScreenMonitorsLocked drops stale stream entries and reports how many were
// removed. Callers must hold screenMonitorMu.
func (s *Server) sweepScreenMonitorsLocked(now time.Time) int {
	cutoff := now.Add(-screenMonitorStaleAfter)
	dropped := 0
	for key, lastFrame := range s.screenMonitorImplants {
		if lastFrame.Before(cutoff) {
			delete(s.screenMonitorImplants, key)
			dropped++
		}
	}
	return dropped
}

func (s *Server) IsScreenMonitoring(agentID string) bool {
	s.screenMonitorMu.Lock()
	defer s.screenMonitorMu.Unlock()
	key := strings.ToLower(agentID)
	lastFrame, ok := s.screenMonitorImplants[key]
	if !ok {
		return false
	}
	if time.Since(lastFrame) > screenMonitorStaleAfter {
		delete(s.screenMonitorImplants, key)
		slog.Info("Dropped a stale screen monitoring entry", "agent_id", key, "last_frame", lastFrame)
		return false
	}
	return true
}

func (s *Server) BroadcastScreenshot(agentID string, base64Data string) {
	mime := "image/png"
	if strings.HasPrefix(base64Data, "/9j/") {
		mime = "image/jpeg"
	}
	capturedAt := time.Now().UTC()
	monitorKey := strings.ToLower(agentID)
	s.screenMonitorMu.Lock()
	if _, active := s.screenMonitorImplants[monitorKey]; active {
		// A frame is also the monitor heartbeat. Without this refresh, a healthy
		// long-running stream could be removed by stale-map maintenance.
		s.screenMonitorImplants[monitorKey] = capturedAt
	}
	s.screenMonitorMu.Unlock()
	s.screenFrameMu.Lock()
	if s.screenFrames == nil {
		s.screenFrames = make(map[string]cachedScreenFrame)
	}
	s.screenFrames[monitorKey] = cachedScreenFrame{
		data:       base64Data,
		mime:       mime,
		capturedAt: capturedAt,
	}
	s.screenFrameMu.Unlock()

	payload := map[string]string{
		"type":        "screenshot",
		"agent_id":    agentID,
		"data":        base64Data,
		"mime":        mime,
		"captured_at": capturedAt.Format(time.RFC3339Nano),
	}
	message, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal screenshot payload", "err", err)
		return
	}

	s.broadcastScreenshotToTenant(message, agentID)
}

func (s *Server) latestScreenFrame(agentID string) (cachedScreenFrame, bool) {
	s.screenFrameMu.RLock()
	defer s.screenFrameMu.RUnlock()
	frame, ok := s.screenFrames[strings.ToLower(agentID)]
	return frame, ok
}

// BroadcastScreenMonitorError notifies subscribed consoles when a stream that
// was already running fails on the agent. The monitor slot is released so the
// operator can retry immediately instead of being blocked by stale server state.
func (s *Server) BroadcastScreenMonitorError(agentID, errorMessage string) {
	s.screenMonitorMu.Lock()
	delete(s.screenMonitorImplants, strings.ToLower(agentID))
	s.screenMonitorMu.Unlock()

	payload := map[string]string{
		"type":     "screen_stream_error",
		"agent_id": agentID,
		"error":    errorMessage,
	}
	message, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal screen monitor error", "agent_id", agentID, "err", err)
		return
	}
	s.broadcastToTenant(message, agentID)
}

// handleAgentRemoteInput accepts remote desktop input events and queues a remote_input task.
// Payload JSON: {"type":"move|click|key","x":0,"y":0,"key":""}
// Agent-side dispatch: Windows (SendInput-equivalent win32 calls), Linux
// (xdotool/ydotool), macOS (osascript keystrokes).
func (s *Server) handleAgentRemoteInput(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	var req remoteInputRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid json: expected {type, x, y, key}")
		return
	}
	if err := req.normalize(); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Throttle per (operator, agent): each accepted event becomes a task row
	// plus an audit row, so an unbounded client must not be able to drive the
	// write path at line speed. pushback is reported as 429, not silently
	// dropped, so a mis-throttled client can tell.
	if userID, ok := currentUserID(c); ok {
		if !s.remoteInputLimiter.allow(strconv.FormatUint(uint64(userID), 10)+":"+strings.ToLower(id),
			MaxRemoteInputEventsPerWindow, RemoteInputWindow, time.Now()) {
			respondError(c, http.StatusTooManyRequests, "remote input rate limit exceeded")
			return
		}
	}

	payload, ok := marshalJSONSafe(req)
	if !ok {
		slog.Error("Remote input: failed to marshal request", "agent_id", id, "type", req.Type)
		respondError(c, http.StatusInternalServerError, "failed to marshal request")
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "remote_input", Command: string(payload)})
	if task == nil {
		return
	}

	s.LogAuditRecord(c, "remote_input", "agent", id, fmt.Sprintf("Remote input: %s", req.Type), true, nil)
	s.broadcastTaskUpdate(id, *task)
	respond(c, gin.H{"task_id": task.ID, "message": "remote input queued"})
}

func (s *Server) handleScreenFrame(c *gin.Context) {
	raw, err := c.GetRawData()
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to read body")
		return
	}

	env, req, kind := s.decodeBeaconEnvelope(raw)
	if kind != frameEncrypted {
		slog.Warn("Screen frame rejected: not a v2 encrypted frame", "agent_id", env.UUID)
		respondError(c, http.StatusUnauthorized, "authentication failed")
		return
	}

	for _, r := range req.Results {
		if r.Type == "screen_frame" && r.Output != "" {
			if s.IsScreenMonitoring(req.UUID) {
				s.BroadcastScreenshot(req.UUID, r.Output)
			}
		}
	}

	respond(c, gin.H{"ok": true})
}

// handleGetRekeyStats reports crypto session rekey activity across live
// agents (counts and timestamps only — no key material).
func (s *Server) handleGetRekeyStats(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	if s.sessionManager == nil {
		respondError(c, http.StatusServiceUnavailable, "session manager not initialized")
		return
	}
	respond(c, s.sessionManager.Stats())
}
