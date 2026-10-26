package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// integrationConfig stores type-specific secrets in WebhookConfig.Headers as JSON.
type integrationConfig struct {
	Type      string `json:"type"`
	Secret    string `json:"secret,omitempty"`
	To        string `json:"to,omitempty"`
	SMTPHost  string `json:"smtp_host,omitempty"`
	SMTPPort  int    `json:"smtp_port,omitempty"`
	SMTPUser  string `json:"smtp_user,omitempty"`
	SMTPPass  string `json:"smtp_pass,omitempty"`
	From      string `json:"from,omitempty"`
	EventType string `json:"event_type,omitempty"`
}

func parseIntegrationConfig(headers string) integrationConfig {
	var cfg integrationConfig
	if headers == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(headers), &cfg); err != nil {
		slog.Warn("Failed to parse integration config", "error", err)
	}
	return cfg
}

func marshalIntegrationConfig(cfg integrationConfig) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func integrationToMap(wh db.WebhookConfig) map[string]interface{} {
	cfg := parseIntegrationConfig(wh.Headers)
	typ := cfg.Type
	if typ == "" {
		typ = wh.EventType
	}
	if typ == "" {
		typ = "webhook"
	}
	status := "ok"
	if !wh.Enabled {
		status = "disabled"
	}
	lastTrigger := ""
	if wh.LastTrigger != nil {
		lastTrigger = wh.LastTrigger.UTC().Format(time.RFC3339)
	}
	return map[string]interface{}{
		"id":           wh.ID,
		"type":         typ,
		"name":         wh.Name,
		"enabled":      wh.Enabled,
		"endpoint":     wh.URL,
		"event_count":  wh.EventCount,
		"last_trigger": lastTrigger,
		"status":       status,
		"event_type":   wh.EventType,
		"method":       wh.Method,
		"created_at":   wh.CreatedAt,
		"updated_at":   wh.UpdatedAt,
		"configured":   wh.URL != "" || cfg.SMTPHost != "",
	}
}

// handleIntegrationsList returns configured integrations from DB plus capability catalog.
func (s *Server) handleIntegrationsList(c *gin.Context) {
	var webhooks []db.WebhookConfig
	// The integrations view reads the same webhook table as the automation
	// webhook handlers, so it must apply the same tenant scope: an unscoped
	// list showed other tenants' targets (and their config secrets).
	if err := s.tenantScope(s.db, c).Order("updated_at desc").Limit(200).Find(&webhooks).Error; err != nil {
		slog.Error("Failed to list webhooks for integrations", "err", err)
	}

	integrations := make([]map[string]interface{}, 0, len(webhooks)+1)
	for _, wh := range webhooks {
		integrations = append(integrations, integrationToMap(wh))
	}

	// Surface Slack from server config if present and not already stored
	s.configMu.RLock()
	slackCfg := s.cfg.Integrations.Slack
	s.configMu.RUnlock()
	if slackCfg.Enabled || slackCfg.BotToken != "" {
		found := false
		for _, it := range integrations {
			if it["type"] == "slack" && it["name"] == "Slack (config)" {
				found = true
				break
			}
		}
		if !found {
			integrations = append(integrations, map[string]interface{}{
				"id":           0,
				"type":         "slack",
				"name":         "Slack (config)",
				"enabled":      slackCfg.Enabled,
				"endpoint":     "",
				"event_count":  0,
				"last_trigger": "",
				"status":       map[bool]string{true: "ok", false: "disabled"}[slackCfg.Enabled],
				"configured":   slackCfg.BotToken != "" && slackCfg.AppToken != "",
				"readonly":     true,
			})
		}
	}

	respond(c, gin.H{"integrations": integrations})
}

