package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handlePrivescCheck handles privilege escalation reconnaissance
func (s *Server) handlePrivescCheck(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	var req struct {
		CheckType string `json:"check_type"` // all, windows, linux, cve_match
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		req.CheckType = c.PostForm("check_type")
		if req.CheckType == "" {
			req.CheckType = "all"
		}
	}

	// Route through issueAgentTask like every other agent-facing dispatch:
	// the bare Create used to skip the tenant scope, the agent-existence
	// check and the approval/lock gates inside createTask.
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "privesc_check", Command: fmt.Sprintf("privesc_check:%s", req.CheckType)})
	if task == nil {
		return
	}

	// Log audit
	s.LogAuditRecord(c, "privilege_escalation_check",
		fmt.Sprintf("Privilege escalation check task %d created (type: %s)", task.ID, req.CheckType),
		agentID, fmt.Sprintf("Check type: %s", req.CheckType),
		true, nil)

	respond(c, gin.H{

		"task_id": task.ID,
		"message": fmt.Sprintf("Privilege escalation check started (type: %s)", req.CheckType),
	})
}

// handleProcessPrivescResult processes privesc check results from agent
func (s *Server) handleProcessPrivescResult(c *gin.Context) {
	var req struct {
		TaskID            uint            `json:"task_id"`
		AgentID           string          `json:"agent_id"`
		OS                string          `json:"os"`
		Vulnerabilities   []VulnInfo      `json:"vulnerabilities"`
		Misconfigurations []MisconfigInfo `json:"misconfigurations"`
		Suggestions       []string        `json:"suggestions"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	// Format results
	result := "=== Privilege Escalation Check Results ===\n\n"
	result += fmt.Sprintf("OS: %s\n\n", req.OS)

	result += fmt.Sprintf("Found %d potential vulnerabilities:\n", len(req.Vulnerabilities))
	for i, vuln := range req.Vulnerabilities {
		result += fmt.Sprintf("%d. [%s] %s - %s (CVE: %s)\n",
			i+1, vuln.Severity, vuln.Title, vuln.Description, vuln.CVE)
	}

	result += fmt.Sprintf("\nFound %d misconfigurations:\n", len(req.Misconfigurations))
	for i, misconfig := range req.Misconfigurations {
		result += fmt.Sprintf("%d. [%s] %s - %s\n",
			i+1, misconfig.Severity, misconfig.Title, misconfig.Description)
	}

	result += "\nSuggestions:\n"
	for i, suggestion := range req.Suggestions {
		result += fmt.Sprintf("%d. %s\n", i+1, suggestion)
	}

	// Update task
	s.db.Model(&db.Task{}).Where("id = ?", req.TaskID).Updates(map[string]interface{}{
		"status": "completed",
		"result": result,
	})

	respond(c, gin.H{

		"vulnerabilities":   len(req.Vulnerabilities),
		"misconfigurations": len(req.Misconfigurations),
		"message":           "Privilege escalation check completed",
	})
}

// VulnInfo represents a potential privilege escalation vulnerability
type VulnInfo struct {
	CVE         string `json:"cve"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Severity    string `json:"severity"` // critical, high, medium, low
	ExploitURL  string `json:"exploit_url"`
}

// MisconfigInfo represents a system misconfiguration
type MisconfigInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Remediation string `json:"remediation"`
}

// handlePrivescPage renders the privilege escalation page
func (s *Server) handlePrivescPage(c *gin.Context) {
	stats := s.getNavStats(c)

	// Get available agents
	var agents []db.Implant
	if err := s.db.Where("status = 'online'").Limit(5000).Find(&agents).Error; err != nil {
		slog.Error("Failed to query privesc agents", "err", err)
	}

	var tasks []db.Task
	s.db.Where("type IN ?", []string{"privesc_check", "privesc_execute"}).
		Order("created_at desc").
		Limit(50).
		Find(&tasks)

	data := gin.H{
		"Title":     "ForgeC2 - Privilege Escalation",
		"ActiveNav": "privesc",
		"Stats":     stats,
		"Agents":    agents,
		"History":   s.buildPrivescHistory(tasks),
		"Findings":  s.buildPrivescFindings(tasks),
	}
	s.renderPageOrJSON(c, data)
}

