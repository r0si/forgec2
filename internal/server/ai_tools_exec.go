package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// ── Tool switch (one case per tool) ───────────────────────────────────────
// NOTE: this is a single large dispatch function by design — each case is an
// independent tool handler sharing the tolerant args pre-parse below. Split
// further only by extracting whole cases into helpers, never by reordering.

func (s *Server) executeToolSwitchCtx(reqCtx *aiReqCtx, name string, argsJSON string) string {
	// Tolerant pre-parse (see executeToolCtx): string-valued keys still land
	// in the map even when array/number keys make Unmarshal return an error.
	// Tools that need non-string values re-parse argsJSON themselves.
	var args map[string]string
	_ = json.Unmarshal([]byte(argsJSON), &args)

	switch name {
	case "list_agents":
		var p struct {
			Status   string `json:"status"`
			OS       string `json:"os"`
			Query    string `json:"query"`
			Elevated *bool  `json:"elevated"`
			Limit    int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Limit <= 0 || p.Limit > 50 {
			p.Limit = 30
		}
		q := s.db.Model(&db.Implant{})
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			q = q.Where("tenant_id = ?", reqCtx.Principal.TenantID)
		}
		switch strings.ToLower(strings.TrimSpace(p.Status)) {
		case "online", "offline":
			q = q.Where("status = ?", strings.ToLower(strings.TrimSpace(p.Status)))
		}
		if osFilter := strings.TrimSpace(p.OS); osFilter != "" {
			q = q.Where("os LIKE ?", "%"+osFilter+"%")
		}
		if query := strings.TrimSpace(p.Query); query != "" {
			like := "%" + query + "%"
			q = q.Where("id LIKE ? OR hostname LIKE ? OR ip LIKE ? OR username LIKE ?", like, like, like, like)
		}
		if p.Elevated != nil && *p.Elevated {
			q = q.Where("elevated = ?", true)
		}
		var agents []db.Implant
		if err := q.Order("last_seen desc").Limit(p.Limit).Find(&agents).Error; err != nil {
			slog.Error("AI: failed to list agents", "err", err)
			return `{"error":"failed to list agents"}`
		}
		out := []map[string]interface{}{}
		for _, a := range agents {
			row := map[string]interface{}{
				"id": a.ID, "hostname": a.Hostname, "ip": a.IP,
				"os": a.OS, "username": a.Username,
				"status": a.Status, "elevated": a.Elevated,
				"last_seen": a.LastSeen.Format(time.RFC3339),
				"stale":     implantIsStale(a),
			}
			if a.Domain != "" {
				row["domain"] = a.Domain
			}
			if a.Integrity != "" {
				row["integrity"] = a.Integrity
			}
			if a.CurrentInterval > 0 {
				row["sleep_s"] = a.CurrentInterval
				row["jitter"] = a.CurrentJitter
			}
			out = append(out, row)
		}
		b, ok := marshalJSONSafe(map[string]interface{}{"count": len(out), "agents": out})
		if !ok {
			return `{"error":"failed to marshal agents"}`
		}
		return string(b)

	case "get_agent_detail":
		aid := s.resolveAIAgentID(reqCtx, args["agent_id"])
		if aid == "" {
			return `{"error":"agent not found"}`
		}
		var agent db.Implant
		if err := s.db.Where("id = ?", aid).First(&agent).Error; err != nil {
			return `{"error":"agent not found"}`
		}
		var taskCount int64
		if err := s.db.Model(&db.Task{}).Where("agent_id = ?", agent.ID).Count(&taskCount).Error; err != nil {
			slog.Error("Failed to count agent tasks", "err", err)
		}
		d := map[string]interface{}{
			"id": agent.ID, "hostname": agent.Hostname, "ip": agent.IP,
			"os": agent.OS, "arch": agent.Arch, "username": agent.Username,
			"domain": agent.Domain, "status": agent.Status, "integrity": agent.Integrity,
			"pid": agent.PID, "process": agent.ProcessName, "elevated": agent.Elevated,
			"task_count": taskCount, "sleep_s": agent.CurrentInterval, "jitter": agent.CurrentJitter,
			"last_seen": agent.LastSeen.Format(time.RFC3339), "stale": implantIsStale(agent),
		}
		if agent.Notes != "" {
			d["notes"] = truncateStr(agent.Notes, 240)
		}
		if agent.Tags != "" {
			d["tags"] = agent.Tags
		}
		var recent []db.Task
		if err := s.db.Where("agent_id = ?", agent.ID).Order("created_at desc").Limit(3).Find(&recent).Error; err == nil && len(recent) > 0 {
			var brief []map[string]interface{}
			for _, t := range recent {
				item := map[string]interface{}{
					"id": t.ID, "type": t.Type, "status": t.Status,
					"created_at": t.CreatedAt.Format(time.RFC3339),
				}
				if t.Command != "" {
					item["command"] = truncateStr(t.Command, 120)
				}
				if t.Error != "" {
					item["error"] = truncateStr(t.Error, 200)
				}
				brief = append(brief, item)
			}
			d["recent_tasks"] = brief
		}
		b, ok := marshalJSONSafe(d)
		if !ok {
			return `{"error":"failed to marshal agent detail"}`
		}
		return string(b)

	case "execute_command":
		ecArgs := parseExecuteCommandArgs(argsJSON)
		aid := s.resolveAIAgentID(reqCtx, ecArgs.AgentID)
		if aid == "" {
			return `{"error":"agent not found"}`
		}
		shell := ecArgs.Shell
		if shell == "" {
			shell = "cmd.exe"
		}
		allowExec := s.aiExecutionEnabled()
		sensitive := isSensitiveCommand(ecArgs.Command)
		status := TaskStatusPendingApproval
		// allow_execute is a SUBSET gate: server-wide RequireApproval for
		// approval-flagged task types (resolveInitialTaskStatus) still wins,
		// so ai.allow_execute can never bypass the operator two-man rule.
		if allowExec && !sensitive && s.resolveInitialTaskStatus("shell") == "pending" {
			status = "pending"
		}
		// Enforce the per-agent pending cap + counter parity with the
		// manual dispatch path (dec happens on claim/cancel/reject).
		task, err := s.createSystemTask(aid, "shell", ecArgs.Command, shell, status, "ai")
		if err != nil {
			b, _ := marshalJSONSafe(map[string]interface{}{"error": sanitizeError(err, "task")})
			return string(b)
		}
		if status == "pending" {
			s.broadcastTaskUpdate(aid, *task)
		}
		if sensitive && allowExec {
			b, ok := marshalJSONSafe(map[string]interface{}{
				"task_id": task.ID,
				"status":  "pending_approval",
				"message": "Sensitive command requires human approval even with allow_execute enabled. Approve it in the Tasks page.",
				"reason":  "sensitive_command",
			})
			if !ok {
				return `{"error":"failed to marshal task result"}`
			}
			return string(b)
		}
		if status == "pending" && ecArgs.WaitForResult {
			return s.waitForTaskResult(task.ID, aid)
		}
		if status == TaskStatusPendingApproval {
			b, ok := marshalJSONSafe(map[string]interface{}{
				"task_id": task.ID,
				"status":  "pending_approval",
				"message": "Command created but requires operator approval before the agent executes it. Approve it in the Tasks page.",
			})
			if !ok {
				return `{"error":"failed to marshal task result"}`
			}
			return string(b)
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"task_id": task.ID,
			"status":  status,
			"message": "Command queued for execution.",
		})
		if !ok {
			return `{"error":"failed to marshal task result"}`
		}
		return string(b)

	case "get_agent_tasks":
		aid := s.resolveAIAgentID(reqCtx, args["agent_id"])
		if aid == "" {
			return `{"error":"agent not found"}`
		}
		var tasks []db.Task
		if err := s.db.Where("agent_id = ?", aid).Order("created_at desc").Limit(10).Find(&tasks).Error; err != nil {
			slog.Error("AI: failed to query agent tasks", "err", err)
		}
		out := []map[string]interface{}{}
		for _, t := range tasks {
			r := map[string]interface{}{
				"id": t.ID, "type": t.Type, "command": t.Command,
				"status": t.Status, "created_at": t.CreatedAt.Format(time.RFC3339),
			}
			if t.Result != "" {
				r["result"] = truncateStr(redactSensitiveResultForAuxiliary(t.Type, t.Result), AIToolResultTruncLen)
			}
			if t.Error != "" {
				r["error"] = t.Error
			}
			out = append(out, r)
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal tasks"}`
		}
		return string(b)

	case "list_listeners":
		var listeners []db.Listener
		if err := s.aiTenantScope(s.db.Order("created_at desc"), reqCtx).Limit(500).Find(&listeners).Error; err != nil {
			slog.Error("AI: failed to list listeners", "err", err)
		}
		out := []map[string]interface{}{}
		for _, l := range listeners {
			out = append(out, map[string]interface{}{
				"id": l.ID, "name": l.Name, "type": l.Type,
				"host": l.Host, "port": l.Port, "enabled": l.Enabled,
			})
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal listeners"}`
		}
		return string(b)

	case "list_credentials":
		var creds []db.CredentialEntry
		credentialQuery := s.db.Order("created_at desc").Limit(100)
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			credentialQuery = credentialQuery.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		if err := credentialQuery.Find(&creds).Error; err != nil {
			slog.Error("AI: failed to list credentials", "err", err)
		}
		out := []map[string]interface{}{}
		for _, c := range creds {
			entry := map[string]interface{}{
				"id": c.ID, "domain": c.Domain, "username": c.Username,
				"type": c.Type, "source": c.Source, "has_password": c.Password != "",
				"has_hash": c.Hash != "",
			}
			out = append(out, entry)
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal credentials"}`
		}
		return string(b)

	case "get_online_operators":
		var ops []map[string]interface{}
		if s.operatorSessions != nil {
			ops = s.operatorSessions.OperatorPresenceSnapshot()
		}
		if ops == nil {
			ops = []map[string]interface{}{}
		}
		b, ok := marshalJSONSafe(map[string]interface{}{"count": len(ops), "operators": ops})
		if !ok {
			return `{"error":"failed to marshal operators"}`
		}
		return string(b)

	case "search_tasks":
		var q struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &q)
		if q.Query == "" {
			q.Query = args["query"]
		}
		if q.Limit <= 0 || q.Limit > 50 {
			q.Limit = 10
		}
		like := "%" + q.Query + "%"
		var tasks []db.Task
		taskQuery := s.db.Where("command LIKE ? OR result LIKE ?", like, like)
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			taskQuery = taskQuery.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		if err := taskQuery.Order("created_at desc").Limit(q.Limit).Find(&tasks).Error; err != nil {
			return `{"error":"failed to search tasks"}`
		}
		out := []map[string]interface{}{}
		for _, t := range tasks {
			out = append(out, map[string]interface{}{
				"id": t.ID, "agent_id": t.AgentID, "type": t.Type, "command": truncateStr(t.Command, 200),
				"status": t.Status, "result": truncateStr(redactSensitiveResultForAuxiliary(t.Type, t.Result), 500),
			})
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal tasks"}`
		}
		return string(b)

	case "get_timeline":
		var p struct {
			AgentID string `json:"agent_id"`
			Limit   int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.AgentID == "" {
			p.AgentID = args["agent_id"]
		}
		if p.AgentID == "" {
			return `{"error":"agent_id required"}`
		}
		if p.Limit <= 0 || p.Limit > 100 {
			p.Limit = 30
		}
		aid2 := s.resolveAIAgentID(reqCtx, p.AgentID)
		if aid2 == "" {
			return `{"error":"agent not found"}`
		}
		// tasks + recent creds + status events (screenshots are filesystem, skip)
		var tasks2 []db.Task
		s.db.Where("agent_id = ?", aid2).Order("created_at desc").Limit(p.Limit).Find(&tasks2)
		var creds []db.CredentialEntry
		s.db.Where("agent_id = ?", aid2).Order("created_at desc").Limit(10).Find(&creds)
		var events []map[string]interface{}
		for _, t := range tasks2 {
			events = append(events, map[string]interface{}{"kind": "task", "type": t.Type, "command": truncateStr(t.Command, 120), "status": t.Status, "time": t.CreatedAt.Format(time.RFC3339), "result": truncateStr(redactSensitiveResultForAuxiliary(t.Type, t.Result), 300)})
		}
		for _, c := range creds {
			events = append(events, map[string]interface{}{"kind": "credential", "username": c.Username, "domain": c.Domain, "type": c.Type, "time": c.CreatedAt.Format(time.RFC3339)})
		}
		b, ok := marshalJSONSafe(events)
		if !ok {
			return `{"error":"failed to marshal timeline"}`
		}
		return string(b)

	case "query_ioc":
		var p struct {
			Days int    `json:"days"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Days <= 0 || p.Days > 365 {
			p.Days = 30
		}
		entries, _, err := s.extractIOCs(p.Days, false, s.principalAgentIDs(reqCtx))
		if err != nil {
			return fmt.Sprintf(`{"error":%q}`, err.Error())
		}
		var filtered []iocEntry
		for _, e := range entries {
			if p.Type != "" && e.Type != p.Type {
				continue
			}
			filtered = append(filtered, e)
			if len(filtered) >= 20 {
				break
			}
		}
		b, ok := marshalJSONSafe(filtered)
		if !ok {
			return `{"error":"failed to marshal iocs"}`
		}
		return string(b)

	case "list_macros":
		var macros []db.CommandMacro
		s.aiTenantScope(s.db.Order("name"), reqCtx).Limit(AutomationRuleLimit).Find(&macros)
		out := []map[string]interface{}{}
		for _, m := range macros {
			var steps []interface{}
			_ = json.Unmarshal([]byte(m.Steps), &steps)
			out = append(out, map[string]interface{}{"id": m.ID, "name": m.Name, "description": m.Description, "steps": len(steps)})
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal macros"}`
		}
		return string(b)

	case "run_macro":
		if !s.aiExecutionEnabled() {
			return `{"error":"allow_execute is disabled in AI config; enable it to run macros"}`
		}
		var p struct {
			MacroID   *uint    `json:"macro_id"`
			MacroName string   `json:"macro_name"`
			AgentIDs  []string `json:"agent_ids"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if len(p.AgentIDs) == 0 {
			return `{"error":"agent_ids required (max 10)"}`
		}
		if len(p.AgentIDs) > 10 {
			p.AgentIDs = p.AgentIDs[:10]
		}
		var macro db.CommandMacro
		if p.MacroID != nil && *p.MacroID != 0 {
			if err := s.aiTenantScope(s.db, reqCtx).First(&macro, *p.MacroID).Error; err != nil {
				return `{"error":"macro not found"}`
			}
		} else if p.MacroName != "" {
			if err := s.aiTenantScope(s.db, reqCtx).Where("name = ?", p.MacroName).First(&macro).Error; err != nil {
				return `{"error":"macro not found by name"}`
			}
		} else {
			return `{"error":"macro_id or macro_name required"}`
		}
		dispatched := 0
		for _, aid := range p.AgentIDs {
			resolved := s.resolveAIAgentID(reqCtx, aid)
			if resolved == "" {
				continue
			}
			if err := s.startMacroRun(&macro, resolved, "ai", true); err == nil {
				dispatched++
			}
		}
		b, ok := marshalJSONSafe(map[string]interface{}{"dispatched": dispatched, "macro": macro.Name})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "create_automation_rule":
		if !s.aiExecutionEnabled() {
			return `{"error":"allow_execute is disabled; enable it to create automation rules"}`
		}
		var p struct {
			Name      string `json:"name"`
			EventType string `json:"event_type"`
			Command   string `json:"command"`
			MacroID   *uint  `json:"macro_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Name == "" || p.EventType == "" {
			return `{"error":"name and event_type required (event_type: agent.checkin, agent.disconnect, task.complete, task.fail, credential.found)"}`
		}
		validEvents := map[string]bool{"agent.checkin": true, "agent.disconnect": true, "task.complete": true, "task.fail": true, "credential.found": true}
		if !validEvents[p.EventType] {
			return `{"error":"invalid event_type"}`
		}
		var actionType, params string
		if p.MacroID != nil && *p.MacroID != 0 {
			b2, _ := json.Marshal(map[string]interface{}{"macro_id": *p.MacroID, "stop_on_error": true})
			actionType = "run_macro"
			params = string(b2)
		} else if p.Command != "" {
			b2, _ := json.Marshal(map[string]string{"command": p.Command})
			actionType = "command"
			params = string(b2)
		} else {
			return `{"error":"command or macro_id required"}`
		}
		rule := db.AutomationRule{
			ID:         fmt.Sprintf("ai-%d", time.Now().UnixNano()),
			Name:       p.Name,
			EventType:  p.EventType,
			Conditions: "[]",
			Actions:    fmt.Sprintf(`[{"type":"%s","params":%s}]`, actionType, params),
			Enabled:    true,
			CreatedBy:  "ai",
		}
		if err := s.db.Create(&rule).Error; err != nil {
			return `{"error":"failed to create rule"}`
		}
		s.invalidateAutomationCache()
		b, ok := marshalJSONSafe(map[string]interface{}{"id": rule.ID, "name": rule.Name, "event_type": rule.EventType})
		if !ok {
			return `{"error":"failed to marshal rule"}`
		}
		return string(b)

	case "get_attack_surface":
		var p struct {
			AgentID string `json:"agent_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.AgentID == "" {
			p.AgentID = args["agent_id"]
		}
		if p.AgentID == "" {
			return `{"error":"agent_id required"}`
		}
		aid := s.resolveAIAgentID(reqCtx, p.AgentID)
		if aid == "" {
			return `{"error":"agent not found"}`
		}
		var ag db.Implant
		if err := s.db.Where("id = ?", aid).First(&ag).Error; err != nil {
			return `{"error":"agent not found"}`
		}
		var credCount int64
		s.db.Model(&db.CredentialEntry{}).Where("agent_id = ?", aid).Count(&credCount)
		var recentTasks []db.Task
		s.db.Where("agent_id = ?", aid).Order("created_at desc").Limit(5).Find(&recentTasks)
		var recentLateral []db.Task
		s.db.Where("agent_id = ? AND type IN ?", aid, []string{"lateral", "ssh_lateral", "token_steal", "token_make"}).Order("created_at desc").Limit(5).Find(&recentLateral)
		surface := map[string]interface{}{
			"agent": map[string]interface{}{
				"id": aid, "hostname": ag.Hostname, "ip": ag.IP, "os": ag.OS, "arch": ag.Arch,
				"username": ag.Username, "domain": ag.Domain, "integrity": ag.Integrity,
				"elevated": ag.Elevated, "pid": ag.PID, "status": ag.Status, "version": ag.Version,
			},
			"credentials_nearby": credCount,
			"recent_tasks":       recentTasks,
			"recent_lateral":     recentLateral,
			"hint":               "Use this surface to recommend the next step (e.g., lateral movement, cred dumping, persistence). Consider OS, privilege level, and nearby creds.",
		}
		b, ok := marshalJSONSafe(surface)
		if !ok {
			return `{"error":"failed to marshal surface"}`
		}
		return string(b)

	case "get_task_detail":
		var p struct {
			TaskID uint `json:"task_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.TaskID == 0 {
			if v, err := strconv.ParseUint(args["task_id"], 10, 64); err == nil {
				p.TaskID = uint(v)
			}
		}
		if p.TaskID == 0 {
			return `{"error":"numeric task_id required"}`
		}
		var task db.Task
		if err := s.db.First(&task, p.TaskID).Error; err != nil {
			return `{"error":"task not found"}`
		}
		if reqCtx != nil && reqCtx.Principal.UserID != 0 && s.resolveAIAgentID(reqCtx, task.AgentID) == "" {
			return `{"error":"task not found"}`
		}
		out := map[string]interface{}{
			"id": task.ID, "agent_id": task.AgentID, "type": task.Type,
			"command": task.Command, "status": task.Status,
			"created_at": task.CreatedAt.Format(time.RFC3339),
			"created_by": task.CreatedBy,
		}
		// Full result/error (context-bounded): this is the diagnosis tool —
		// truncation here defeats its purpose.
		const maxDetail = 16 * 1024
		if task.Result != "" {
			out["result"] = truncateStr(redactSensitiveResultForAuxiliary(task.Type, task.Result), maxDetail)
		}
		if task.Error != "" {
			out["error"] = truncateStr(task.Error, maxDetail)
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal task"}`
		}
		return string(b)

	case "execute_command_bulk":
		var p struct {
			AgentIDs []string `json:"agent_ids"`
			Command  string   `json:"command"`
			Shell    string   `json:"shell"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if len(p.AgentIDs) == 0 || strings.TrimSpace(p.Command) == "" {
			return `{"error":"agent_ids and command required (max 20 agents)"}`
		}
		if len(p.AgentIDs) > 20 {
			p.AgentIDs = p.AgentIDs[:20]
		}
		allowExec := s.aiExecutionEnabled()
		sensitive := isSensitiveCommand(p.Command)
		status := "pending_approval"
		// Same subset-gate as execute_command: server RequireApproval wins.
		if allowExec && !sensitive && s.resolveInitialTaskStatus("shell") == "pending" {
			status = "pending"
		}
		if p.Shell == "" {
			p.Shell = "cmd.exe"
		}
		type bulkResult struct {
			AgentID string `json:"agent_id"`
			TaskID  uint   `json:"task_id,omitempty"`
			Status  string `json:"status,omitempty"`
			Error   string `json:"error,omitempty"`
		}
		var results []bulkResult
		for _, aid := range p.AgentIDs {
			resolved := s.resolveAIAgentID(reqCtx, aid)
			if resolved == "" {
				results = append(results, bulkResult{AgentID: aid, Error: "agent not found"})
				continue
			}
			task, err := s.createSystemTask(resolved, "shell", p.Command, p.Shell, status, "ai")
			if err != nil {
				results = append(results, bulkResult{AgentID: resolved, Error: sanitizeError(err, "task")})
				continue
			}
			if status == "pending" {
				s.broadcastTaskUpdate(resolved, *task)
			}
			results = append(results, bulkResult{AgentID: resolved, TaskID: task.ID, Status: status})
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"status":            status,
			"sensitive":         sensitive,
			"results":           results,
			"pending_tasks_url": "/tasks",
		})
		if !ok {
			return `{"error":"failed to marshal results"}`
		}
		return string(b)

	case "query_bloodhound":
		var p struct {
			AgentID string `json:"agent_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		q := s.db.Model(&db.BloodHoundResult{})
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			q = q.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		if p.AgentID != "" {
			q = q.Where("agent_id = ?", s.resolveAIAgentID(reqCtx, p.AgentID))
		}
		var bh db.BloodHoundResult
		if err := q.Order("id desc").First(&bh).Error; err != nil {
			return `{"error":"no bloodhound collections found. Run a collection from the BloodHound page first."}`
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"id":                 bh.ID,
			"agent_id":           bh.AgentID,
			"collection_method":  bh.CollectionMethod,
			"summary":            truncateStr(bh.Summary, AIToolResultTruncLen*2),
			"user_count":         bh.UserCount,
			"computer_count":     bh.ComputerCount,
			"group_count":        bh.GroupCount,
			"session_count":      bh.SessionCount,
			"domain_admin_count": bh.DomainAdminCount,
		})
		if !ok {
			return `{"error":"failed to marshal bloodhound data"}`
		}
		return string(b)

	case "get_coverage_gaps":
		usedTypes := map[string]bool{}
		var types []string
		// Tenant gate: coverage must reflect the caller's own fleet, not
		// every tenant's task types.
		typeQ := s.db.Table("tasks").Distinct()
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			typeQ = typeQ.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		if err := typeQ.Pluck("type", &types).Error; err != nil {
			types = nil
		}
		for _, t := range types {
			usedTypes[t] = true
		}
		type gapTactic struct {
			Tactic     string   `json:"tactic"`
			Techniques []string `json:"techniques"`
		}
		var covered, gaps []gapTactic
		gapIndex := map[string]int{}
		for _, grp := range attackTacticMap {
			for _, tech := range grp.Techniques {
				isCovered := false
				for _, tt := range tech.TaskTypes {
					if usedTypes[tt] {
						isCovered = true
						break
					}
				}
				entry := fmt.Sprintf("%s %s (%s)", tech.ID, tech.Name, strings.Join(tech.TaskTypes, ","))
				idx := -1
				target := &covered
				if !isCovered {
					if gi, ok := gapIndex[grp.Tactic]; ok {
						idx = gi
					} else {
						gaps = append(gaps, gapTactic{Tactic: grp.Tactic})
						idx = len(gaps) - 1
						gapIndex[grp.Tactic] = idx
					}
					target = &gaps
				} else {
					found := false
					for i := range covered {
						if covered[i].Tactic == grp.Tactic {
							idx = i
							found = true
							break
						}
					}
					if !found {
						covered = append(covered, gapTactic{Tactic: grp.Tactic})
						idx = len(covered) - 1
					}
				}
				(*target)[idx].Techniques = append((*target)[idx].Techniques, entry)
			}
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"covered_tactics": covered,
			"gaps_by_tactic":  gaps,
			"used_task_types": types,
			"hint":            "Recommend gaps that complement existing access: with creds but no persistence, suggest persistence; with discovery but no collection, suggest screen/keylog capture.",
		})
		if !ok {
			return `{"error":"failed to marshal coverage"}`
		}
		return string(b)

	case "save_engagement_note":
		var p struct {
			Note string `json:"note"`
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		note := strings.TrimSpace(p.Note)
		if note == "" {
			return `{"error":"note required"}`
		}
		s.configMu.Lock()
		current := s.cfg.AI.EngagementNotes
		if p.Mode == "replace" {
			if len(note) > aiMaxNotesLen {
				s.configMu.Unlock()
				return `{"error":"note too large (max 8000 chars in replace mode)"}`
			}
			s.cfg.AI.EngagementNotes = note
		} else {
			stamp := time.Now().UTC().Format("2006-01-02")
			addition := fmt.Sprintf("\n- [%s] %s", stamp, note)
			if len(current)+len(addition) > aiMaxNotesLen {
				s.configMu.Unlock()
				return fmt.Sprintf(`{"error":"engagement memory full (%d chars). Use mode=replace or prune via AI settings."}`, aiMaxNotesLen)
			}
			s.cfg.AI.EngagementNotes = current + addition
		}
		s.configMu.Unlock()
		configPath := s.configPath
		if configPath == "" {
			configPath = "config.yaml"
		}
		if err := s.cfg.Save(configPath); err != nil {
			return `{"error":"failed to persist note: ` + sanitizeError(err, "AI operation") + `"}`
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"saved": true, "mode": p.Mode, "total_chars": len(s.cfg.AI.EngagementNotes),
		})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "list_pending_tasks":
		var p struct {
			Creator string `json:"creator"`
			Limit   int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Limit <= 0 || p.Limit > 50 {
			p.Limit = 20
		}
		q := s.db.Where("status IN ?", []string{"pending", TaskStatusPendingApproval})
		// Tenant gate: only surface tasks of caller-visible agents (same
		// agent-subquery pattern as list_credentials/search_tasks above).
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			q = q.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		switch p.Creator {
		case "ai":
			q = q.Where("created_by = ?", "ai")
		case "human":
			q = q.Where("created_by NOT IN ?", []string{"ai", "automation", "system"})
		}
		var tasks []db.Task
		q.Order("created_at desc").Limit(p.Limit).Find(&tasks)
		out := []map[string]interface{}{}
		for _, t := range tasks {
			out = append(out, map[string]interface{}{
				"id": t.ID, "agent_id": t.AgentID, "type": t.Type,
				"command":    truncateStr(t.Command, 200),
				"status":     t.Status,
				"created_by": t.CreatedBy,
				"sensitive":  isSensitiveCommand(t.Command),
				"created_at": t.CreatedAt.Format(time.RFC3339),
			})
		}
		b, ok := marshalJSONSafe(out)
		if !ok {
			return `{"error":"failed to marshal tasks"}`
		}
		return string(b)

	case "bulk_task_action":
		var p struct {
			TaskIDs []uint `json:"task_ids"`
			Action  string `json:"action"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if len(p.TaskIDs) == 0 {
			return `{"error":"task_ids required (max 50)"}`
		}
		if len(p.TaskIDs) > 50 {
			p.TaskIDs = p.TaskIDs[:50]
		}
		if p.Action != "approve" && p.Action != "cancel" {
			return `{"error":"action must be \"approve\" or \"cancel\""}`
		}
		allowExec := s.aiExecutionEnabled()
		var approved, cancelled, skippedHuman, skippedSensitive, skippedOther int
		var audit []auditEntry
		type bulkErr struct {
			TaskID uint   `json:"task_id"`
			Error  string `json:"error"`
		}
		var errs []bulkErr
		for _, tid := range p.TaskIDs {
			var task db.Task
			if err := s.db.First(&task, tid).Error; err != nil {
				errs = append(errs, bulkErr{TaskID: tid, Error: "not found"})
				continue
			}
			// Tenant gate: one tenant's AI must not approve/cancel another
			// tenant's AI tasks (defeats the two-man rule across tenants).
			if reqCtx != nil && reqCtx.Principal.UserID != 0 {
				var visible int64
				s.db.Model(&db.Implant{}).Where("id = ? AND tenant_id = ?", task.AgentID, reqCtx.Principal.TenantID).Count(&visible)
				if visible == 0 {
					errs = append(errs, bulkErr{TaskID: tid, Error: "not found"})
					continue
				}
			}
			if task.Status != "pending" && task.Status != TaskStatusPendingApproval {
				skippedOther++
				continue
			}
			// The AI assistant may only act on its own tasks. Human-created
			// tasks are untouchable here — that preserves the two-man rule.
			if task.CreatedBy != "ai" {
				skippedHuman++
				continue
			}
			if p.Action == "cancel" {
				res := s.db.Model(&db.Task{}).
					Where("id = ? AND agent_id = ? AND status IN ?", tid, task.AgentID, []string{"pending", TaskStatusPendingApproval}).
					Updates(map[string]interface{}{"status": "cancelled", "error": "cancelled by AI assistant"})
				if res.Error != nil || res.RowsAffected == 0 {
					errs = append(errs, bulkErr{TaskID: tid, Error: "state changed concurrently"})
					continue
				}
				s.decPendingTasks(task.AgentID)
				cancelled++
				audit = append(audit, auditEntry{
					action: "ai_bulk_cancel_task", resource: "agent_id", agentID: task.AgentID,
					details: fmt.Sprintf("actor=ai Cancelled AI task #%d (%s)", tid, task.Type), success: true,
				})
				fresh := task
				fresh.Status = "cancelled"
				s.broadcastTaskUpdate(task.AgentID, fresh)
			} else { // approve
				// Sensitive commands can NEVER be auto-approved by the AI,
				// even with allow_execute — a human must press the button.
				if !allowExec {
					skippedOther++
					continue
				}
				if isSensitiveCommand(task.Command) {
					skippedSensitive++
					continue
				}
				res := s.db.Model(&db.Task{}).
					Where("id = ? AND agent_id = ? AND status = ?", tid, task.AgentID, TaskStatusPendingApproval).
					Updates(map[string]interface{}{"status": "pending", "approved_by": "ai", "approved_at": time.Now()})
				if res.Error != nil || res.RowsAffected == 0 {
					errs = append(errs, bulkErr{TaskID: tid, Error: "state changed concurrently"})
					continue
				}
				approved++
				audit = append(audit, auditEntry{
					action: "ai_bulk_approve_task", resource: "agent_id", agentID: task.AgentID,
					details: fmt.Sprintf("actor=ai Approved AI task #%d (%s)", tid, task.Type), success: true,
				})
				fresh := task
				fresh.Status = "pending"
				s.broadcastTaskUpdate(task.AgentID, fresh)
			}
		}
		// Chain-safe audit trail for every applied bulk action (nil context
		// records actor as system; details carry the ai attribution).
		if len(audit) > 0 {
			s.LogAuditRecords(nil, audit)
		}
		result := map[string]interface{}{
			"action":            p.Action,
			"approved":          approved,
			"cancelled":         cancelled,
			"skipped_human":     skippedHuman,
			"skipped_other":     skippedOther,
			"sensitive_blocked": skippedSensitive,
		}
		if len(errs) > 0 {
			result["errors"] = errs
		}
		b, ok := marshalJSONSafe(result)
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "get_screenshot_summary":
		var p struct {
			AgentID string `json:"agent_id"`
			Days    int    `json:"days"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Days <= 0 || p.Days > 365 {
			p.Days = 30
		}
		since := time.Now().AddDate(0, 0, -p.Days)
		q := s.db.Where("type IN ? AND created_at >= ?", []string{"screenshot", "screenshot_window", "screen_stream_start"}, since)
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			q = q.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		if p.AgentID != "" {
			aid := s.resolveAIAgentID(reqCtx, p.AgentID)
			if aid == "" {
				return `{"error":"agent not found"}`
			}
			q = q.Where("agent_id = ?", aid)
		}
		var tasks []db.Task
		q.Order("created_at desc").Limit(500).Find(&tasks)
		byAgent := map[string]int{}
		type shotItem struct {
			TaskID    uint   `json:"task_id"`
			AgentID   string `json:"agent_id"`
			Status    string `json:"status"`
			Size      int64  `json:"size_bytes"`
			CreatedAt string `json:"created_at"`
		}
		var recent []shotItem
		for i, t := range tasks {
			byAgent[t.AgentID]++
			if i < 5 {
				recent = append(recent, shotItem{TaskID: t.ID, AgentID: t.AgentID, Status: t.Status, Size: t.TotalBytes, CreatedAt: t.CreatedAt.Format(time.RFC3339)})
			}
		}
		var agentsOut []map[string]interface{}
		for aid, cnt := range byAgent {
			agentsOut = append(agentsOut, map[string]interface{}{"agent_id": aid, "count": cnt})
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"window_days": p.Days,
			"total":       len(tasks),
			"by_agent":    agentsOut,
			"recent":      recent,
			"note":        "Screenshot files are served from the Screenshots page; this is task-level metadata.",
		})
		if !ok {
			return `{"error":"failed to marshal summary"}`
		}
		return string(b)

	case "get_keylog_summary":
		var p struct {
			AgentID string `json:"agent_id"`
			Days    int    `json:"days"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Days <= 0 || p.Days > 365 {
			p.Days = 30
		}
		since := time.Now().AddDate(0, 0, -p.Days)
		q := s.db.Where("type = ? AND created_at >= ?", "keylogger_dump", since)
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			q = q.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID))
		}
		if p.AgentID != "" {
			aid := s.resolveAIAgentID(reqCtx, p.AgentID)
			if aid == "" {
				return `{"error":"agent not found"}`
			}
			q = q.Where("agent_id = ?", aid)
		}
		var tasks []db.Task
		q.Order("created_at desc").Limit(200).Find(&tasks)
		byAgent := map[string]int{}
		highValueKeywords := []string{"password", "passwd", "login", "logon", "credential", "token", "@gmail", "@outlook", "@yahoo", "@qq.", "@163.", "banking", "vpn"}
		type kvEntry struct {
			TaskID    uint   `json:"task_id"`
			AgentID   string `json:"agent_id"`
			Keyword   string `json:"keyword"`
			Context   string `json:"context"`
			CreatedAt string `json:"created_at"`
		}
		var hits []kvEntry
		totalBytes := int64(0)
		for _, t := range tasks {
			byAgent[t.AgentID]++
			totalBytes += int64(len(t.Result))
			lower := strings.ToLower(t.Result)
			for _, kw := range highValueKeywords {
				idx := strings.Index(lower, kw)
				if idx < 0 {
					continue
				}
				start := idx - 40
				if start < 0 {
					start = 0
				}
				end := idx + len(kw) + 40
				if end > len(t.Result) {
					end = len(t.Result)
				}
				ctx := t.Result[start:end]
				ctx = strings.ReplaceAll(ctx, "\n", " ")
				hits = append(hits, kvEntry{TaskID: t.ID, AgentID: t.AgentID, Keyword: kw, Context: truncateStr(ctx, 100), CreatedAt: t.CreatedAt.Format(time.RFC3339)})
				break // one hit per dump is enough for triage
			}
			if len(hits) >= 10 {
				break
			}
		}
		var agentsOut []map[string]interface{}
		for aid, cnt := range byAgent {
			agentsOut = append(agentsOut, map[string]interface{}{"agent_id": aid, "dumps": cnt})
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"window_days":     p.Days,
			"total_dumps":     len(tasks),
			"captured_bytes":  totalBytes,
			"by_agent":        agentsOut,
			"high_value_hits": hits,
			"note":            "Full keystroke dumps are on the agent detail page; this is a keyword-triage view.",
		})
		if !ok {
			return `{"error":"failed to marshal summary"}`
		}
		return string(b)

	case "generate_report":
		var p struct {
			Scope string `json:"scope"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Scope == "" {
			p.Scope = "full"
		}
		switch p.Scope {
		case "full", "executive", "technical", "coverage":
		default:
			return `{"error":"invalid scope (full|executive|technical|coverage)"}`
		}
		md, sections, err := s.buildAIMarkdownReport(p.Scope, reqCtx)
		if err != nil {
			return `{"error":"failed to build report: ` + sanitizeError(err, "report") + `"}`
		}
		title := strings.TrimSpace(p.Title)
		if title == "" {
			title = fmt.Sprintf("AI %s Report %s", strings.ToUpper(p.Scope[:1])+p.Scope[1:], time.Now().Format("2006-01-02 15:04"))
		}
		sectionsJSON, _ := json.Marshal(sections)
		rep := db.GeneratedReport{
			Name:     title,
			Template: "ai_" + p.Scope,
			Format:   "markdown",
			Content:  md,
			Sections: string(sectionsJSON),
		}
		if err := s.db.Create(&rep).Error; err != nil {
			return `{"error":"failed to save report"}`
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"report_id": rep.ID,
			"title":     title,
			"scope":     p.Scope,
			"chars":     len(md),
			"view_url":  "/report",
			"note":      "Saved to the report library. The operator can view/export it on the Report page.",
		})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "create_listener":
		if !s.aiExecutionEnabled() {
			return `{"error":"allow_execute is disabled in AI config; enable it to manage listeners"}`
		}
		var p struct {
			Name      string `json:"name"`
			Scheme    string `json:"scheme"`
			Host      string `json:"host"`
			Port      int    `json:"port"`
			DNSDomain string `json:"dns_domain"`
			ICMPAddr  string `json:"icmp_addr"`
			Notes     string `json:"notes"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		scheme := strings.ToLower(strings.TrimSpace(p.Scheme))
		if !validateListenerScheme(scheme) {
			b, _ := marshalJSONSafe(map[string]interface{}{"error": listenerSchemeHint(p.Scheme)})
			return string(b)
		}
		l := db.Listener{
			Name:      strings.TrimSpace(p.Name),
			Scheme:    scheme,
			Host:      strings.TrimSpace(p.Host),
			Port:      p.Port,
			DNSDomain: strings.TrimSpace(p.DNSDomain),
			ICMPAddr:  strings.TrimSpace(p.ICMPAddr),
			Notes:     p.Notes,
			Enabled:   true,
			Status:    "running",
		}
		// AI-created listeners belong to the principal's tenant when the
		// caller is an authenticated operator; system paths stay legacy 0.
		if reqCtx != nil && reqCtx.Principal.UserID != 0 {
			l.TenantID = reqCtx.Principal.TenantID
		}
		if l.Name == "" {
			l.Name = fmt.Sprintf("Listener %d", l.Port)
		}
		if l.Host == "" && scheme != "dns" && scheme != "icmp" {
			return `{"error":"host required (e.g. 0.0.0.0)"}`
		}
		if scheme == "dns" && l.DNSDomain == "" {
			return `{"error":"dns_domain required for dns listeners"}`
		}
		if l.Port != 0 && (l.Port < 1 || l.Port > 65535) {
			return `{"error":"port must be between 1 and 65535"}`
		}
		if l.Host != "" && !isValidHost(l.Host) {
			return `{"error":"invalid host address"}`
		}
		normalizeListenerProtocol(&l)
		if err := s.db.Create(&l).Error; err != nil {
			b, _ := marshalJSONSafe(map[string]interface{}{"error": sanitizeError(err, "Listener create")})
			return string(b)
		}
		bound := s.startListenerForRecord(&l, "ai-created")
		if !bound {
			s.db.Model(&l).Update("status", "stopped")
		}
		s.syncListenerProbe(&l)
		s.broadcastListenerUpdate("created", &l)
		bindStatus := "running"
		if !bound {
			bindStatus = "stopped"
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"id": l.ID, "name": l.Name, "scheme": l.Scheme,
			"host": l.Host, "port": l.Port,
			"bind_status": bindStatus,
			"note":        "bind_status=stopped means the port could not be bound; check conflicts on the Listeners page.",
		})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "update_listener":
		if !s.aiExecutionEnabled() {
			return `{"error":"allow_execute is disabled in AI config; enable it to manage listeners"}`
		}
		var p struct {
			ListenerID uint   `json:"listener_id"`
			Enabled    *bool  `json:"enabled"`
			Name       string `json:"name"`
			Host       string `json:"host"`
			Port       int    `json:"port"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.ListenerID == 0 {
			if v, err := strconv.ParseUint(args["listener_id"], 10, 64); err == nil {
				p.ListenerID = uint(v)
			}
		}
		if p.ListenerID == 0 {
			return `{"error":"numeric listener_id required"}`
		}
		var l db.Listener
		if err := s.aiTenantScope(s.db, reqCtx).First(&l, p.ListenerID).Error; err != nil {
			return `{"error":"listener not found"}`
		}
		bindChanged := false
		oldKey := listenerKey(&l)
		if p.Name != "" {
			l.Name = strings.TrimSpace(p.Name)
		}
		if p.Host != "" {
			h := strings.TrimSpace(p.Host)
			if !isValidHost(h) {
				return `{"error":"invalid host address"}`
			}
			l.Host = h
			bindChanged = true
		}
		if p.Port != 0 {
			if p.Port < 1 || p.Port > 65535 {
				return `{"error":"port must be between 1 and 65535"}`
			}
			l.Port = p.Port
			bindChanged = true
		}
		if p.Enabled != nil {
			l.Enabled = *p.Enabled
		}
		if err := s.db.Save(&l).Error; err != nil {
			return `{"error":"failed to save listener"}`
		}
		if bindChanged {
			s.stopExtraListener(oldKey)
		}
		status := l.Status
		if l.Enabled {
			if s.startListenerForRecord(&l, "ai-updated") {
				status = "running"
			} else {
				status = "stopped"
			}
			s.db.Model(&l).Update("status", status)
		} else if p.Enabled != nil && !*p.Enabled {
			s.stopExtraListener(oldKey)
			status = "stopped"
			s.db.Model(&l).Update("status", status)
		}
		s.syncListenerProbe(&l)
		s.broadcastListenerUpdate("updated", &l)
		b, ok := marshalJSONSafe(map[string]interface{}{
			"id": l.ID, "name": l.Name, "enabled": l.Enabled,
			"host": l.Host, "port": l.Port, "bind_status": status,
		})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "delete_listener":
		if !s.aiExecutionEnabled() {
			return `{"error":"allow_execute is disabled in AI config; enable it to manage listeners"}`
		}
		var p struct {
			ListenerID uint `json:"listener_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.ListenerID == 0 {
			if v, err := strconv.ParseUint(args["listener_id"], 10, 64); err == nil {
				p.ListenerID = uint(v)
			}
		}
		if p.ListenerID == 0 {
			return `{"error":"numeric listener_id required"}`
		}
		var agentCount int64
		s.aiTenantScope(s.db.Model(&db.Implant{}), reqCtx).Where("listener_id = ?", p.ListenerID).Count(&agentCount)
		if agentCount > 0 {
			b, _ := marshalJSONSafe(map[string]interface{}{
				"error":       fmt.Sprintf("cannot delete: %d agents still reference this listener", agentCount),
				"agent_count": agentCount,
			})
			return string(b)
		}
		var l db.Listener
		if err := s.aiTenantScope(s.db, reqCtx).First(&l, p.ListenerID).Error; err == nil {
			s.stopExtraListener(listenerKey(&l))
			if s.circuitBreaker != nil {
				s.circuitBreaker.UnregisterTarget(listenerTargetID(&l))
			}
		} else {
			return `{"error":"listener not found"}`
		}
		if err := s.aiTenantScope(s.db, reqCtx).Delete(&db.Listener{}, p.ListenerID).Error; err != nil {
			return `{"error":"failed to delete listener"}`
		}
		s.broadcastListenerUpdate("deleted", &l)
		b, ok := marshalJSONSafe(map[string]interface{}{"deleted": true, "id": l.ID, "name": l.Name})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "get_situation":
		snap := s.collectSituation(reqCtx)
		b, ok := marshalJSONSafe(snap)
		if !ok {
			return `{"error":"failed to marshal situation"}`
		}
		return string(b)

	case "get_alerts":
		var p struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.Limit <= 0 || p.Limit > 50 {
			p.Limit = 15
		}
		status := strings.ToLower(strings.TrimSpace(p.Status))
		if status == "" {
			status = "active"
		}
		q := s.db.Model(&db.Alert{})
		if status != "all" {
			q = q.Where("status = ?", status)
		}
		var alerts []db.Alert
		if err := q.Order("created_at desc").Limit(p.Limit).Find(&alerts).Error; err != nil {
			return `{"error":"failed to list alerts"}`
		}
		out := []map[string]interface{}{}
		for _, a := range alerts {
			out = append(out, map[string]interface{}{
				"id": a.ID, "type": a.Type, "severity": a.Severity,
				"title": a.Title, "message": truncateStr(a.Message, 240),
				"source": a.Source, "source_name": a.SourceName,
				"status": a.Status, "created_at": a.CreatedAt.Format(time.RFC3339),
			})
		}
		b, ok := marshalJSONSafe(map[string]interface{}{"count": len(out), "alerts": out})
		if !ok {
			return `{"error":"failed to marshal alerts"}`
		}
		return string(b)

	case "set_sleep":
		var p struct {
			AgentID  string `json:"agent_id"`
			Interval int    `json:"interval"`
			Jitter   int    `json:"jitter"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.AgentID == "" {
			p.AgentID = args["agent_id"]
		}
		aid := s.resolveAIAgentID(reqCtx, p.AgentID)
		if aid == "" {
			return `{"error":"agent not found"}`
		}
		if p.Interval < 1 || p.Interval > 86400 {
			return `{"error":"interval must be 1-86400 seconds"}`
		}
		if p.Jitter < 0 || p.Jitter > 100 {
			return `{"error":"jitter must be 0-100"}`
		}
		// OPSEC floors apply to AI-driven sleep changes like any other entry.
		p.Interval, p.Jitter, _ = s.clampSleepInts(p.Interval, p.Jitter)
		allowExec := s.aiExecutionEnabled()
		status := TaskStatusPendingApproval
		if allowExec && s.resolveInitialTaskStatus("set_sleep") == "pending" {
			status = "pending"
		}
		cmd := fmt.Sprintf("%d,%d", p.Interval, p.Jitter)
		task, err := s.createSystemTask(aid, "set_sleep", cmd, "", status, "ai")
		if err != nil {
			b, _ := marshalJSONSafe(map[string]interface{}{"error": sanitizeError(err, "task")})
			return string(b)
		}
		if status == "pending" {
			s.broadcastTaskUpdate(aid, *task)
		}
		msg := "Sleep change queued."
		if status == TaskStatusPendingApproval {
			msg = "Sleep change requires operator approval."
		}
		b, ok := marshalJSONSafe(map[string]interface{}{
			"task_id": task.ID, "status": status, "interval": p.Interval, "jitter": p.Jitter, "message": msg,
		})
		if !ok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	case "queue_collection":
		var p struct {
			AgentID string `json:"agent_id"`
			Action  string `json:"action"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &p)
		if p.AgentID == "" {
			p.AgentID = args["agent_id"]
		}
		if p.Action == "" {
			p.Action = args["action"]
		}
		aid := s.resolveAIAgentID(reqCtx, p.AgentID)
		if aid == "" {
			return `{"error":"agent not found"}`
		}
		taskType, ok := aiCollectionTypes[strings.ToLower(strings.TrimSpace(p.Action))]
		if !ok {
			return `{"error":"action must be screenshot, ps, netstat, av, users, drives, services, or beacon_now"}`
		}
		allowExec := s.aiExecutionEnabled()
		status := TaskStatusPendingApproval
		if allowExec && s.resolveInitialTaskStatus(taskType) == "pending" {
			status = "pending"
		}
		task, err := s.createSystemTask(aid, taskType, "", "", status, "ai")
		if err != nil {
			b, _ := marshalJSONSafe(map[string]interface{}{"error": sanitizeError(err, "task")})
			return string(b)
		}
		if status == "pending" {
			s.broadcastTaskUpdate(aid, *task)
		}
		msg := "Collection task queued."
		if status == TaskStatusPendingApproval {
			msg = "Collection task requires operator approval."
		}
		b, mok := marshalJSONSafe(map[string]interface{}{
			"task_id": task.ID, "status": status, "action": taskType, "message": msg,
		})
		if !mok {
			return `{"error":"failed to marshal result"}`
		}
		return string(b)

	default:
		return `{"error":"unknown tool"}`
	}
}
