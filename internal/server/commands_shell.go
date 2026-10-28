package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// ── Shell dispatch + task queries ─────────────────────────────────────────

func validateCallbackURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("cannot resolve host")
		}
		for _, resolved := range ips {
			if isPrivateIP(resolved.String()) {
				return fmt.Errorf("private/internal IP addresses are not allowed")
			}
		}
		return nil
	}
	if isPrivateIP(ip.String()) {
		return fmt.Errorf("private/internal IP addresses are not allowed")
	}
	return nil
}

func validatePID(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fmt.Errorf("must be a positive integer PID")
	}
	return nil
}

// allowedCallbackMethods restricts callback HTTP methods to safe verbs.
// DELETE, PUT, PATCH are rejected to prevent SSRF-based request forgery.
var allowedCallbackMethods = map[string]bool{
	"GET": true, "POST": true, "HEAD": true,
}

func validateCallbackMethod(method string) error {
	if method == "" {
		return nil
	}
	upper := strings.ToUpper(method)
	if !allowedCallbackMethods[upper] {
		return fmt.Errorf("callback method %q not allowed (use GET, POST, or HEAD)", method)
	}
	return nil
}

func (s *Server) handleShellPage(c *gin.Context) {
	id := c.Param("id")
	var agent db.Implant
	// Tenant-scoped like every other agent route: the unscoped read was a
	// direct IDOR on other tenants' agents. 404 text preserved.
	if err := s.tenantScope(s.db, c).First(&agent, "id = ?", id).Error; err != nil {
		c.String(http.StatusNotFound, "Agent not found")
		return
	}

	agent.Status = s.agentStatus(agent).Status

	s.configMu.RLock()
	expectedInterval := s.cfg.Implant.DefaultInterval
	s.configMu.RUnlock()
	stats := s.getNavStats(c)
	data := gin.H{
		"Title":            fmt.Sprintf("ForgeC2 - Shell %s", agent.Hostname),
		"ActiveNav":        "agents",
		"Agent":            agent,
		"IsFullPage":       true,
		"ExpectedInterval": expectedInterval,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleSendCommand(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	cmd := c.PostForm("command")
	shell := c.PostForm("shell")
	callbackURL := c.PostForm("callback_url")
	callbackMethod := c.PostForm("callback_method")

	// Fall back to JSON body when form fields are empty (the frontend
	// postJson helper sends Content-Type: application/json).
	if cmd == "" || shell == "" {
		var jb struct {
			Command        string `json:"command"`
			Shell          string `json:"shell"`
			CallbackURL    string `json:"callback_url"`
			CallbackMethod string `json:"callback_method"`
		}
		if err := c.ShouldBindJSON(&jb); err == nil {
			if cmd == "" {
				cmd = jb.Command
			}
			if shell == "" {
				shell = jb.Shell
			}
			if callbackURL == "" {
				callbackURL = jb.CallbackURL
			}
			if callbackMethod == "" {
				callbackMethod = jb.CallbackMethod
			}
		}
	}

	if len(cmd) > MaxCommandLength {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("command too long (max %d characters)", MaxCommandLength))
		return
	}

	slog.Debug("HandleSendCommand called", "agent_id", id, "command", truncateString(cmd, 100))

	target, ok := s.getAgentOrFail(c, id)
	if !ok {
		return
	}
	if shell == "" {
		// Match the default shell to the target OS — cmd.exe on a Linux
		// implant just errors out on the agent side.
		if target.OS != "" && !strings.HasPrefix(strings.ToLower(target.OS), "win") {
			shell = "/bin/sh"
		} else {
			shell = "cmd.exe"
		}
	}

	// Validate callback fields BEFORE creating the task so a rejected request
	// never leaves a pending task that the agent would execute anyway.
	if callbackURL != "" {
		if err := validateCallbackURL(callbackURL); err != nil {
			respondError(c, http.StatusBadRequest, "invalid callback URL")
			return
		}
		if err := validateCallbackMethod(callbackMethod); err != nil {
			respondError(c, http.StatusBadRequest, "invalid callback method")
			return
		}
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "shell", Command: cmd, Shell: shell})
	if task == nil {
		return
	}

	// Set callback fields if provided
	if callbackURL != "" {
		if err := s.db.Model(&task).Updates(map[string]interface{}{
			"callback_url":    callbackURL,
			"callback_method": callbackMethod,
		}).Error; err != nil {
			slog.Error("Failed to update task callback", "task_id", task.ID, "error", err)
			respondError(c, http.StatusInternalServerError, "failed to update task callback")
			return
		}
		task.CallbackURL = callbackURL
		if callbackMethod != "" {
			task.CallbackMethod = callbackMethod
		}
	}

	slog.Info("Task created successfully", "agent_id", id, "task_id", task.ID, "command", truncateString(cmd, 100))
	s.dispatchTask(c, task, "send_command", cmd)
}

