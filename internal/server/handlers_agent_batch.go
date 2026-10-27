package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) handleBulkDeleteAgents(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.AgentIDs) == 0 {
		respondError(c, http.StatusBadRequest, "agent_ids required")
		return
	}
	if len(req.AgentIDs) > MaxBatchAgentLimit {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("too many agents (max %d)", MaxBatchAgentLimit))
		return
	}

	// Tenant gate (fail-closed BEFORE the transaction): any requested id
	// outside the caller's tenant aborts the whole batch so a foreign UUID
	// can neither be probed nor deleted. Single-delete atomicity below is
	// preserved — nothing is touched on this path.
	var visible []db.Implant
	if err := s.tenantScope(s.db, c).Select("id").Where("id IN ?", req.AgentIDs).Find(&visible).Error; err != nil {
		handleQueryError(c, err, "Failed to query agents for bulk delete")
		return
	}
	if len(visible) != len(req.AgentIDs) {
		seen := make(map[string]bool, len(visible))
		for _, a := range visible {
			seen[a.ID] = true
		}
		for _, id := range req.AgentIDs {
			if !seen[id] {
				respondError(c, http.StatusNotFound, "agent not found")
				return
			}
		}
	}

	// Whole batch is deleted in a single transaction: any failure rolls back
	// every agent so the operation never leaves a partially-deleted state.
	deleted := 0
	var deleteAudit []auditEntry
	tx := s.db.Begin()
	for _, id := range req.AgentIDs {
		if err := s.deleteAgentRecordTx(tx, id); err != nil {
			tx.Rollback()
			slog.Error("Bulk delete: failed to delete agent, rolling back batch", "agent_id", id, "err", err)
			respondError(c, http.StatusInternalServerError, "failed to delete agents")
			return
		}
		deleted++
		deleteAudit = append(deleteAudit, auditEntry{action: "delete_agent", resource: "agent", agentID: id, details: "bulk delete", success: true})
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		slog.Error("Bulk delete: commit failed, rolling back entire batch", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to delete agents")
		return
	}
	s.LogAuditRecords(c, deleteAudit)

	user, _ := c.Get("user")
	operator := fmt.Sprintf("%v", user)
	s.broadcastBulkAgentDeleteAlert(operator, deleted)
	s.LogAuditRecord(c, "batch_delete_agents", "agent", "", fmt.Sprintf("deleted %d agents", deleted), true, nil)
	slog.Warn("Bulk agent delete", "deleted", deleted, "user", operator)

	respond(c, gin.H{

		"deleted": deleted,
		"failed":  0,
	})
}

