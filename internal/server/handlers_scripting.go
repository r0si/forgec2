package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/scripting"
	"github.com/gin-gonic/gin"
)

func (s *Server) loadScriptsFromDB() {
	var rows []db.Script
	if err := s.db.Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return
	}
	engine := scripting.GetEngine()
	for _, row := range rows {
		_ = engine.LoadScript(row.ID, row.Name, row.Code)
	}
}

func (s *Server) handleScriptingPage(c *gin.Context) {
	stats := s.getNavStats(c)
	scripts, err := s.listScriptsFromDB()
	if err != nil {
		handleQueryError(c, err, "Failed to list scripts")
		return
	}
	data := gin.H{
		"Title":     "ForgeC2 - Scripting Console",
		"ActiveNav": "scripting",
		"Stats":     stats,
		"Scripts":   scripts,
	}
	for k, v := range stats {
		data[k] = v
	}
	s.renderPageOrJSON(c, data)
}

func (s *Server) listScriptsFromDB() ([]scripting.Script, error) {
	var rows []db.Script
	if err := s.db.Order("updated_at desc").Limit(200).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]scripting.Script, 0, len(rows))
	for _, row := range rows {
		out = append(out, scripting.Script{
			ID:          strconv.FormatUint(uint64(row.ID), 10),
			Name:        row.Name,
			Description: row.Description,
			Code:        row.Code,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
			RunCount:    row.RunCount,
			LastRun:     row.LastRun,
		})
	}
	return out, nil
}

func (s *Server) handleAPIGetScripts(c *gin.Context) {
	scripts, err := s.listScriptsFromDB()
	if err != nil {
		handleQueryError(c, err, "Failed to list scripts")
		return
	}
	respond(c, scripts)
}

func (s *Server) handleAPISaveScript(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		ID          string `json:"id"`
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		Code        string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	var row db.Script
	now := time.Now()
	if req.ID != "" {
		if id, err := strconv.ParseUint(req.ID, 10, 64); err == nil {
			if err := s.db.First(&row, id).Error; err == nil {
				row.Name = req.Name
				row.Description = req.Description
				row.Code = req.Code
				row.UpdatedAt = now
				if err := s.db.Save(&row).Error; err != nil {
					respondError(c, http.StatusInternalServerError, sanitizeError(err, "script"))
					return
				}
			}
		}
	}
	if row.ID == 0 {
		row = db.Script{
			Name:        req.Name,
			Description: req.Description,
			Code:        req.Code,
			Enabled:     true,
			CreatedBy:   c.GetString("username"),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.db.Create(&row).Error; err != nil {
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "script"))
			return
		}
	}

	_ = scripting.GetEngine().LoadScript(row.ID, row.Name, row.Code)

	script := scripting.Script{
		ID:          strconv.FormatUint(uint64(row.ID), 10),
		Name:        row.Name,
		Description: row.Description,
		Code:        row.Code,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		RunCount:    row.RunCount,
		LastRun:     row.LastRun,
	}
	s.LogAuditRecord(c, "save_script", "scripting", script.ID, "Script saved: "+req.Name, true, nil)
	respond(c, script)
}

