package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleAPIWorkflows(c *gin.Context) {
	p := parsePagination(c, 50, 200)
	var total int64
	wfScope, wfOK := s.workflowScope(c)
	if !wfOK {
		respond(c, gin.H{"workflows": []db.Workflow{}, "total": 0})
		return
	}
	if err := wfScope.Count(&total).Error; err != nil {
		slog.Error("Failed to count workflows", "err", err)
	}
	var workflows []db.Workflow
	if err := wfScope.Preload("Steps").Order("created_at desc").Offset(p.Offset).Limit(p.PageSize).Find(&workflows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to load workflows")
		return
	}
	respond(c, gin.H{"workflows": workflows, "total": total, "page": p.Page, "page_size": p.PageSize})
}

func (s *Server) handleAPIWorkflowsDetail(c *gin.Context) {
	wf, ok := s.findVisibleWorkflow(c, c.Param("id"))
	if !ok {
		return
	}
	respond(c, gin.H{"workflow": wf})
}

func (s *Server) handleAPIWorkflowsToggle(c *gin.Context) {
	wf, ok := s.findVisibleWorkflow(c, c.Param("id"))
	if !ok {
		return
	}
	// Update the single column instead of Save(&wf): the preloaded Steps association makes a
	// full Save rewrite child rows as well, which is both wider than intended and the source of
	// a bogus "no such column" alias in the generated SQL.
	if err := s.db.Model(&db.Workflow{}).Where("id = ?", wf.ID).Update("enabled", !wf.Enabled).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update workflow")
		return
	}
	respond(c, gin.H{"enabled": !wf.Enabled})
	s.LogAuditRecord(c, "workflow_toggle", "workflow", wf.ID, fmt.Sprintf("Workflow %s", map[bool]string{true: "enabled", false: "disabled"}[!wf.Enabled]), true, nil)
}

func (s *Server) handleAPIWorkflowsExecute(c *gin.Context) {
	wf, ok := s.findVisibleWorkflow(c, c.Param("id"))
	if !ok {
		return
	}

	// Target resolution is tenant-scoped too: the workflow's own scope_ids must not be able to
	// name another tenant's hosts.
	targetAgents := s.workflowTargetAgents(c, wf)
	if len(targetAgents) == 0 {
		respond(c, gin.H{"task_count": 0, "agents_count": 0})
		return
	}
	agentIDs := make([]string, len(targetAgents))
	for i, a := range targetAgents {
		agentIDs[i] = a.ID
	}

	// Use workflow engine for conditional execution
	engine := NewWorkflowEngine(s)
	taskCount, agentsCount, err := engine.ExecuteWorkflow(wf, agentIDs)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Workflow execution"))
		return
	}

	respond(c, gin.H{"task_count": taskCount, "agents_count": agentsCount})
	s.LogAuditRecord(c, "workflow_execute", "workflow", wf.ID, fmt.Sprintf("Executed workflow on %d agent(s), created %d task(s)", agentsCount, taskCount), true, nil)
}

