package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// ── REST API: Agents ──

func (s *Server) apiListAgents(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	s.writeAgentListJSON(c)
}

// computeTaskStats returns task status counts per agent ID using a single GROUP BY query.
func computeTaskStats(database *gorm.DB, ids []string) map[string]*db.TaskStats {
	if len(ids) == 0 {
		return nil
	}
	type row struct {
		AgentID string
		Status  string
		Count   int
	}
	var rows []row
	database.Raw(
		"SELECT agent_id, status, COUNT(*) as count FROM tasks WHERE agent_id IN ? GROUP BY agent_id, status",
		ids,
	).Scan(&rows)

	out := make(map[string]*db.TaskStats, len(ids))
	for _, r := range rows {
		ts, ok := out[r.AgentID]
		if !ok {
			ts = &db.TaskStats{}
			out[r.AgentID] = ts
		}
		switch r.Status {
		case "pending":
			ts.Pending = r.Count
		case "running":
			ts.Running = r.Count
		case "completed":
			ts.Completed = r.Count
		case "failed":
			ts.Failed = r.Count
		}
	}
	return out
}

func (s *Server) apiGetAgent(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var agent db.Implant
	if err := s.tenantScope(s.db, c).First(&agent, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	respond(c, agent)
}

func (s *Server) apiDeleteAgent(c *gin.Context) {
	role, _ := c.Get("user_role")
	if role != "admin" {
		respondError(c, http.StatusForbidden, "admin required")
		return
	}

	id := c.Param("id")
	var visible int64
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("id = ?", id).Count(&visible).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "delete failed")
		return
	}
	if visible == 0 {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	// Cascade-delete all dependent rows (tasks, credentials, tokens, …) —
	// a bare implant delete would leave orphans behind.
	if !s.deleteAgentRecord(id) {
		respondError(c, http.StatusInternalServerError, "delete failed")
		return
	}
	s.LogAuditRecord(c, "delete_agent", "agent", id, "Agent deleted", true, nil)
	respond(c, gin.H{"message": "agent deleted"})
}

// ── REST API: Tasks ──

func (s *Server) apiListTasks(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var tasks []db.Task
	query := s.tenantScope(s.db, c).Order("created_at desc")

	if agentID := c.Query("agent_id"); agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	if taskType := c.Query("type"); taskType != "" {
		query = query.Where("type = ?", taskType)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	limit := APITaskListLimit
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n < limit {
			limit = n
		}
	}

	if err := query.Limit(limit).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database error")
		return
	}
	// Documented REST v1 list shape: {"success": true, "data": [...], "total": n}
	// (api/openapi.yaml). The sibling total is part of the published scripting
	// contract, so these list endpoints stay off respond()'s payload wrapping;
	// single-object endpoints above use respond() and are byte-identical.
	c.JSON(http.StatusOK, gin.H{"success": true, "data": tasks, "total": len(tasks)})
}

func (s *Server) apiGetTask(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	// Strict numeric parse: a raw string here is both an injection vector and
	// a tenant-isolation gap (same fix as handleGetTaskStatus).
	taskID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid task id")
		return
	}
	var task db.Task
	query := s.tenantScope(s.db, c)
	if err := query.First(&task, taskID).Error; err != nil {
		// Legacy fallback for pre-tenant tenant-0 rows (see
		// handleGetTaskStatus): re-gate on agent visibility.
		if ferr := s.db.First(&task, taskID).Error; ferr != nil {
			respondError(c, http.StatusNotFound, "task not found")
			return
		}
		var vis db.Implant
		if verr := s.tenantScope(s.db.Select("id"), c).Where("id = ?", task.AgentID).First(&vis).Error; verr != nil {
			respondError(c, http.StatusNotFound, "task not found")
			return
		}
	}
	respond(c, task)
}