func (s *Server) handleAPIDeleteScript(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	uid, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid script id")
		return
	}
	var row db.Script
	if err := s.db.First(&row, uid).Error; err != nil {
		respondError(c, http.StatusNotFound, "script not found")
		return
	}
	if err := s.db.Delete(&row).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "script"))
		return
	}
	scripting.GetEngine().UnloadScript(row.Name)
	s.LogAuditRecord(c, "delete_script", "scripting", id, "Script deleted", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleAPIExecuteScript(c *gin.Context) {
	if !s.requireAdmin(c) {
		return
	}
	var req struct {
		ScriptID string `json:"script_id"`
		Code     string `json:"code"`
		AgentID  string `json:"agent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	context := map[string]interface{}{
		"agents":      []db.Implant{},
		"tasks":       []db.Task{},
		"credentials": []db.CredentialEntry{},
	}

	var agents []db.Implant
	if err := s.db.Select("id, hostname, ip, os, status").Limit(100).Find(&agents).Error; err != nil {
		slog.Error("Scripting: failed to query agents", "err", err)
	}
	if agents != nil {
		context["agents"] = agents
	}

	var tasks []db.Task
	if err := s.db.Select("id, agent_id, type, command, status").Order("created_at desc").Limit(50).Find(&tasks).Error; err != nil {
		slog.Error("Scripting: failed to query tasks", "err", err)
	}
	if tasks != nil {
		context["tasks"] = tasks
	}

	var creds []db.CredentialEntry
	if err := s.db.Select("agent_id, domain, username, type, source").Limit(50).Find(&creds).Error; err != nil {
		slog.Error("Scripting: failed to query credentials", "err", err)
	}
	if creds != nil {
		context["credentials"] = creds
	}

	// Optional target agent. When set, the `agents` global is narrowed to that
	// one host so a script written against a single target cannot silently fan
	// out across the fleet. The lookup is tenant-scoped, so a foreign agent id
	// is indistinguishable from a missing one.
	targetAgentID := strings.TrimSpace(req.AgentID)
	if targetAgentID != "" {
		var target db.Implant
		if err := s.tenantScope(s.db.Model(&db.Implant{}), c).
			Select("id, hostname, ip, os, status").
			Where("id = ?", targetAgentID).
			First(&target).Error; err != nil {
			respondError(c, http.StatusNotFound, "agent not found")
			return
		}
		context["agent_id"] = target.ID
		context["agents"] = []db.Implant{target}
	}

	engine := scripting.GetEngine()
	var result scripting.ExecutionResult
	caller := scripting.Caller{
		Username: c.GetString("username"),
		Role:     c.GetString("user_role"),
		TenantID: s.currentTenantID(c),
	}

	scriptLabel := "inline"
	if req.ScriptID != "" {
		if uid, err := strconv.ParseUint(req.ScriptID, 10, 64); err == nil {
			var row db.Script
			if err := s.db.First(&row, uid).Error; err == nil {
				_ = engine.LoadScript(row.ID, row.Name, row.Code)
				row.RunCount++
				row.LastRun = time.Now()
				if err := s.db.Save(&row).Error; err != nil {
					slog.Error("Failed to update script run count", "script_id", uid, "err", err)
				}
				scriptLabel = row.Name
			}
		}
		result = engine.Execute(req.ScriptID, context, caller)
	} else if req.Code != "" {
		result = engine.ExecuteCode(req.Code, context, caller)
	} else {
		respondError(c, http.StatusBadRequest, "no script_id or code provided")
		return
	}

	var execErr error
	if !result.Success {
		execErr = errors.New(result.Error)
	}
	s.LogAuditRecord(c, "execute_script", "scripting", targetAgentID,
		scriptRunDetails(scriptLabel, targetAgentID), result.Success, execErr)
	respond(c, gin.H{"result": result})
}

// scriptRunDetails renders the audit `details` string for a script execution.
// The run-history endpoint parses it back, so both sides must agree.
func scriptRunDetails(scriptLabel, agentID string) string {
	label := strings.Join(strings.Fields(scriptLabel), " ")
	if label == "" {
		label = "inline"
	}
	if len(label) > 80 {
		label = label[:80]
	}
	if agentID != "" {
		return "script=" + label + " agent=" + agentID
	}
	return "script=" + label
}

func parseScriptRunDetails(details string) (string, string) {
	rest, ok := strings.CutPrefix(details, "script=")
	if !ok {
		return "", ""
	}
	label, agentID, found := strings.Cut(rest, " agent=")
	if !found {
		return rest, ""
	}
	return label, agentID
}

// handleAPIScriptsHistory serves the scripting run history from the audit log.
// Script execution runs in-process through the Goja engine and never creates a
// task row, so the task table could never hold this data; the audit log is the
// only durable record of who ran what, when, and whether it succeeded.
func (s *Server) handleAPIScriptsHistory(c *gin.Context) {
	var logs []db.AuditLog
	if err := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).
		Where("action = ?", "execute_script").
		Order("created_at DESC").
		Limit(50).
		Find(&logs).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "script history"))
		return
	}

	type historyEntry struct {
		ID         uint      `json:"id"`
		ScriptName string    `json:"script_name"`
		AgentID    string    `json:"agent_id"`
		User       string    `json:"user"`
		Status     string    `json:"status"`
		Error      string    `json:"error"`
		CreatedAt  time.Time `json:"created_at"`
	}

	entries := make([]historyEntry, 0, len(logs))
	for _, l := range logs {
		scriptName, agentID := parseScriptRunDetails(l.Details)
		status := "success"
		if !l.Success {
			status = "failed"
		}
		entries = append(entries, historyEntry{
			ID:         l.ID,
			ScriptName: scriptName,
			AgentID:    agentID,
			User:       l.User,
			Status:     status,
			Error:      l.Error,
			CreatedAt:  l.CreatedAt,
		})
	}

	respond(c, gin.H{"history": entries})
}