func (s *Server) handleGetAgentTasks(c *gin.Context) {
	id := c.Param("id")
	// Tenant gate: task history (often credential-bearing) must not leak
	// across tenants via a guessed agent UUID.
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	p := parsePagination(c, DefaultTaskPageSize, MaxTaskPageSize)

	query := s.db.Where("agent_id = ?", id).
		Where("type NOT IN ?", []string{"screen_stream_start", "screen_stream_stop", "ls"})

	var total int64
	if err := query.Model(&db.Task{}).Count(&total).Error; err != nil {
		slog.Error("Failed to count agent tasks", "agent_id", id, "error", err)
		respondError(c, http.StatusInternalServerError, "failed to query tasks")
		return
	}

	var tasks []db.Task
	if err := query.Order("created_at desc").Offset(p.Offset).Limit(p.PageSize).Find(&tasks).Error; err != nil {
		slog.Error("Failed to query agent tasks", "agent_id", id, "error", err)
		respondError(c, http.StatusInternalServerError, "failed to query tasks")
		return
	}

	respond(c, gin.H{
		"tasks":     tasks,
		"total":     total,
		"page":      p.Page,
		"page_size": p.PageSize,
	})
}

func (s *Server) handleGetTaskStatus(c *gin.Context) {
	// Parse strictly: GORM treats a raw string condition containing spaces as
	// SQL, so passing the URL parameter straight into First() allowed
	// boolean-blind injection ("1 OR 1=1") and cross-tenant reads.
	taskID, err := strconv.ParseUint(c.Param("taskId"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid task id")
		return
	}

	var task db.Task
	query := s.tenantScope(s.db.Preload("Agent"), c)
	// The agent-scoped route must not expose a task belonging to a different
	// agent. The global /tasks/:taskId route intentionally has no id parameter.
	agentID := strings.TrimSpace(c.Param("id"))
	if agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	if err := query.First(&task, taskID).Error; err != nil {
		// Legacy fallback: pre-tenant tasks carry tenant 0 while operators
		// sit on tenant 1, so the scoped query above 404s rows the agent
		// list still shows (list gates on agent visibility, not task
		// tenant). Retry unscoped but re-gate on agent visibility so no
		// foreign-tenant task leaks: scoped route requires the agent to
		// match, global route requires the task's agent to be visible.
		fallback := s.db.Preload("Agent")
		if agentID != "" {
			fallback = fallback.Where("agent_id = ?", agentID)
		}
		if ferr := fallback.First(&task, taskID).Error; ferr != nil {
			respondError(c, http.StatusNotFound, "task not found")
			return
		}
		if agentID == "" {
			var vis db.Implant
			if verr := s.tenantScope(s.db.Select("id"), c).Where("id = ?", task.AgentID).First(&vis).Error; verr != nil {
				respondError(c, http.StatusNotFound, "task not found")
				return
			}
		} else if task.AgentID != agentID {
			respondError(c, http.StatusNotFound, "task not found")
			return
		}
	}
	task = taskForOperator(task)

	payload := gin.H{
		"id":         task.ID,
		"status":     task.Status,
		"result":     task.Result,
		"error":      task.Error,
		"command":    task.Command,
		"type":       task.Type,
		"agent":      task.Agent.Hostname,
		"created":    task.CreatedAt.Format("2006-01-02 15:04:05"),
		"created_by": task.CreatedBy,
	}
	// A failed task carries the same reason code the failure report groups by, so the console can show "timeout" next to
	// the message without a second request and without its own copy of the classification rules.
	if task.Status == "failed" && strings.TrimSpace(task.Error) != "" {
		payload["failure_reason"] = classifyFailure(task.Error)
	}
	// A task that ran more than once says so here: the retry is automatic, and the operator reading one task needs to know
	// that the result they are looking at is the second attempt rather than the first.
	if task.RetryCount > 0 {
		payload["retry_count"] = task.RetryCount
	}
	respond(c, payload)
}

// handleBatchTaskStatus returns the status of multiple tasks in a single request.
// POST /api/v1/tasks/batch-status  body: task_ids=1,2,3
func (s *Server) handleBatchTaskStatus(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	idsStr := c.PostForm("task_ids")
	if idsStr == "" {
		idsStr = c.Query("task_ids")
	}
	if idsStr == "" {
		respondError(c, http.StatusBadRequest, "task_ids is required")
		return
	}

	idStrs := strings.Split(idsStr, ",")
	if len(idStrs) > MaxBulkCancelLimit {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("too many task IDs (max %d)", MaxBulkCancelLimit))
		return
	}

	var ids []uint
	for _, s := range idStrs {
		s = strings.TrimSpace(s)
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			continue
		}
		ids = append(ids, uint(n))
	}
	if len(ids) == 0 {
		respondError(c, http.StatusBadRequest, "no valid task IDs")
		return
	}

	var tasks []db.Task
	// Tenant scope mirrors handleGetTaskStatus: without it a tenant-scoped
	// operator could read other tenants' task Result/Error bodies.
	if err := s.tenantScope(s.db, c).Where("id IN ?", ids).Find(&tasks).Error; err != nil {
		slog.Error("Failed to query tasks for batch status", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to query tasks")
		return
	}

	type taskStatus struct {
		ID     uint   `json:"id"`
		Status string `json:"status"`
		Result string `json:"result,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	results := make([]taskStatus, len(tasks))
	for i, t := range tasks {
		results[i] = taskStatus{
			ID:     t.ID,
			Status: t.Status,
			Result: t.Result,
			Error:  t.Error,
		}
	}
	respond(c, gin.H{"tasks": results, "total": len(results)})
}
