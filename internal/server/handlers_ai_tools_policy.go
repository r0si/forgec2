package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	aiToolRiskRead         = "read"
	aiToolRiskLowRiskWrite = "low_risk_write"
	aiToolRiskWrite        = "write"
	aiToolRiskDestructive  = "destructive"
	aiToolRiskSensitive    = "sensitive"
)

type aiToolPolicy struct {
	Risk       string
	Permission string
	Hidden     bool
	GlobalOnly bool
}

var aiToolPolicies = map[string]aiToolPolicy{
	"list_agents":            {Risk: aiToolRiskRead, Permission: db.PermAgentsRead},
	"get_agent_detail":       {Risk: aiToolRiskRead, Permission: db.PermAgentsRead},
	"execute_command":        {Risk: aiToolRiskWrite, Permission: db.PermTasksWrite},
	"get_agent_tasks":        {Risk: aiToolRiskRead, Permission: db.PermTasksRead},
	"list_listeners":         {Risk: aiToolRiskRead, Permission: db.PermListenersRead, GlobalOnly: true},
	"list_credentials":       {Risk: aiToolRiskSensitive, Permission: db.PermCredsRead},
	"get_online_operators":   {Risk: aiToolRiskRead, Permission: db.PermUsersRead, GlobalOnly: true},
	"search_tasks":           {Risk: aiToolRiskRead, Permission: db.PermTasksRead},
	"get_timeline":           {Risk: aiToolRiskRead, Permission: db.PermTasksRead},
	"query_ioc":              {Risk: aiToolRiskRead, Permission: db.PermIntelRead, GlobalOnly: true},
	"list_macros":            {Risk: aiToolRiskRead, Permission: db.PermAutomationRead, GlobalOnly: true},
	"run_macro":              {Risk: aiToolRiskWrite, Permission: db.PermTasksWrite},
	"create_automation_rule": {Risk: aiToolRiskWrite, Permission: db.PermAutomationWrite, GlobalOnly: true},
	"get_attack_surface":     {Risk: aiToolRiskRead, Permission: db.PermAgentsRead},
	"get_task_detail":        {Risk: aiToolRiskRead, Permission: db.PermTasksRead},
	"execute_command_bulk":   {Risk: aiToolRiskWrite, Permission: db.PermTasksWrite},
	"query_bloodhound":       {Risk: aiToolRiskSensitive, Permission: db.PermCredsRead},
	"get_coverage_gaps":      {Risk: aiToolRiskRead, Permission: db.PermAgentsRead, GlobalOnly: true},
	"save_engagement_note":   {Risk: aiToolRiskWrite, Permission: db.PermSettingsWrite, GlobalOnly: true},
	"list_pending_tasks":     {Risk: aiToolRiskRead, Permission: db.PermTasksRead, Hidden: true},
	// AI may never approve its own legacy tasks. Human approval is handled by
	// the AIExecutionIntent API and is always bound to the deciding operator.
	"bulk_task_action":       {Risk: aiToolRiskSensitive, Permission: db.PermTasksWrite, Hidden: true},
	"get_screenshot_summary": {Risk: aiToolRiskRead, Permission: db.PermFilesRead},
	"get_keylog_summary":     {Risk: aiToolRiskSensitive, Permission: db.PermCredsRead},
	"generate_report":        {Risk: aiToolRiskRead, Permission: db.PermIntelRead, GlobalOnly: true},
	// The assistant may write a plugin package, but plugins are installation-global and writing code is
	// a write: plugins.write plus a human approval intent, and the tool itself never executes anything.
	"create_plugin": {Risk: aiToolRiskWrite, Permission: db.PermPluginsWrite, GlobalOnly: true},
	// Running one needs its own permission: authoring code and executing code are different powers, so
	// `plugins.execute` can be granted without `plugins.write` and vice versa.
	"run_plugin": {Risk: aiToolRiskWrite, Permission: db.PermPluginsExecute, GlobalOnly: true},
	// Iterating on a plugin the assistant wrote. Same permission as authoring it, plus the tool refuses
	// any package whose author is not the assistant.
	"update_plugin": {Risk: aiToolRiskWrite, Permission: db.PermPluginsWrite, GlobalOnly: true},
	// Removing one has its own permission, so "may run plugins" and "may delete them" are granted
	// separately; the tool also refuses any package the assistant did not author.
	"delete_plugin":   {Risk: aiToolRiskDestructive, Permission: db.PermPluginsDelete, GlobalOnly: true},
	"create_listener": {Risk: aiToolRiskWrite, Permission: db.PermListenersWrite, GlobalOnly: true},
	"update_listener": {Risk: aiToolRiskWrite, Permission: db.PermListenersWrite, GlobalOnly: true},
	"delete_listener": {Risk: aiToolRiskDestructive, Permission: db.PermListenersDelete, GlobalOnly: true},
	// get_situation applies tenant and permission scoping inside the collector;
	// keeping it GlobalOnly made the tool unusable for every normal tenant,
	// including administrators, while older conversations could still ask the
	// model to call it.
	"get_situation":    {Risk: aiToolRiskRead, Permission: db.PermAgentsRead},
	"get_alerts":       {Risk: aiToolRiskRead, Permission: db.PermOpsecRead, GlobalOnly: true},
	"set_sleep":        {Risk: aiToolRiskLowRiskWrite, Permission: db.PermAgentsWrite},
	"queue_collection": {Risk: aiToolRiskLowRiskWrite, Permission: db.PermTasksWrite},
	"search_knowledge": {Risk: aiToolRiskRead, Permission: db.PermAIUse},
	"web_search":       {Risk: aiToolRiskRead, Permission: db.PermAIUse},
}

