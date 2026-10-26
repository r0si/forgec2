package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handleAutoTagRules lists all auto-tag rules.
// GET /api/autotag/rules
func (s *Server) handleAutoTagRules(c *gin.Context) {
	var rules []db.AutoTagRule
	if err := s.db.Preload("Tag").Order("priority desc, created_at desc").Limit(200).Find(&rules).Error; err != nil {
		handleQueryError(c, err, "Failed to list auto-tag rules")
		return
	}
	respond(c, gin.H{"rules": rules})
}

// handleAutoTagCreate creates a new auto-tag rule.
// POST /api/autotag/rules
func (s *Server) handleAutoTagCreate(c *gin.Context) {
	var rule db.AutoTagRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	if rule.Name == "" || rule.TagID == "" {
		respondError(c, http.StatusBadRequest, "name and tag_id required")
		return
	}
	// Conditions arrive as a JSON array; ShouldBindJSON already stored it as text.
	if rule.ID == "" {
		rule.ID = util.NewString()
	}
	if err := s.db.Create(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create rule")
		return
	}
	var created db.AutoTagRule
	if err := s.db.Preload("Tag").First(&created, "id = ?", rule.ID).Error; err != nil {
		respondError(c, http.StatusNotFound, "rule not found after create")
		return
	}
	respond(c, gin.H{"rule": created})
}

// handleAutoTagUpdate updates an existing auto-tag rule.
// PUT /api/autotag/rules/:id
func (s *Server) handleAutoTagUpdate(c *gin.Context) {
	id := c.Param("id")
	var rule db.AutoTagRule
	if !s.findOrFail(c, &rule, id, "rule") {
		return
	}
	if err := c.ShouldBindJSON(&rule); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	rule.ID = id
	if err := s.db.Save(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update rule")
		return
	}
	var updated db.AutoTagRule
	if err := s.db.Preload("Tag").First(&updated, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, "rule not found after update")
		return
	}
	respond(c, gin.H{"rule": updated})
}

// handleAutoTagToggle enables or disables a rule.
// POST /api/autotag/rules/:id/toggle
func (s *Server) handleAutoTagToggle(c *gin.Context) {
	id := c.Param("id")
	var rule db.AutoTagRule
	if !s.findOrFail(c, &rule, id, "rule") {
		return
	}
	rule.Enabled = !rule.Enabled
	if err := s.db.Save(&rule).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to toggle rule")
		return
	}
	respond(c, gin.H{"rule": rule})
}

// handleAutoTagDelete removes a rule.
// DELETE /api/autotag/rules/:id
func (s *Server) handleAutoTagDelete(c *gin.Context) {
	id := c.Param("id")
	res := s.db.Delete(&db.AutoTagRule{}, "id = ?", id)
	if res.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(res.Error, "Auto tag"))
		return
	}
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "rule not found")
		return
	}
	respond(c, gin.H{})
}

// handleAutoTagApply evaluates all enabled rules against current agents.
// POST /api/autotag/apply
func (s *Server) handleAutoTagApply(c *gin.Context) {
	var rules []db.AutoTagRule
	if err := s.db.Preload("Tag").Where("enabled = ?", true).Limit(AutoTagRuleLimit).Find(&rules).Error; err != nil {
		slog.Error("Failed to load auto-tag rules for apply", "err", err)
	}

	var agents []db.Implant
	if err := s.db.Limit(AutoTagAgentLimit).Find(&agents).Error; err != nil {
		slog.Error("Failed to load agents for auto-tag", "err", err)
	}

	// Load all existing assignments once (avoids an O(rules*agents) query storm).
	var assignments []db.AgentTagAssignment
	if err := s.db.Limit(AutoTagAssignmentLimit).Find(&assignments).Error; err != nil {
		slog.Error("Failed to load auto-tag assignments", "err", err)
	}
	existingSet := make(map[string]bool, len(assignments))
	for _, a := range assignments {
		existingSet[a.AgentTagID+"|"+a.ImplantID] = true
	}

	// Collect the new assignments first, then write them in one batched
	// transaction: the inner loop used to issue one INSERT per (rule,
	// agent) pair, turning a 200-rule × 5000-agent apply into a
	// single-statement-per-row storm. The batch is also capped so a
	// pathological rule set cannot allocate an unbounded slice.
	var pending []db.AgentTagAssignment
	capped := false
	for _, r := range rules {
		if r.Tag == nil {
			continue
		}
		for _, a := range agents {
			if !autoTagMatch(r.Condition, a) {
				continue
			}
			key := r.TagID + "|" + a.ID
			if existingSet[key] {
				continue
			}
			if len(pending) >= AutoTagApplyBatchLimit {
				capped = true
				break
			}
			pending = append(pending, db.AgentTagAssignment{AgentTagID: r.TagID, ImplantID: a.ID})
			existingSet[key] = true
		}
		if capped {
			break
		}
	}
	applied := 0
	if len(pending) > 0 {
		// One transaction, batched inserts; a conflict on the unique
		// index (concurrent apply) is reported, not fatal.
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			return tx.CreateInBatches(&pending, 500).Error
		}); err != nil {
			// A total batch failure is not a success: nothing was applied, and
			// no caller reads the "partial" flag, so a 200 here read as "done".
			slog.Error("Auto-tag: failed to apply assignment batch", "count", len(pending), "err", err)
			respondError(c, http.StatusInternalServerError, "auto-tag batch insert failed")
			return
		}
		applied = len(pending)
	}
	if capped {
		slog.Warn("Auto-tag apply hit the batch cap", "cap", AutoTagApplyBatchLimit)
	}
	respond(c, gin.H{"applied": applied, "capped": capped})
}

// autoTagMatch evaluates a stored condition (JSON array of {field,op,value})
// against an implant.
func autoTagMatch(condition string, a db.Implant) bool {
	if strings.TrimSpace(condition) == "" {
		return false
	}
	var conds []struct {
		Field string `json:"field"`
		Op    string `json:"op"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(condition), &conds); err != nil {
		return false
	}
	if len(conds) == 0 {
		return false
	}
	for _, cond := range conds {
		if !matchOne(cond.Field, cond.Op, cond.Value, a) {
			return false
		}
	}
	return true
}

func matchOne(field, op, value string, a db.Implant) bool {
	var actual string
	switch field {
	case "hostname":
		actual = a.Hostname
	case "os":
		actual = a.OS
	case "arch":
		actual = a.Arch
	case "ip":
		actual = a.IP
	case "username":
		actual = a.Username
	case "external_ip", "public_ip":
		actual = a.PublicIP
	case "process_name":
		actual = a.ProcessName
	case "domain":
		actual = a.Domain
	default:
		actual = ""
	}
	actual = strings.ToLower(actual)
	val := strings.ToLower(value)
	switch op {
	case "contains":
		return strings.Contains(actual, val)
	case "equals":
		return actual == val
	case "not_equals":
		return actual != val
	case "starts_with":
		return strings.HasPrefix(actual, val)
	case "regex":
		matched, err := regexp.MatchString(val, actual)
		return err == nil && matched
	default:
		return false
	}
}