func (s *Server) handleBatchCommand(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}

	var req struct {
		AgentIDs []string `json:"agent_ids"`
		Command  string   `json:"command"`
		Shell    string   `json:"shell"`
		TaskType string   `json:"task_type"`
		Type     string   `json:"type"`
		File     string   `json:"file"`
		Args     string   `json:"args"`
		// IdempotencyKey deduplicates retried batches: an agent already
		// holding a live task with the key is skipped (same semantics as
		// the single-task WithIdempotencyKey path).
		IdempotencyKey string `json:"idempotency_key"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid json")
		return
	}

	if len(req.AgentIDs) == 0 {
		respondError(c, http.StatusBadRequest, "no agents selected")
		return
	}
	if len(req.AgentIDs) > MaxBatchAgentLimit {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("too many agents (max %d)", MaxBatchAgentLimit))
		return
	}

	if req.TaskType == "" {
		req.TaskType = req.Type
	}

	if req.TaskType == "" {
		req.TaskType = "shell"
	}

	// Operator identity for audit attribution on every batched task.
	user, _ := c.Get("user")
	operator, _ := user.(string)
	// Same caller id the single-task path uses, so the operator soft-lock
	// (agent-conflict guard) applies to bulk operations too (P2-4).
	var callerUID uint
	if uid, exists := c.Get("user_id"); exists {
		if u, ok := uid.(uint); ok {
			callerUID = u
		}
	}

	uniqueIDs := make([]string, 0, len(req.AgentIDs))
	seen := make(map[string]struct{}, len(req.AgentIDs))
	for _, id := range req.AgentIDs {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			uniqueIDs = append(uniqueIDs, id)
		}
	}

	var existingAgents []db.Implant
	// Tenant gate: foreign-tenant ids simply miss the visible set and fall
	// into failedCount below — no task is ever queued on another tenant's
	// agent. The RoE columns ride along so the RoE gate can check the row
	// in memory instead of re-SELECTing every agent (finding: up to 500
	// point queries serialized on the single-writer pool).
	if err := s.tenantScope(s.db, c).Select("id", "tenant_id", "public_ip", "ip").Where("id IN ?", uniqueIDs).Find(&existingAgents).Error; err != nil {
		handleQueryError(c, err, "Failed to query existing agents for batch")
		return
	}
	existingSet := make(map[string]bool, len(existingAgents))
	tenantByAgent := make(map[string]uint, len(existingAgents))
	agentsByID := make(map[string]db.Implant, len(existingAgents))
	for _, a := range existingAgents {
		existingSet[a.ID] = true
		tenantByAgent[a.ID] = a.TenantID
		agentsByID[a.ID] = a
	}

	// Build all tasks first, then batch-insert in a single DB call.
	// Apply the same gates as createTask (command length + per-agent pending
	// depth). The single-task path rejects new tasks once an agent has
	// MaxPendingTasksPerAgent outstanding; the bulk path must not silently
	// bypass that ceiling by inserting straight into the DB.
	if len(req.Command) > MaxCommandLength {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("command too long (max %d characters)", MaxCommandLength))
		return
	}

	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if len(idempotencyKey) > 64 {
		respondError(c, http.StatusBadRequest, "idempotency key too long (max 64 characters)")
		return
	}

	// Bulk set_sleep commands pass through the same OPSEC floors as every
	// other entry point (clamped, never rejected, so older consoles work).
	command := req.Command
	if req.TaskType == "set_sleep" {
		if clamped, changed := func() (string, bool) {
			minInterval, minJitter := s.sleepMinimums()
			out := clampSleepString(req.Command, minInterval, minJitter)
			return out, out != req.Command
		}(); changed {
			s.LogAuditRecord(c, "batch_sleep_clamped", "agent", "", "bulk set_sleep raised to OPSEC floors", true, nil)
			command = clamped
		}
	}

	// Collaboration locks: agents locked by another operator are skipped
	// (one query for the whole batch). Locked skips are reported separately
	// from validation failures so the operator knows to coordinate, unlock,
	// or force via single-task endpoints.
	lockedByOther := s.batchLockedByOther(uniqueIDs, operator)

	// Idempotency pre-check (one query for the whole batch): agents already
	// holding a live task with the key are skipped so a retried batch is a
	// no-op per agent instead of duplicating work on every agent.
	liveKeyHolders := make(map[string]bool)
	if idempotencyKey != "" {
		var live []db.Task
		if err := s.db.Select("agent_id").Where("agent_id IN ? AND idempotency_key = ? AND status IN ?",
			uniqueIDs, idempotencyKey, []string{"pending", TaskStatusPendingApproval, "running"}).
			Find(&live).Error; err != nil {
			slog.Error("Batch command: idempotency lookup failed", "err", err)
			respondError(c, http.StatusInternalServerError, "failed to check idempotency")
			return
		}
		for _, t := range live {
			liveKeyHolders[t.AgentID] = true
		}
	}

	tasks := make([]db.Task, 0, len(uniqueIDs))

	// Phase 1 (NO lock): per-agent validation is now I/O-free for agents in
	// the batch (the RoE gate reuses the agent load above). Doing it under
	// agentPendingTasksMu — the same mutex every beacon result ingestion and
	// createTask acquires — stalled the whole task pipeline for up to
	// MaxBatchAgentLimit sequential queries.
	type batchCandidate struct {
		agentID string
	}
	candidates := make([]batchCandidate, 0, len(uniqueIDs))
	lockedSkipped := 0
	dedupSkipped := 0
	for _, agentID := range uniqueIDs {
		if !existingSet[agentID] {
			continue
		}

		// Idempotent retry: this agent already holds the live key.
		if liveKeyHolders[agentID] {
			slog.Info("Batch command: live idempotency key, skipping", "agent_id", agentID)
			dedupSkipped++
			continue
		}

		// Validate task type once per agent
		if !IsKnownTaskType(req.TaskType) && !protocol.ValidTaskType(req.TaskType) {
			slog.Error("Batch command: unknown task type", "type", req.TaskType)
			continue
		}

		// Validation reuses the batch's own agent load for the RoE gate —
		// no per-agent point SELECTs under the single-writer pool.
		preloaded := agentsByID[agentID]
		if err := s.validateTaskCreationWithAgent(c, agentID, req.TaskType, command, req.Args, req.File, callerUID, &preloaded); err != nil {
			slog.Warn("Batch command: validation failed", "agent_id", agentID, "err", err)
			continue
		}

		// Collaboration locks apply to bulk writes too: skip agents locked
		// by another operator (reported separately below) instead of
		// stomping coordinated work. No force override on bulk — use the
		// single-task endpoint with ?force for that.
		if holder, ok := lockedByOther[agentID]; ok {
			slog.Info("Batch command: agent locked, skipping", "agent_id", agentID, "locked_by", holder)
			lockedSkipped++
			continue
		}

		candidates = append(candidates, batchCandidate{agentID: agentID})
	}

	// Phase 2 (locked): counter accounting only — pure memory operations.
	s.agentPendingTasksMu.Lock()
	for _, cand := range candidates {
		if s.agentPendingTasks[cand.agentID] >= MaxPendingTasksPerAgent {
			slog.Warn("Batch command: agent pending queue full, skipping",
				"agent_id", cand.agentID, "pending", s.agentPendingTasks[cand.agentID], "limit", MaxPendingTasksPerAgent)
			continue
		}
		s.agentPendingTasks[cand.agentID]++

		status := s.resolveInitialTaskStatus(req.TaskType)
		row := db.Task{
			AgentID:        cand.agentID,
			Type:           req.TaskType,
			Command:        command,
			Shell:          req.Shell,
			Path:           req.File,
			Data:           req.Args,
			Status:         status,
			CreatedBy:      operator,
			IdempotencyKey: idempotencyKey,
			// Same tenant inheritance as createTask, or tenant-scoped
			// single-task reads 404 on batched rows.
			TenantID: tenantByAgent[cand.agentID],
		}
		// Approval-gated bulk tasks carry the same expiry as single creates
		// so the sweeper treats both paths identically.
		if status == TaskStatusPendingApproval {
			exp := time.Now().Add(ApprovalExpiryDuration)
			row.ApprovalExpiresAt = &exp
		}
		tasks = append(tasks, row)
	}
	s.agentPendingTasksMu.Unlock()

	// Batch-insert atomically: CreateInBatches commits per chunk, so a
	// mid-way failure would otherwise leave earlier chunks durably inserted
	// while the code decrements ALL pending counters and reports failure —
	// counters drift low and created tasks go unreported. One transaction
	// makes failure all-or-nothing, matching the counter rollback below.
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(tasks, 100).Error
	}); err != nil {
		for i := range tasks {
			s.decPendingTasks(tasks[i].AgentID)
		}
		slog.Error("Batch command: failed to batch-create tasks", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to create tasks")
		return
	}

	// Post-insert: fire hooks, broadcast. Pending counters were already
	// incremented under the gate check above. Hooks go through the bounded
	// worker pool via fireTaskCreatedHook so a bulk dispatch cannot spawn an
	// unbounded goroutine fan-out.
	for i := range tasks {
		if s.pluginManager != nil {
			s.fireTaskCreatedHook(tasks[i].AgentID, tasks[i].ID, tasks[i].Type, tasks[i].Command)
		}
		s.metrics.TasksTotal.Inc()
		s.broadcastTaskUpdate(tasks[i].AgentID, tasks[i])
	}

	taskCount := len(tasks)
	failedCount := len(uniqueIDs) - taskCount - lockedSkipped - dedupSkipped
	if failedCount < 0 {
		failedCount = 0
	}

	slog.Info("Batch command sent", "count", taskCount, "failed", failedCount, "locked_skipped", lockedSkipped, "deduped", dedupSkipped, "type", req.TaskType, "command", truncateString(req.Command, 100))
	s.LogAuditRecord(c, "batch_command", "agent", "", fmt.Sprintf("%s to %d agents (%d failed, %d locked-skipped, %d deduped)", req.TaskType, taskCount, failedCount, lockedSkipped, dedupSkipped), true, nil)

	s.pushBulkResult(BulkResult{
		TenantID:  s.currentTenantID(c),
		Timestamp: time.Now(),
		Command:   req.Command,
		TaskType:  req.TaskType,
		Created:   taskCount,
		Skipped:   lockedSkipped,
		Failed:    failedCount,
		Operator:  operator,
	})

	respond(c, gin.H{

		"tasks_created":  taskCount,
		"failed":         failedCount,
		"locked_skipped": lockedSkipped,
		"deduped":        dedupSkipped,
	})
}

// bulkHistoryFor returns the bulk-command history visible to this caller. The
// history is process-wide, so it must be filtered by tenant: before this it
// returned every tenant's rows, including the command strings.
func (s *Server) bulkHistoryFor(c *gin.Context) []BulkResult {
	tid := s.currentTenantID(c)
	s.bulkHistoryMu.Lock()
	out := make([]BulkResult, 0, len(s.bulkHistory))
	for _, r := range s.bulkHistory {
		if tid == 0 || r.TenantID == tid || r.TenantID == 0 {
			out = append(out, r)
		}
	}
	s.bulkHistoryMu.Unlock()
	return out
}

func (s *Server) handleBulkResults(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}

	page := 1
	pageSize := 20
	// Value-semantics parse: a failed or non-positive value leaves the
	// default untouched (the previous Sscanf-with-pointer-write dance is
	// gone, so the same inputs keep the same outcomes).
	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && v > 0 {
			page = v
		}
	}
	if ps := c.Query("pageSize"); ps != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(ps)); err == nil && v > 0 {
			pageSize = v
		}
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	results := s.bulkHistoryFor(c)

	total := len(results)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	respond(c, gin.H{
		"results":   results[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