func (s *Server) buildToolsForContext(reqCtx *aiReqCtx) []toolDef {
	if reqCtx != nil && reqCtx.DisableTools {
		return nil
	}
	tools := s.buildTools()
	if reqCtx == nil || reqCtx.Principal.UserID == 0 {
		return tools
	}
	filtered := make([]toolDef, 0, len(tools))
	for _, tool := range tools {
		policy, ok := aiToolPolicies[tool.Function.Name]
		if !ok || policy.Hidden || (policy.GlobalOnly && reqCtx.Principal.TenantID != 0) || !reqCtx.Principal.hasPermission(s.db, policy.Permission) {
			continue
		}
		filtered = append(filtered, tool)
	}
	if reqCtx.Principal.hasPermission(s.db, db.PermAIUse) && len(reqCtx.KnowledgeCollectionIDs) > 0 {
		filtered = append(filtered, toolDef{Type: "function", Function: toolFuncDef{
			Name:        "search_knowledge",
			Description: "Search only the knowledge collections explicitly selected for this conversation. Returns cited source and chunk identifiers with relevant text.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":          map[string]string{"type": "string", "description": "Search query"},
					"collection_ids": map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}},
					"limit":          map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 12},
				},
				"required": []string{"query", "collection_ids"},
			},
		}})
	}
	return filtered
}

func summarizeAIArguments(raw string) string {
	var value interface{}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return truncateString(strings.TrimSpace(raw), 500)
	}
	var redact func(interface{}) interface{}
	redact = func(v interface{}) interface{} {
		switch typed := v.(type) {
		case map[string]interface{}:
			out := make(map[string]interface{}, len(typed))
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "key") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") {
					out[key] = "[REDACTED]"
				} else {
					out[key] = redact(typed[key])
				}
			}
			return out
		case []interface{}:
			out := make([]interface{}, len(typed))
			for i := range typed {
				out[i] = redact(typed[i])
			}
			return out
		case string:
			return truncateString(typed, 160)
		default:
			return typed
		}
	}
	encoded, _ := json.Marshal(redact(value))
	return truncateString(string(encoded), 500)
}