func (s *Server) handleAPICreateWorkflow(c *gin.Context) {
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		ScopeType   string            `json:"scope_type"`
		ScopeIDs    string            `json:"scope_ids"`
		Steps       []db.WorkflowStep `json:"steps"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "name is required")
		return
	}
	if req.ScopeType == "" {
		req.ScopeType = "all"
	}
	wf := db.Workflow{
		ID:          fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:        req.Name,
		Description: req.Description,
		Enabled:     true,
		ScopeType:   req.ScopeType,
		ScopeIDs:    req.ScopeIDs,
		CreatedBy:   s.currentUsername(c),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.db.Create(&wf).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Workflow operation"))
		return
	}
	for i, step := range req.Steps {
		step.WorkflowID = wf.ID
		step.StepOrder = i + 1
		if step.Shell == "" {
			step.Shell = "cmd"
		}
		if step.TimeoutSec == 0 {
			step.TimeoutSec = 60
		}
		step.CreatedAt = time.Now()
		if err := s.db.Create(&step).Error; err != nil {
			slog.Error("Failed to create workflow step", "workflow_id", wf.ID, "step", i+1, "error", err)
		}
	}
	respond(c, gin.H{"id": wf.ID})
}

func (s *Server) handleAPIUpdateWorkflow(c *gin.Context) {
	id := c.Param("id")
	var wf db.Workflow
	wf, ok := s.findVisibleWorkflow(c, id)
	if !ok {
		return
	}
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		ScopeType   string            `json:"scope_type"`
		ScopeIDs    string            `json:"scope_ids"`
		Enabled     *bool             `json:"enabled"`
		Steps       []db.WorkflowStep `json:"steps"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name != "" {
		wf.Name = req.Name
	}
	if req.Description != "" {
		wf.Description = req.Description
	}
	if req.ScopeType != "" {
		wf.ScopeType = req.ScopeType
	}
	wf.ScopeIDs = req.ScopeIDs
	if req.Enabled != nil {
		wf.Enabled = *req.Enabled
	}
	wf.UpdatedAt = time.Now()
	if err := s.db.Save(&wf).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Workflow operation"))
		return
	}

	if req.Steps != nil {
		if err := s.db.Where("workflow_id = ?", id).Delete(&db.WorkflowStep{}).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "failed to delete old workflow steps")
			return
		}
		for i, step := range req.Steps {
			step.WorkflowID = wf.ID
			step.StepOrder = i + 1
			if step.Shell == "" {
				step.Shell = "cmd"
			}
			if step.TimeoutSec == 0 {
				step.TimeoutSec = 60
			}
			step.CreatedAt = time.Now()
			if err := s.db.Create(&step).Error; err != nil {
				slog.Error("Failed to create workflow step", "workflow_id", wf.ID, "step", i+1, "error", err)
			}
		}
	}
	respond(c, gin.H{})
}

