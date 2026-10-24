package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var aiProfileTestLimiter = struct {
	sync.Mutex
	hits map[string][]time.Time
}{hits: make(map[string][]time.Time)}

func aiProfileTestAllowed(key string) bool {
	aiProfileTestLimiter.Lock()
	defer aiProfileTestLimiter.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	hits := aiProfileTestLimiter.hits[key]
	// prune
	n := 0
	for _, t := range hits {
		if t.After(cutoff) {
			hits[n] = t
			n++
		}
	}
	hits = hits[:n]
	if len(hits) >= 5 {
		aiProfileTestLimiter.hits[key] = hits
		return false
	}
	hits = append(hits, now)
	aiProfileTestLimiter.hits[key] = hits
	return true
}

type aiProfileView struct {
	ID                  uint       `json:"id"`
	Name                string     `json:"name"`
	Provider            string     `json:"provider"`
	Model               string     `json:"model"`
	Endpoint            string     `json:"endpoint,omitempty"`
	ContextLimit        int        `json:"context_limit"`
	OutputLimit         int        `json:"output_limit"`
	SupportsReasoning   bool       `json:"supports_reasoning"`
	SupportsTools       bool       `json:"supports_tools"`
	Enabled             bool       `json:"enabled"`
	IsDefault           bool       `json:"is_default"`
	HasAPIKey           bool       `json:"has_api_key"`
	LastHealthStatus    string     `json:"last_health_status,omitempty"`
	LastHealthError     string     `json:"last_health_error,omitempty"`
	LastHealthLatencyMS int64      `json:"last_health_latency_ms,omitempty"`
	LastCheckedAt       *time.Time `json:"last_checked_at,omitempty"`
}

func makeAIProfileView(profile db.AIProviderProfile, includeDiagnostics bool) aiProfileView {
	view := aiProfileView{
		ID: profile.ID, Name: profile.Name, Provider: profile.Provider, Model: profile.Model,
		Endpoint: profile.Endpoint, ContextLimit: profile.ContextLimit, OutputLimit: profile.OutputLimit,
		SupportsReasoning: profile.SupportsReasoning, SupportsTools: profile.SupportsTools,
		Enabled: profile.Enabled, IsDefault: profile.IsDefault, HasAPIKey: strings.TrimSpace(profile.APIKey) != "",
	}
	if includeDiagnostics {
		view.LastHealthStatus = profile.LastHealthStatus
		view.LastHealthError = profile.LastHealthError
		view.LastHealthLatencyMS = profile.LastHealthLatency
		view.LastCheckedAt = profile.LastCheckedAt
	}
	return view
}

func (s *Server) handleAIProfilesList(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	var profiles []db.AIProviderProfile
	query := s.db.Where("tenant_id = ?", principal.TenantID)
	if !principal.hasPermission(s.db, db.PermAIConfigure) {
		query = query.Where("enabled = ?", true)
	}
	if err := query.Order("is_default DESC, name ASC").Find(&profiles).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to list AI profiles")
		return
	}
	views := make([]aiProfileView, 0, len(profiles))
	for _, profile := range profiles {
		view := makeAIProfileView(profile, principal.hasPermission(s.db, db.PermAIConfigure))
		if !principal.hasPermission(s.db, db.PermAIConfigure) {
			view.Endpoint = ""
		}
		views = append(views, view)
	}
	respond(c, views)
}