// privescHistoryEntry is the JSON shape the frontend privesc page expects for history rows.
type privescHistoryEntry struct {
	ID            uint      `json:"id"`
	AgentID       string    `json:"agent_id"`
	CheckType     string    `json:"check_type"`
	Status        string    `json:"status"`
	Result        string    `json:"result"`
	FindingsCount int       `json:"findings_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// handlePrivescExecute was removed: privesc_execute was never a registered
// protocol task type, so the task it created could not be dispatched to any
// implant (and the UI never received an exploit_command to send, since
// findings carry only reference URLs). Routing it through issueAgentTask
// exposed the phantom type; the honest fix is to delete the endpoint rather
// than keep an operation that claimed "exploit task created" for a task that
// could never run. A real exploit-execution surface needs a proper TaskSpec
// plus an agent handler, not an unregistered type.

// buildPrivescHistory maps privesc tasks to the frontend history shape.
func (s *Server) buildPrivescHistory(tasks []db.Task) []privescHistoryEntry {
	out := make([]privescHistoryEntry, 0, len(tasks))
	for _, t := range tasks {
		checkType := strings.TrimPrefix(t.Command, "privesc_check:")
		if checkType == t.Command {
			checkType = "all"
		}
		out = append(out, privescHistoryEntry{
			ID:            t.ID,
			AgentID:       t.AgentID,
			CheckType:     checkType,
			Status:        t.Status,
			Result:        t.Result,
			FindingsCount: len(s.parsePrivescFindings(t.Result)),
			CreatedAt:     t.CreatedAt,
		})
	}
	return out
}

// handlePrivescHistory returns privesc check history for a single task.
func (s *Server) handlePrivescHistory(c *gin.Context) {
	id := c.Param("id")

	var task db.Task
	if err := s.db.Where("id = ? AND type IN ?", id, []string{"privesc_check", "privesc_execute"}).First(&task).Error; err != nil {
		respondError(c, http.StatusNotFound, "privesc task not found")
		return
	}

	respond(c, gin.H{
		"tasks":    []privescHistoryEntry{s.buildPrivescHistory([]db.Task{task})[0]},
		"findings": s.parsePrivescFindings(task.Result),
		"total":    1,
	})
}

// buildPrivescFindings collects parsed findings across a set of privesc tasks.
func (s *Server) buildPrivescFindings(tasks []db.Task) []gin.H {
	out := make([]gin.H, 0)
	for _, t := range tasks {
		out = append(out, s.parsePrivescFindings(t.Result)...)
	}
	return out
}

// parsePrivescFindings best-effort parses structured findings from agent privesc result text.
func (s *Server) parsePrivescFindings(result string) []gin.H {
	findings := make([]gin.H, 0)
	lines := strings.Split(result, "\n")

	var currentCVE string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track CVE section headers: "--- CVE-2021-4034 (pkexec/pwnkit) ---"
		if strings.HasPrefix(trimmed, "--- CVE-") {
			rest := strings.TrimPrefix(trimmed, "---")
			rest = strings.TrimSuffix(rest, "---")
			fields := strings.SplitN(strings.TrimSpace(rest), " ", 2)
			if len(fields) > 0 && strings.HasPrefix(fields[0], "CVE-") {
				currentCVE = strings.TrimRight(fields[0], ":")
				continue
			}
			continue
		}

		isFinding := strings.Contains(trimmed, "[!]") ||
			strings.Contains(trimmed, "WRITABLE") ||
			strings.Contains(trimmed, "vulnerable") ||
			strings.Contains(trimmed, "potentially vulnerable")

		if !isFinding || strings.TrimSpace(trimmed) == "" {
			continue
		}

		severity := "high"
		if strings.Contains(trimmed, "WRITABLE") || strings.Contains(trimmed, "possible") {
			severity = "medium"
		}
		if strings.Contains(trimmed, "potentially vulnerable") {
			severity = "medium"
		}

		title := strings.Trim(strings.TrimPrefix(trimmed, "[!]"), " -")
		if currentCVE != "" {
			title = fmt.Sprintf("%s (%s)", currentCVE, title)
		}

		findings = append(findings, gin.H{
			"title":          title,
			"severity":       severity,
			"cve_id":         currentCVE,
			"description":    trimmed,
			"recommendation": "",
		})
	}
	return findings
}

// handlePrivescRun creates a privesc check task for the given agent.
func (s *Server) handlePrivescRun(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		AgentID   string `json:"agent_id"`
		CheckType string `json:"check_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AgentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}
	if req.CheckType == "" {
		req.CheckType = "all"
	}
	// issueAgentTask validates the agent (tenant scope included) and routes
	// through the same gates as every other task-issuing endpoint; the bare
	// Create used to accept any agent id from any tenant.
	task := s.issueAgentTask(c, req.AgentID, TaskSpec{Type: "privesc_check", Command: fmt.Sprintf("privesc_check:%s", req.CheckType)})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "privesc_check", "agent", req.AgentID,
		fmt.Sprintf("Privesc check task created (type: %s)", req.CheckType), true, nil)
	respond(c, gin.H{"task_id": task.ID})
}

// handlePrivescExecute was removed 2026-10: privesc_execute was never a
// registered protocol task type, so the row it created could not be
// dispatched to any implant, and no finding ever carried an exploit_command
// for the UI to send (agent output offers reference URLs only). Routing it
// through issueAgentTask exposed the phantom type as a 400; the honest fix
// is deleting the endpoint rather than keeping an operation that claims
// "exploit task created" for a task that can never run. A real
// exploit-execution surface needs a proper TaskSpec plus an agent handler.