// executionSummary is one grouped workflow run (derived from execution_logs).
type executionSummary struct {
	ExecutionID  string     `json:"execution_id"`
	WorkflowID   string     `json:"workflow_id"`
	WorkflowName string     `json:"workflow_name"`
	Status       string     `json:"status"`
	TasksCreated int64      `json:"tasks_created"`
	AgentsCount  int64      `json:"agents_count"`
	ErrorMsg     string     `json:"error_msg,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// executionStatus derives the aggregate run status from its log rows.
// aborted > running > failed > completed, matching legacy row semantics.
func executionStatus(statuses map[string]bool) string {
	if statuses["aborted"] {
		return "aborted"
	}
	if statuses["running"] || statuses["pending"] {
		return "running"
	}
	if statuses["failed"] {
		return "failed"
	}
	return "completed"
}

func (s *Server) handleListWorkflowExecutions(c *gin.Context) {
	workflowID := c.Param("id")
	type groupRow struct {
		ExecutionID  string
		WorkflowID   string
		WorkflowName string
		TasksCreated int64
		AgentsCount  int64
		ErrorMsg     string
		StartedAt    time.Time
		CompletedAt  *time.Time
		CreatedAt    time.Time
	}
	var rows []groupRow
	if err := s.db.Table("execution_logs").
		Select("execution_id, workflow_id, workflow_name, COUNT(*) AS tasks_created, COUNT(DISTINCT agent_id) AS agents_count, MAX(error_msg) AS error_msg, MIN(started_at) AS started_at, MAX(completed_at) AS completed_at, MIN(created_at) AS created_at").
		Where("workflow_id = ?", workflowID).
		Group("execution_id").
		Order("started_at DESC").
		Limit(50).
		Scan(&rows).Error; err != nil {
		slog.Error("Failed to list workflow executions", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to load executions")
		return
	}

	execs := make([]executionSummary, 0, len(rows))
	for _, r := range rows {
		statuses := map[string]bool{}
		var grouped []struct {
			Status       string
			BranchAction string
			BranchTarget string
		}
		if err := s.db.Table("execution_logs").
			Select("status, branch_action, branch_target").
			Where("execution_id = ?", r.ExecutionID).
			Group("status, branch_action, branch_target").
			Scan(&grouped).Error; err == nil {
			for _, g := range grouped {
				statuses[g.Status] = true
				if g.BranchAction == "abort" || g.BranchTarget == "stop_on_failure" {
					statuses["aborted"] = true
				}
			}
		}
		execs = append(execs, executionSummary{
			ExecutionID:  r.ExecutionID,
			WorkflowID:   r.WorkflowID,
			WorkflowName: r.WorkflowName,
			Status:       executionStatus(statuses),
			TasksCreated: r.TasksCreated,
			AgentsCount:  r.AgentsCount,
			ErrorMsg:     r.ErrorMsg,
			StartedAt:    r.StartedAt,
			CompletedAt:  r.CompletedAt,
			CreatedAt:    r.CreatedAt,
		})
	}
	respond(c, gin.H{"executions": execs})
}

func (s *Server) handleGetWorkflowExecution(c *gin.Context) {
	executionID := c.Param("executionId")
	var logs []db.ExecutionLog
	if err := s.db.Where("execution_id = ?", executionID).Order("step_order, id").Find(&logs).Error; err != nil {
		slog.Error("Failed to query workflow execution logs", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to load execution")
		return
	}
	if len(logs) == 0 {
		respondError(c, http.StatusNotFound, "execution not found")
		return
	}
	first := logs[0]
	summary := executionSummary{
		ExecutionID:  executionID,
		WorkflowID:   first.WorkflowID,
		WorkflowName: first.WorkflowName,
		StartedAt:    first.StartedAt,
		CreatedAt:    first.CreatedAt,
	}
	for _, l := range logs {
		if l.CompletedAt != nil && (summary.CompletedAt == nil || l.CompletedAt.After(*summary.CompletedAt)) {
			summary.CompletedAt = l.CompletedAt
		}
		if l.ErrorMsg != "" {
			summary.ErrorMsg = l.ErrorMsg
		}
	}
	statuses := map[string]bool{}
	for _, l := range logs {
		statuses[l.Status] = true
		if l.BranchAction == "abort" || l.BranchTarget == "stop_on_failure" {
			statuses["aborted"] = true
		}
	}
	summary.Status = executionStatus(statuses)
	respond(c, gin.H{"execution": summary, "logs": logs})
}

func (s *Server) handleAPIDeleteWorkflow(c *gin.Context) {
	id := c.Param("id")
	if err := s.db.Where("workflow_id = ?", id).Delete(&db.WorkflowStep{}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete workflow steps")
		return
	}
	wfDelScope, wfDelOK := s.workflowScope(c)
	if !wfDelOK {
		respondError(c, http.StatusNotFound, "workflow not found")
		return
	}
	if err := wfDelScope.Where("id = ?", id).Delete(&db.Workflow{}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Workflow operation"))
		return
	}
	s.LogAuditRecord(c, "delete_workflow", "workflow", id, "Workflow deleted", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleAPIGroups(c *gin.Context) {
	var groups []db.AgentGroup
	if err := s.tenantScope(s.db.Model(&db.AgentGroup{}), c).Limit(200).Find(&groups).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to load groups")
		return
	}
	type enrichedGroup struct {
		db.AgentGroup
		AgentCount int `json:"agent_count"`
		ChildCount int `json:"child_count"`
	}
	if len(groups) == 0 {
		respond(c, gin.H{"groups": []enrichedGroup{}})
		return
	}
	groupIDs := make([]string, len(groups))
	for i, g := range groups {
		groupIDs[i] = g.ID
	}

	type agentCountRow struct {
		GroupID string
		Count   int64
	}
	var agentCounts []agentCountRow
	if err := s.db.Model(&db.Implant{}).Select("agent_group_assignments.agent_group_id as group_id, COUNT(*) as count").
		Joins("JOIN agent_group_assignments ON agent_group_assignments.implant_id = implants.id").
		Where("agent_group_assignments.agent_group_id IN ?", groupIDs).
		Group("agent_group_assignments.agent_group_id").Find(&agentCounts).Error; err != nil {
		slog.Error("Failed to query agent counts by group", "err", err)
	}
	agentCountMap := make(map[string]int64, len(agentCounts))
	for _, row := range agentCounts {
		agentCountMap[row.GroupID] = row.Count
	}

	type childCountRow struct {
		ParentID string
		Count    int64
	}
	var childCounts []childCountRow
	if err := s.db.Model(&db.AgentGroup{}).Select("parent_id, COUNT(*) as count").
		Where("parent_id IN ?", groupIDs).
		Group("parent_id").Find(&childCounts).Error; err != nil {
		slog.Error("Failed to query child counts by group", "err", err)
	}
	childCountMap := make(map[string]int64, len(childCounts))
	for _, row := range childCounts {
		childCountMap[row.ParentID] = row.Count
	}

	result := make([]enrichedGroup, len(groups))
	for i, g := range groups {
		result[i] = enrichedGroup{
			AgentGroup: g,
			AgentCount: int(agentCountMap[g.ID]),
			ChildCount: int(childCountMap[g.ID]),
		}
	}
	respond(c, gin.H{"groups": result})
}

func (s *Server) handleAPICreateGroup(c *gin.Context) {
	var req struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Color       string  `json:"color"`
		ParentID    *string `json:"parent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "name is required")
		return
	}
	if req.Color == "" {
		req.Color = "#2ecc71"
	}
	group := db.AgentGroup{
		ID:          fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:        req.Name,
		Description: req.Description,
		Color:       req.Color,
		ParentID:    req.ParentID,
		TenantID:    s.currentTenantID(c),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.db.Create(&group).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Workflow operation"))
		return
	}
	respond(c, gin.H{"id": group.ID})
}

