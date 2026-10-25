package server

import (
	"log/slog"
	"net/http"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

type siemRuleInput struct {
	Name         string `json:"name"`
	Enabled      *bool  `json:"enabled"`
	Action       string `json:"action"`
	WindowSec    int    `json:"window_sec"`
	Threshold    int    `json:"threshold"`
	AlertAction  string `json:"alert_action"`
	AlertDetails string `json:"alert_details"`
}

func (s *Server) handleListSIEMRules(c *gin.Context) {
	var rules []db.SIEMRule
	if err := s.db.Order("id asc").Find(&rules).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "List SIEM rules"))
		return
	}
	if rules == nil {
		rules = []db.SIEMRule{}
	}
	respond(c, gin.H{"rules": rules})
}

func (s *Server) handleCreateSIEMRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req siemRuleInput
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if msg := validateSIEMRule(req); msg != "" {
		respondError(c, http.StatusBadRequest, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := db.SIEMRule{
		Name:         req.Name,
		Enabled:      enabled,
		Action:       req.Action,
		WindowSec:    req.WindowSec,
		Threshold:    req.Threshold,
		AlertAction:  req.AlertAction,
		AlertDetails: req.AlertDetails,
	}
	if err := s.db.Create(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Create SIEM rule"))
		return
	}
	// SIEMRule.Enabled carries `gorm:"default:true"`, and GORM substitutes the
	// column default for a zero value on insert — so an explicit
	// enabled:false was silently stored as true and an operator could not create
	// a paused rule. Write the flag back when it was deliberately given as false.
	if !enabled {
		if err := s.db.Model(&db.SIEMRule{}).Where("id = ?", rule.ID).Update("enabled", false).Error; err != nil {
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "Create SIEM rule"))
			return
		}
		rule.Enabled = false
	}
	s.reloadSIEMRules()
	s.LogAuditRecord(c, "siem_rule_create", "siem", rule.Name, "SIEM correlation rule created", true, nil)
	respond(c, gin.H{"rule": rule})
}

func (s *Server) handleUpdateSIEMRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var rule db.SIEMRule
	if err := s.db.First(&rule, c.Param("id")).Error; err != nil {
		respondError(c, http.StatusNotFound, "Rule not found")
		return
	}
	var req siemRuleInput
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if msg := validateSIEMRule(req); msg != "" {
		respondError(c, http.StatusBadRequest, msg)
		return
	}
	rule.Name = req.Name
	rule.Action = req.Action
	rule.WindowSec = req.WindowSec
	rule.Threshold = req.Threshold
	rule.AlertAction = req.AlertAction
	rule.AlertDetails = req.AlertDetails
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if err := s.db.Save(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Update SIEM rule"))
		return
	}
	s.reloadSIEMRules()
	s.LogAuditRecord(c, "siem_rule_update", "siem", rule.Name, "SIEM correlation rule updated", true, nil)
	respond(c, gin.H{"rule": rule})
}

func (s *Server) handleToggleSIEMRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var rule db.SIEMRule
	if err := s.db.First(&rule, c.Param("id")).Error; err != nil {
		respondError(c, http.StatusNotFound, "Rule not found")
		return
	}
	rule.Enabled = !rule.Enabled
	if err := s.db.Save(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Toggle SIEM rule"))
		return
	}
	s.reloadSIEMRules()
	s.LogAuditRecord(c, "siem_rule_toggle", "siem", rule.Name, "SIEM correlation rule toggled", true, nil)
	respond(c, gin.H{"enabled": rule.Enabled})
}

func (s *Server) handleDeleteSIEMRule(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var rule db.SIEMRule
	if err := s.db.First(&rule, c.Param("id")).Error; err != nil {
		respondError(c, http.StatusNotFound, "Rule not found")
		return
	}
	if err := s.db.Delete(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Delete SIEM rule"))
		return
	}
	s.reloadSIEMRules()
	s.LogAuditRecord(c, "siem_rule_delete", "siem", rule.Name, "SIEM correlation rule deleted", true, nil)
	respond(c, gin.H{})
}

func (s *Server) reloadSIEMRules() {
	if s.siem == nil {
		return
	}
	s.siem.ReloadRules()
	slog.Info("SIEM rules reloaded from DB")
}

func validateSIEMRule(req siemRuleInput) string {
	if req.Name == "" {
		return "name is required"
	}
	if req.Action == "" {
		return "action is required"
	}
	if req.WindowSec < 1 {
		return "window_sec must be >= 1"
	}
	if req.Threshold < 1 {
		return "threshold must be >= 1"
	}
	if req.AlertAction == "" {
		return "alert_action is required"
	}
	return ""
}