type aiProfileInput struct {
	Name              string `json:"name"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	Endpoint          string `json:"endpoint"`
	APIKey            string `json:"api_key"`
	ContextLimit      int    `json:"context_limit"`
	OutputLimit       int    `json:"output_limit"`
	SupportsReasoning bool   `json:"supports_reasoning"`
	SupportsTools     *bool  `json:"supports_tools"`
	Enabled           *bool  `json:"enabled"`
	IsDefault         bool   `json:"is_default"`
}

// normalizeAIProfileInput validates and canonicalises a provider profile. It is
// a Server method so endpoint validation can consult the AI allowlist, which
// lives in the running config.
func (s *Server) normalizeAIProfileInput(req aiProfileInput) (aiProfileInput, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Model = strings.TrimSpace(req.Model)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Name == "" || len(req.Name) > 120 || req.Model == "" || len(req.Model) > 200 {
		return req, fmt.Errorf("name and model are required")
	}
	// Shared registry, same as config.Validate and handleAIConfig.
	if !config.IsKnownAIProvider(req.Provider) {
		return req, fmt.Errorf("unsupported provider")
	}
	if req.Endpoint == "" && config.AIProviderNeedsExplicitEndpoint(req.Provider) {
		return req, fmt.Errorf("endpoint is required when provider is %q", req.Provider)
	}
	if req.Endpoint != "" {
		if len(req.Endpoint) > 2048 {
			return req, fmt.Errorf("endpoint too long")
		}
		if err := s.validateAIEndpoint(req.Endpoint); err != nil {
			return req, fmt.Errorf("endpoint blocked: %w", err)
		}
	}
	if req.ContextLimit <= 0 {
		req.ContextLimit = 48000
	}
	if req.OutputLimit <= 0 {
		req.OutputLimit = 4096
	}
	if req.ContextLimit > 2_000_000 || req.OutputLimit > 200_000 {
		return req, fmt.Errorf("model limits are too large")
	}
	return req, nil
}

func (s *Server) handleAIProfileCreate(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok || !principal.hasPermission(s.db, db.PermAIConfigure) {
		respondError(c, http.StatusForbidden, "AI configure permission required")
		return
	}
	var req aiProfileInput
	if err := bindLimitedAISessionJSON(c, 32*1024, &req); err != nil {
		respondAISessionBindError(c, err)
		return
	}
	var err error
	if req, err = s.normalizeAIProfileInput(req); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.APIKey) == "" {
		respondError(c, http.StatusBadRequest, "api_key is required")
		return
	}
	profile := db.AIProviderProfile{
		TenantID: principal.TenantID, Name: req.Name, Provider: req.Provider, Model: req.Model,
		Endpoint: req.Endpoint, APIKey: strings.TrimSpace(req.APIKey), ContextLimit: req.ContextLimit,
		OutputLimit: req.OutputLimit, SupportsReasoning: req.SupportsReasoning,
		SupportsTools: req.SupportsTools == nil || *req.SupportsTools,
		Enabled:       req.Enabled == nil || *req.Enabled, IsDefault: req.IsDefault,
		CreatedBy: principal.Username,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if profile.IsDefault {
			if err := tx.Model(&db.AIProviderProfile{}).Where("tenant_id = ?", principal.TenantID).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&profile).Error
	}); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create AI profile")
		return
	}
	var stored db.AIProviderProfile
	s.db.Where("id = ?", profile.ID).First(&stored)
	s.LogOperatorAction(c, OperatorAction{Action: "ai_profile_create", Resource: "ai_profile", TargetID: fmt.Sprint(profile.ID), RiskLevel: "medium", Details: profile.Name})
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": makeAIProfileView(stored, true)})
}

func (s *Server) loadTenantAIProfile(c *gin.Context, principal aiPrincipal) (db.AIProviderProfile, bool) {
	var profile db.AIProviderProfile
	if err := s.db.Where("id = ? AND tenant_id = ?", c.Param("id"), principal.TenantID).First(&profile).Error; err != nil {
		respondError(c, http.StatusNotFound, "AI profile not found")
		return profile, false
	}
	return profile, true
}

func (s *Server) handleAIProfileUpdate(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok || !principal.hasPermission(s.db, db.PermAIConfigure) {
		respondError(c, http.StatusForbidden, "AI configure permission required")
		return
	}
	profile, ok := s.loadTenantAIProfile(c, principal)
	if !ok {
		return
	}
	var req aiProfileInput
	if err := bindLimitedAISessionJSON(c, 32*1024, &req); err != nil {
		respondAISessionBindError(c, err)
		return
	}
	var err error
	if req, err = s.normalizeAIProfileInput(req); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	profile.Name, profile.Provider, profile.Model, profile.Endpoint = req.Name, req.Provider, req.Model, req.Endpoint
	profile.ContextLimit, profile.OutputLimit, profile.SupportsReasoning = req.ContextLimit, req.OutputLimit, req.SupportsReasoning
	if req.APIKey != "" {
		profile.APIKey = strings.TrimSpace(req.APIKey)
	}
	if req.SupportsTools != nil {
		profile.SupportsTools = *req.SupportsTools
	}
	if req.Enabled != nil {
		profile.Enabled = *req.Enabled
	}
	profile.IsDefault = req.IsDefault
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if profile.IsDefault {
			if err := tx.Model(&db.AIProviderProfile{}).Where("tenant_id = ? AND id <> ?", principal.TenantID, profile.ID).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Save(&profile).Error
	}); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update AI profile")
		return
	}
	var stored db.AIProviderProfile
	s.db.Where("id = ?", profile.ID).First(&stored)
	respond(c, makeAIProfileView(stored, true))
}

func (s *Server) handleAIProfileDelete(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok || !principal.hasPermission(s.db, db.PermAIConfigure) {
		respondError(c, http.StatusForbidden, "AI configure permission required")
		return
	}
	profile, ok := s.loadTenantAIProfile(c, principal)
	if !ok {
		return
	}
	var references int64
	s.db.Model(&db.AIChatSession{}).Where("profile_id = ?", profile.ID).Count(&references)
	if references > 0 {
		respondError(c, http.StatusConflict, "profile is used by existing sessions; disable it instead")
		return
	}
	if err := s.db.Delete(&profile).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete AI profile")
		return
	}
	s.LogOperatorAction(c, OperatorAction{Action: "ai_profile_delete", Resource: "ai_profile", TargetID: fmt.Sprint(profile.ID), RiskLevel: "high", Details: profile.Name})
	respond(c, gin.H{})
}

func (s *Server) handleAIProfileTest(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok || !principal.hasPermission(s.db, db.PermAIConfigure) {
		respondError(c, http.StatusForbidden, "AI configure permission required")
		return
	}
	if !aiProfileTestAllowed(fmt.Sprintf("%d:%d", principal.TenantID, principal.UserID)) {
		respondError(c, http.StatusTooManyRequests, "too many health checks; try again in a minute")
		return
	}
	profile, ok := s.loadTenantAIProfile(c, principal)
	if !ok {
		return
	}
	endpoint := profile.Endpoint
	if endpoint == "" {
		endpoint = aiDefaultEndpoint(profile.Provider)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	body := chatRequest{Model: profile.Model, Messages: []chatMessage{{Role: "user", Content: "Reply with OK."}}, Stream: false, MaxTokens: 8}
	payload, _ := json.Marshal(body)
	resp, err := s.aiDoRequestWithConfig(ctx, payload, aiProviderRequestConfig{
		enabled: true, provider: profile.Provider, endpoint: endpoint, apiKey: profile.APIKey, model: profile.Model,
	})
	latency := time.Since(start).Milliseconds()
	status, healthErr := "healthy", ""
	if err == nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err = fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
	}
	if err != nil {
		status, healthErr = "unhealthy", truncateString(err.Error(), 500)
	}
	now := time.Now()
	s.db.Model(&db.AIProviderProfile{}).Where("id = ?", profile.ID).Updates(map[string]interface{}{
		"last_health_status": status, "last_health_error": healthErr,
		"last_health_latency": latency, "last_checked_at": now,
	})
	code := http.StatusOK
	if err != nil {
		code = http.StatusBadGateway
	}
	c.JSON(code, gin.H{"success": err == nil, "data": gin.H{"status": status, "latency_ms": latency, "error": healthErr}})
}