func (s *Server) handleAPIUpdateGroup(c *gin.Context) {
	id := c.Param("id")
	var group db.AgentGroup
	if err := s.tenantScope(s.db.Model(&db.AgentGroup{}), c).Where("id = ?", id).First(&group).Error; err != nil {
		respondError(c, http.StatusNotFound, "group not found")
		return
	}
	var req struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Color       string  `json:"color"`
		ParentID    *string `json:"parent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name != "" {
		group.Name = req.Name
	}
	if req.Description != "" {
		group.Description = req.Description
	}
	if req.Color != "" {
		group.Color = req.Color
	}
	group.ParentID = req.ParentID
	group.UpdatedAt = time.Now()
	if err := s.db.Save(&group).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update group")
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleAPIDeleteGroup(c *gin.Context) {
	id := c.Param("id")
	var agentCount int64
	if err := s.db.Model(&db.Implant{}).Joins("JOIN agent_group_assignments ON agent_group_assignments.implant_id = implants.id").
		Where("agent_group_assignments.agent_group_id = ?", id).Count(&agentCount).Error; err != nil {
		slog.Error("Failed to count group agents", "group_id", id, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to check agent count")
		return
	}
	if agentCount > 0 {
		respondError(c, http.StatusBadRequest, "cannot delete group with agents — reassign agents first")
		return
	}
	var childCount int64
	if err := s.db.Model(&db.AgentGroup{}).Where("parent_id = ?", id).Count(&childCount).Error; err != nil {
		slog.Error("Failed to count child groups", "parent_id", id, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to check child group count")
		return
	}
	if childCount > 0 {
		respondError(c, http.StatusBadRequest, "cannot delete group with child groups — remove children first")
		return
	}
	// Delete through the tenant scope: a group belongs to one tenant now.
	if err := s.tenantScope(s.db.Model(&db.AgentGroup{}), c).Where("id = ?", id).Delete(&db.AgentGroup{}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete group")
		return
	}
	s.LogAuditRecord(c, "delete_group", "group", id, "Agent group deleted", true, nil)
	respond(c, gin.H{})
}