func (s *Server) authorizeAITool(reqCtx *aiReqCtx, name, argsJSON string) (bool, string) {
	if reqCtx == nil || reqCtx.Principal.UserID == 0 {
		return true, ""
	}
	policy, exists := aiToolPolicies[name]
	if !exists || policy.Hidden || (policy.GlobalOnly && reqCtx.Principal.TenantID != 0) {
		return false, `{"error":"tool is not available to the AI assistant"}`
	}
	if !reqCtx.Principal.hasPermission(s.db, policy.Permission) {
		return false, fmt.Sprintf(`{"error":"missing permission %s"}`, policy.Permission)
	}
	if policy.Risk == aiToolRiskRead || reqCtx.ApprovedIntent || (policy.Risk == aiToolRiskLowRiskWrite && reqCtx.AllowLowRiskWrites) {
		return true, ""
	}
	intent := db.AIExecutionIntent{
		ID: uuid.NewString(), TenantID: reqCtx.Principal.TenantID, OwnerID: reqCtx.Principal.UserID,
		Owner: reqCtx.Principal.Username, SessionID: reqCtx.SessionID, RunID: reqCtx.RunID,
		ToolName: name, Risk: policy.Risk, RequiredPerm: policy.Permission,
		TargetSummary: summarizeAIArguments(argsJSON), Arguments: argsJSON,
		ArgumentsDigest: summarizeAIArguments(argsJSON), Status: "pending",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&intent).Error; err != nil {
		return false, `{"error":"failed to create approval intent"}`
	}
	if reqCtx.RunID != "" {
		s.db.Model(&db.AIChatRun{}).Where("id = ?", reqCtx.RunID).Update("status", aiRunStatusWaitingApproval)
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"status": "pending_approval", "intent_id": intent.ID, "tool": name,
		"risk": policy.Risk, "required_permission": policy.Permission,
		"arguments_summary": intent.ArgumentsDigest,
	})
	return false, string(payload)
}

func (s *Server) handleAIIntentsList(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	query := s.db.Where("tenant_id = ?", principal.TenantID)
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	} else {
		query = query.Where("status = ?", "pending")
	}
	var intents []db.AIExecutionIntent
	if err := query.Order("created_at DESC").Limit(100).Find(&intents).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to list approval intents")
		return
	}
	visible := intents[:0]
	for _, intent := range intents {
		if intent.RequiredPerm == "" || principal.hasPermission(s.db, intent.RequiredPerm) {
			intent.Arguments = ""
			intent.Result = ""
			visible = append(visible, intent)
		}
	}
	respond(c, visible)
}

func (s *Server) findApprovableAIIntent(c *gin.Context) (db.AIExecutionIntent, aiPrincipal, bool) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return db.AIExecutionIntent{}, principal, false
	}
	var intent db.AIExecutionIntent
	if err := s.db.Where("id = ? AND tenant_id = ?", c.Param("id"), principal.TenantID).First(&intent).Error; err != nil {
		respondError(c, http.StatusNotFound, "approval intent not found")
		return intent, principal, false
	}
	if intent.RequiredPerm != "" && !principal.hasPermission(s.db, intent.RequiredPerm) {
		respondError(c, http.StatusForbidden, "permission required to decide this intent")
		return intent, principal, false
	}
	if intent.Status != "pending" {
		respondError(c, http.StatusConflict, "approval intent is no longer pending")
		return intent, principal, false
	}
	return intent, principal, true
}