func (s *Server) handleIntegrationsCreate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		Type      string `json:"type" binding:"required"`
		Name      string `json:"name" binding:"required"`
		URL       string `json:"url"`
		Secret    string `json:"secret"`
		To        string `json:"to"`
		SMTPHost  string `json:"smtp_host"`
		SMTPPort  int    `json:"smtp_port"`
		SMTPUser  string `json:"smtp_user"`
		SMTPPass  string `json:"smtp_pass"`
		From      string `json:"from"`
		EventType string `json:"event_type"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if req.EventType == "" {
		req.EventType = "all"
	}
	if req.Type != "email" && req.URL != "" {
		if err := validateWebhookURL(req.URL); err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "Integration"))
			return
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	cfg := integrationConfig{
		Type:      req.Type,
		Secret:    req.Secret,
		To:        req.To,
		SMTPHost:  req.SMTPHost,
		SMTPPort:  req.SMTPPort,
		SMTPUser:  req.SMTPUser,
		SMTPPass:  req.SMTPPass,
		From:      req.From,
		EventType: req.EventType,
	}
	wh := db.WebhookConfig{
		Name:      req.Name,
		URL:       req.URL,
		EventType: req.EventType,
		Method:    "POST",
		Headers:   marshalIntegrationConfig(cfg),
		Enabled:   enabled,
		// Stamp the caller's tenant: webhook delivery matches on tenant_id, so
		// an unstamped row (0) silently never fires for a tenant-scoped event.
		TenantID:  s.currentTenantID(c),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&wh).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Integration"))
		return
	}
	s.LogAuditRecord(c, "create_integration", "integration", strconv.FormatUint(uint64(wh.ID), 10), req.Name, true, nil)
	respond(c, gin.H{"integration": integrationToMap(wh)})
}

func (s *Server) handleIntegrationsUpdate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var wh db.WebhookConfig
	if !s.findTenantOrFail(c, &wh, id, "integration") {
		return
	}
	var req struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		URL       string `json:"url"`
		Secret    string `json:"secret"`
		To        string `json:"to"`
		SMTPHost  string `json:"smtp_host"`
		SMTPPort  int    `json:"smtp_port"`
		SMTPUser  string `json:"smtp_user"`
		SMTPPass  string `json:"smtp_pass"`
		From      string `json:"from"`
		EventType string `json:"event_type"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	cfg := parseIntegrationConfig(wh.Headers)
	if req.Type != "" {
		cfg.Type = strings.ToLower(req.Type)
	}
	if req.Secret != "" {
		cfg.Secret = req.Secret
	}
	if req.To != "" {
		cfg.To = req.To
	}
	if req.SMTPHost != "" {
		cfg.SMTPHost = req.SMTPHost
	}
	if req.SMTPPort > 0 {
		cfg.SMTPPort = req.SMTPPort
	}
	if req.SMTPUser != "" {
		cfg.SMTPUser = req.SMTPUser
	}
	if req.SMTPPass != "" {
		cfg.SMTPPass = req.SMTPPass
	}
	if req.From != "" {
		cfg.From = req.From
	}
	if req.EventType != "" {
		cfg.EventType = req.EventType
		wh.EventType = req.EventType
	}
	if req.Name != "" {
		wh.Name = req.Name
	}
	if req.URL != "" {
		if err := validateWebhookURL(req.URL); err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "Integration"))
			return
		}
		wh.URL = req.URL
	}
	if req.Enabled != nil {
		wh.Enabled = *req.Enabled
	}
	wh.Headers = marshalIntegrationConfig(cfg)
	wh.UpdatedAt = time.Now()
	if err := s.db.Save(&wh).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Integration"))
		return
	}
	s.LogAuditRecord(c, "update_integration", "integration", id, wh.Name, true, nil)
	respond(c, gin.H{"integration": integrationToMap(wh)})
}

func (s *Server) handleIntegrationsDelete(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	// Tenant-scoped delete: an id outside the caller's tenant deletes nothing
	// and reports 404 instead of silently succeeding.
	res := s.tenantScope(s.db, c).Delete(&db.WebhookConfig{}, "id = ?", id)
	if res.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(res.Error, "Integration"))
		return
	}
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "integration not found")
		return
	}
	s.LogAuditRecord(c, "delete_integration", "integration", id, "", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleIntegrationsToggle(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var wh db.WebhookConfig
	if !s.findTenantOrFail(c, &wh, id, "integration") {
		return
	}
	wh.Enabled = !wh.Enabled
	wh.UpdatedAt = time.Now()
	if err := s.db.Save(&wh).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Integration"))
		return
	}
	respond(c, gin.H{"integration": integrationToMap(wh)})
}

// handleActiveMalleable and GET /integrations/malleable were removed. The
// response was a partial view of the same config that GET /settings returns
// (missing request_prepend/append, headers, placements, resp_jitter), and the
// UI abandoned it in favour of /settings: the toggle on that card always
// rendered as disabled. The Malleable editor writes through
// POST /settings/malleable, which is unchanged.