func (s *Server) apiCreateTask(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		AgentID       string `json:"agent_id" binding:"required"`
		Type          string `json:"type" binding:"required"`
		Command       string `json:"command"`
		Shell         string `json:"shell"`
		Path          string `json:"path"`
		Data          string `json:"data"`
		EncryptResult bool   `json:"encrypt_result"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	if !IsKnownTaskType(req.Type) && !protocol.ValidTaskType(req.Type) {
		respondError(c, http.StatusBadRequest, "unknown task type: "+req.Type)
		return
	}

	// Path/Data are optional but required by some types (rename/chmod need
	// data). A missing mandatory parameter is a client error: answer 400 with
	// the parameter name instead of an opaque 500.
	task, err := s.createTask(req.AgentID, req.Type, req.Command, req.Shell, req.Path, req.Data, 0, 0)
	if err != nil {
		var reqErr *taskRequestError
		if errors.As(err, &reqErr) {
			respondError(c, http.StatusBadRequest, reqErr.Error())
			return
		}
		slog.Error("Task creation failed", "agent_id", req.AgentID, "type", req.Type, "err", err)
		respondError(c, http.StatusInternalServerError, "task creation failed")
		return
	}

	// Per-task result encryption (P2): mint a per-task AES-256-GCM key, store it
	// on the task row, and the agent will seal this task's result with it.
	if req.EncryptResult {
		if k, kerr := generateTaskKey(); kerr == nil {
			if uerr := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Update("task_key", k).Error; uerr == nil {
				task.TaskKey = k
			}
		}
	}

	s.broadcastTaskUpdate(req.AgentID, *task)
	slog.Info("Task created via API", "task_id", task.ID, "agent_id", task.AgentID, "type", task.Type)
	respond(c, task)
}

// ── REST API: Credentials ──

func (s *Server) apiListCredentials(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var creds []db.CredentialEntry
	query := s.tenantScope(s.db.Order("created_at desc"), c)

	if agentID := c.Query("agent_id"); agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	if credType := c.Query("type"); credType != "" {
		query = query.Where("type = ?", credType)
	}
	if domain := c.Query("domain"); domain != "" {
		query = query.Where("domain LIKE ? ESCAPE '\\'", "%"+escapeLike(domain)+"%")
	}

	if err := query.Limit(APICredentialListLimit).Find(&creds).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": creds, "total": len(creds)})
}

// ── REST API: Listeners ──

func (s *Server) apiListListeners(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	s.handleListListeners(c)
}

// ── REST API: Dashboard Stats ──

func (s *Server) apiDashboardStats(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	offlineCutoff := time.Now().Add(-s.offlineThreshold())
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	staleCutoff := now.Add(-s.staleThreshold())

	type agentCounts struct {
		Total   int64
		Online  int64
		Stale   int64
		Offline int64
	}
	type taskCounts struct {
		Total     int64
		Today     int64
		Pending   int64
		Completed int64
		Failed    int64
	}

	g, ctx := errgroup.WithContext(c.Request.Context())
	tid := s.currentTenantID(c)

	var ac agentCounts
	g.Go(func() error {
		if tid != 0 {
			return s.db.WithContext(ctx).Raw(`
				SELECT
					COUNT(*) as total,
					COALESCE(SUM(CASE WHEN last_seen > ? THEN 1 ELSE 0 END), 0) as online,
					COALESCE(SUM(CASE WHEN last_seen > ? AND last_seen <= ? THEN 1 ELSE 0 END), 0) as stale,
					COALESCE(SUM(CASE WHEN last_seen <= ? THEN 1 ELSE 0 END), 0) as offline
				FROM implants WHERE deleted_at IS NULL AND tenant_id = ?`, offlineCutoff, offlineCutoff, staleCutoff, offlineCutoff, tid,
			).Scan(&ac).Error
		}
		return s.db.WithContext(ctx).Raw(`
			SELECT
				COUNT(*) as total,
				COALESCE(SUM(CASE WHEN last_seen > ? THEN 1 ELSE 0 END), 0) as online,
				COALESCE(SUM(CASE WHEN last_seen > ? AND last_seen <= ? THEN 1 ELSE 0 END), 0) as stale,
				COALESCE(SUM(CASE WHEN last_seen <= ? THEN 1 ELSE 0 END), 0) as offline
			FROM implants WHERE deleted_at IS NULL`, offlineCutoff, offlineCutoff, staleCutoff, offlineCutoff,
		).Scan(&ac).Error
	})

	var tc taskCounts
	g.Go(func() error {
		if tid != 0 {
			return s.db.WithContext(ctx).Raw(`
				SELECT
					COUNT(*) as total,
					COALESCE(SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END), 0) as today,
					COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) as pending,
					COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
					COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed
				FROM tasks WHERE tenant_id = ?`, todayStart, tid,
			).Scan(&tc).Error
		}
		return s.db.WithContext(ctx).Raw(`
			SELECT
				COUNT(*) as total,
				COALESCE(SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END), 0) as today,
				COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) as pending,
				COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
				COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed
			FROM tasks`, todayStart,
		).Scan(&tc).Error
	})

	// Only implants and tasks have a tenant_id column. Credentials and tokens
	// inherit ownership from their agent, listeners are deployment-global, and
	// audit rows inherit ownership from their operator. Applying tenantScope to
	// those tables directly generates invalid SQL ("no such column: tenant_id")
	// and makes the whole dashboard fail with HTTP 500 for tenant-bound users.
	countAgentOwnedRows := func(model any, total *int64) error {
		query := s.db.WithContext(ctx).Model(model)
		if tid != 0 {
			ownedAgentIDs := s.db.WithContext(ctx).Model(&db.Implant{}).
				Select("id").Where("tenant_id = ?", tid)
			query = query.Where("agent_id IN (?)", ownedAgentIDs)
		}
		return query.Count(total).Error
	}

	var totalCreds, totalTokens, totalListeners, totalAudits int64
	g.Go(func() error { return countAgentOwnedRows(&db.CredentialEntry{}, &totalCreds) })
	g.Go(func() error { return countAgentOwnedRows(&db.TokenEntry{}, &totalTokens) })
	g.Go(func() error {
		return s.db.WithContext(ctx).Model(&db.Listener{}).Count(&totalListeners).Error
	})
	g.Go(func() error {
		return s.auditTenantScope(s.db.WithContext(ctx).Model(&db.AuditLog{}), c).Count(&totalAudits).Error
	})
	var onlineUsersList []UserSession
	g.Go(func() error {
		onlineUsersList = s.getOnlineUsers()
		return nil
	})
	if err := g.Wait(); err != nil {
		handleQueryError(c, err, "API: failed to count dashboard stats")
		return
	}

	if tid != 0 && len(onlineUsersList) > 0 {
		usernames := make([]string, 0, len(onlineUsersList))
		for _, session := range onlineUsersList {
			usernames = append(usernames, session.Username)
		}
		var tenantUsernames []string
		if err := s.db.WithContext(c.Request.Context()).Model(&db.User{}).
			Where("tenant_id = ? AND username IN ?", tid, usernames).
			Pluck("username", &tenantUsernames).Error; err != nil {
			handleQueryError(c, err, "API: failed to scope online users")
			return
		}
		allowed := make(map[string]struct{}, len(tenantUsernames))
		for _, username := range tenantUsernames {
			allowed[username] = struct{}{}
		}
		filtered := onlineUsersList[:0]
		for _, session := range onlineUsersList {
			if _, ok := allowed[session.Username]; ok {
				filtered = append(filtered, session)
			}
		}
		onlineUsersList = filtered
	}
	onlineUsers := int64(len(onlineUsersList))

	var recentTasks []db.Task
	recentTasksQuery := s.db.WithContext(c.Request.Context())
	if tid != 0 {
		recentTasksQuery = recentTasksQuery.Where("tenant_id = ?", tid)
	}
	if err := recentTasksQuery.Where("type NOT IN ?", []string{"screen_stream_start", "screen_stream_stop", "ls"}).
		Order("created_at desc").Limit(DashboardRecentTasks).Find(&recentTasks).Error; err != nil {
		handleQueryError(c, err, "API: failed to query recent tasks")
		return
	}
	if len(recentTasks) > 0 {
		agentIDs := make([]string, 0, len(recentTasks))
		for _, t := range recentTasks {
			if t.AgentID != "" {
				agentIDs = append(agentIDs, t.AgentID)
			}
		}
		if len(agentIDs) > 0 {
			var agents []db.Implant
			agentsQuery := s.db.WithContext(c.Request.Context()).Where("id IN ?", agentIDs)
			if tid != 0 {
				agentsQuery = agentsQuery.Where("tenant_id = ?", tid)
			}
			if err := agentsQuery.Find(&agents).Error; err != nil {
				handleQueryError(c, err, "API: failed to query agents for recent tasks")
				return
			}
			agentMap := make(map[string]db.Implant, len(agents))
			for _, a := range agents {
				agentMap[a.ID] = a
			}
			for i := range recentTasks {
				if a, ok := agentMap[recentTasks[i].AgentID]; ok {
					recentTasks[i].Agent = a
				}
			}
		}
	}

	// Protocol summary: which wire protocol the server speaks, how wide the
	// compatibility window is, and how many deployed agents still speak a
	// version the server would refuse. An agent on an outdated version never
	// shows up in the agent list as "offline" without a reason - this count
	// is where the operator sees the fleet-wide picture.
	var outdatedAgents int64
	outdatedQuery := s.db.WithContext(c.Request.Context()).Model(&db.Implant{}).
		Where("protocol_version > 0 AND protocol_version <> ?", protocol.CurrentProtocolVersion)
	if tid != 0 {
		outdatedQuery = outdatedQuery.Where("tenant_id = ?", tid)
	}
	if err := outdatedQuery.Count(&outdatedAgents).Error; err != nil {
		handleQueryError(c, err, "API: failed to count agents on an outdated protocol")
		return
	}

	stats := gin.H{
		"total_agents":             ac.Total,
		"online_agents":            ac.Online,
		"today_tasks":              tc.Today,
		"pending_tasks":            tc.Pending,
		"failed_tasks":             tc.Failed,
		"total_tasks":              tc.Total,
		"total_creds":              totalCreds,
		"total_tokens":             totalTokens,
		"total_listeners":          totalListeners,
		"total_audits":             totalAudits,
		"online_count":             ac.Online,
		"stale_count":              ac.Stale,
		"offline_count":            ac.Offline,
		"listener_count":           totalListeners,
		"pending_count":            tc.Pending,
		"online_users":             onlineUsers,
		"completed_tasks":          tc.Completed,
		"server_version":           ServerVersion,
		"protocol_version":         protocol.CurrentProtocolVersion,
		"protocol_compat_window":   s.protocolCompatWindow(),
		"protocol_outdated_agents": outdatedAgents,
		"protocol":                 s.protocolState(),
		"recent_tasks":             recentTasks,
	}

	respond(c, stats)
}

// ── REST API: Audit Logs ──

func (s *Server) apiListAuditLogs(c *gin.Context) {
	role, _ := c.Get("user_role")
	if role != "admin" {
		respondError(c, http.StatusForbidden, "admin required")
		return
	}
	var logs []db.AuditLog
	query := s.auditTenantScope(s.db, c).Order("created_at desc")

	if action := c.Query("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if user := c.Query("user"); user != "" {
		query = query.Where("user LIKE ? ESCAPE '\\'", "%"+escapeLike(user)+"%")
	}

	if err := query.Limit(APIAuditLogListLimit).Find(&logs).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "database error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": logs, "total": len(logs)})
}

// ── REST API: Bulk Results ──

func (s *Server) apiBulkResults(c *gin.Context) {
	role, _ := c.Get("user_role")
	roleStr, _ := role.(string)
	if roleStr != "admin" && !db.RoleHasPermission(roleStr, db.PermTasksRead) {
		respondError(c, http.StatusForbidden, "insufficient permissions")
		return
	}

	results := s.bulkHistoryFor(c)
	c.JSON(http.StatusOK, gin.H{"success": true, "results": results, "total": len(results)})
}

// ── REST API: Health ──

func (s *Server) apiHealth(c *gin.Context) {
	health := gin.H{
		"status":    "ok",
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}

	// Check database. Keep the API probe available during partial startup so
	// orchestrators can inspect the degraded response instead of seeing a panic.
	if s == nil || s.db == nil {
		health["status"] = "degraded"
		health["database"] = "unavailable"
	} else if sqlDB, err := s.db.DB(); err == nil {
		if err := sqlDB.Ping(); err != nil {
			health["status"] = "degraded"
			health["database"] = "unreachable"
		} else {
			health["database"] = "connected"
		}
	}

	// Audit chain integrity, cached: the console polls this endpoint, and a full
	// verification walks rows. /ready stays uncached (it must notice tampering
	// promptly); the UI view tolerates a minute of staleness.
	if s != nil && s.db != nil {
		verdict := s.cachedAuditChainVerdict(auditHealthCacheTTL)
		health["audit_chain"] = gin.H{
			"ok":           verdict.OK,
			"checked":      verdict.Checked,
			"first_bad_id": verdict.FirstBadID,
			"reason":       verdict.Reason,
		}
	}

	// Backup freshness: an operator (or monitor) should be able to see that the
	// last snapshot is stale without shelling into the box.
	if s != nil && s.backupManager != nil {
		bh := s.backupManager.Health()
		health["backup"] = gin.H{
			"retain":       bh.Retain,
			"age_seconds":  bh.AgeSeconds,
			"last_success": bh.LastSuccess,
			"last_failure": bh.LastFailure,
			"last_error":   bh.LastError,
		}
	} else {
		health["backup"] = gin.H{"enabled": false}
	}

	// Audit backlog: the async queue is bounded and non-blocking; operators can
	// alert on depth before entries start being dropped.
	if s != nil {
		health["audit_queue_depth"] = s.auditQueueDepth()
	} else {
		health["audit_queue_depth"] = 0
	}

	respond(c, health)
}

// registerAPIRoutes registers all REST API routes under the given group
func (s *Server) registerAPIRoutes(api *gin.RouterGroup) {
	api.GET("/health", s.apiHealth)

	agentsRead := api.Group("/")
	agentsRead.Use(middleware.RequirePermission(db.PermAgentsRead))
	{
		agentsRead.GET("/dashboard", s.apiDashboardStats)
		agentsRead.GET("/agents", s.apiListAgents)
		agentsRead.GET("/agents/:id", s.apiGetAgent)
	}

	agentsWrite := api.Group("/")
	agentsWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		agentsWrite.POST("/tasks", s.apiCreateTask)
	}

	agentsDelete := api.Group("/")
	agentsDelete.Use(middleware.RequirePermission(db.PermAgentsDelete))
	{
		agentsDelete.DELETE("/agents/:id", s.apiDeleteAgent)
	}

	tasksRead := api.Group("/")
	tasksRead.Use(middleware.RequirePermission(db.PermTasksRead))
	{
		tasksRead.GET("/tasks", s.apiListTasks)
		tasksRead.GET("/tasks/:id", s.apiGetTask)
		tasksRead.GET("/task-types", s.apiListTaskTypes)
		tasksRead.POST("/tasks/status", s.apiBulkTaskStatus)
	}

	credsRead := api.Group("/")
	credsRead.Use(middleware.RequirePermission(db.PermCredsRead))
	{
		credsRead.GET("/credentials", s.apiListCredentials)
	}

	listenersRead := api.Group("/")
	listenersRead.Use(middleware.RequirePermission(db.PermListenersRead))
	{
		listenersRead.GET("/listeners", s.apiListListeners)
	}

	// These handlers enforce their own checks (admin / PermTasksRead).
	api.GET("/audit", s.apiListAuditLogs)
	api.GET("/bulk/results", s.apiBulkResults)
}