func (s *Server) handleAIIntentApprove(c *gin.Context) {
	intent, principal, ok := s.findApprovableAIIntent(c)
	if !ok {
		return
	}
	now := time.Now()
	claim := s.db.Model(&db.AIExecutionIntent{}).Where("id = ? AND status = ?", intent.ID, "pending").Updates(map[string]interface{}{
		"status": "executing", "decided_by": principal.Username, "decided_at": now, "updated_at": now,
	})
	if claim.Error != nil {
		respondError(c, http.StatusInternalServerError, "failed to approve intent")
		return
	}
	if claim.RowsAffected == 0 {
		respondError(c, http.StatusConflict, "approval intent is no longer pending")
		return
	}
	result := s.executeToolCtx(&aiReqCtx{
		Principal: principal, SessionID: intent.SessionID, RunID: intent.RunID, ApprovedIntent: true,
	}, intent.ToolName, intent.Arguments)
	summary := truncateString(result, 500)
	intent.Status, intent.DecidedBy, intent.DecidedAt = "executed", principal.Username, &now
	intent.Result, intent.ResultDigest, intent.UpdatedAt = result, summary, time.Now()
	var resultBody map[string]interface{}
	if json.Unmarshal([]byte(result), &resultBody) == nil {
		if taskID, ok := resultBody["task_id"].(float64); ok && taskID > 0 {
			value := uint(taskID)
			intent.TaskID = &value
		}
	}
	if err := s.db.Save(&intent).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to approve intent")
		return
	}
	s.appendAIRunEvent(intent.RunID, "tool_result", result)
	s.finishRunIfNoPendingIntents(intent.RunID)
	s.LogOperatorAction(c, OperatorAction{Action: "ai_intent_approve", Resource: "ai", TargetID: intent.ID, RiskLevel: intent.Risk, Details: intent.ArgumentsDigest})
	respond(c, gin.H{"id": intent.ID, "status": "executed", "result": summary})
}

func (s *Server) handleAIIntentReject(c *gin.Context) {
	intent, principal, ok := s.findApprovableAIIntent(c)
	if !ok {
		return
	}
	now := time.Now()
	result := s.db.Model(&db.AIExecutionIntent{}).Where("id = ? AND status = ?", intent.ID, "pending").Updates(map[string]interface{}{
		"status": "rejected", "decided_by": principal.Username, "decided_at": now, "updated_at": now,
	})
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "failed to reject intent")
		return
	}
	if result.RowsAffected == 0 {
		respondError(c, http.StatusConflict, "approval intent is no longer pending")
		return
	}
	s.appendAIRunEvent(intent.RunID, "tool_result", `{"status":"rejected"}`)
	s.finishRunIfNoPendingIntents(intent.RunID)
	s.LogOperatorAction(c, OperatorAction{Action: "ai_intent_reject", Resource: "ai", TargetID: intent.ID, RiskLevel: intent.Risk, Details: intent.ArgumentsDigest})
	respond(c, gin.H{"id": intent.ID, "status": "rejected"})
}

func (s *Server) appendAIRunEvent(runID, eventType, payload string) {
	if runID == "" {
		return
	}
	var run db.AIChatRun
	if s.db.Select("id", "last_event_seq").Where("id = ?", runID).First(&run).Error != nil {
		return
	}
	sequence := run.LastEventSeq + 1
	event := db.AIChatRunEvent{RunID: runID, Sequence: sequence, Type: eventType, Payload: payload}
	if s.db.Create(&event).Error == nil {
		s.db.Model(&db.AIChatRun{}).Where("id = ?", runID).Update("last_event_seq", sequence)
		if s.aiRuns != nil {
			s.aiRuns.publish(runID, aiLiveEvent{Sequence: sequence, Type: eventType, Payload: payload}, false)
		}
	}
}

func (s *Server) finishRunIfNoPendingIntents(runID string) {
	if runID == "" {
		return
	}
	var pending int64
	s.db.Model(&db.AIExecutionIntent{}).Where("run_id = ? AND status = ?", runID, "pending").Count(&pending)
	if pending == 0 {
		s.db.Model(&db.AIChatRun{}).Where("id = ? AND status = ?", runID, aiRunStatusWaitingApproval).Update("status", aiRunStatusCompleted)
	}
}
