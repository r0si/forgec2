package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// getAgentOrFail fetches agent by ID within the caller's tenant scope.
// On failure writes JSON 404 and returns false.
func (s *Server) getAgentOrFail(c *gin.Context, id string) (db.Implant, bool) {
	var agent db.Implant
	q := s.db.Model(&db.Implant{})
	q = s.tenantScope(q, c)
	if err := q.First(&agent, "id = ?", id).Error; err != nil {
		slog.Error("Agent not found", "agent_id", id, "error", err)
		respondError(c, http.StatusNotFound, "agent not found")
		return agent, false
	}
	return agent, true
}

// TaskOption configures optional createTask behaviour.
type TaskOption func(*taskOptions)
type taskOptions struct {
	callerUserID   uint
	idempotencyKey string
	// forceWrite bypasses another operator's collaboration lock; non-empty
	// is the human-provided reason (audited). Only set for explicit admin
	// overrides — see issueAgentTask.
	forceWrite string
}

// WithCaller tags a createTask call with the operator user ID so the
// soft-lock check can exclude the caller from the conflict list.
func WithCaller(uid uint) TaskOption {
	return func(o *taskOptions) { o.callerUserID = uid }
}

// WithIdempotencyKey deduplicates task creation: when non-empty and a live
// (pending/pending_approval/running) task with the same key already exists
// on the agent, createTask returns it instead of inserting a duplicate
// (double-clicks, retried automation, replayed API calls).
func WithIdempotencyKey(key string) TaskOption {
	return func(o *taskOptions) { o.idempotencyKey = key }
}

// WithForceWrite overrides another operator's collaboration lock with a
// human reason (audited at creation). Callers must restrict this to explicit
// admin overrides; system paths never set it.
func WithForceWrite(reason string) TaskOption {
	return func(o *taskOptions) { o.forceWrite = reason }
}

// callerOpts extracts the user_id from gin.Context and returns a WithCaller
// option. If the context has no user_id (e.g. automation, scripting), it
// returns nil so the soft-lock check is skipped.
func callerOpts(c *gin.Context) []TaskOption {
	if c == nil {
		return nil
	}
	uid, _ := c.Get("user_id")
	u, ok := uid.(uint)
	if !ok || u == 0 {
		return nil
	}
	return []TaskOption{WithCaller(u)}
}

// validateTaskCreation applies every shared gate for new tasks: task-type
// validity, chrome-agent affinity, Rules-of-Engagement, the lportfwd kill
// switch, adaptive OPSEC blocking, and the operator soft-lock. Centralizing
// these keeps the single-task and batch paths consistent so neither can
// bypass a gate. Creation paths that do not already hold the agent row call
// this (the RoE gate SELECTs it); batch paths use
// validateTaskCreationWithAgent to reuse their own batch load.
func (s *Server) validateTaskCreation(c *gin.Context, agentID, taskType, command, data, path string, callerUserID uint) error {
	return s.validateTaskCreationWithAgent(c, agentID, taskType, command, data, path, callerUserID, nil)
}

// validateTaskCreationWithAgent is validateTaskCreation for callers that
// already hold the agent row (the batch path loads all agents up front):
// passing preloadedAgent lets the RoE gate check that row in memory instead
// of re-SELECTing every agent, which is one point query per agent on the
// single-writer pool. With preloadedAgent == nil it behaves exactly like
// validateTaskCreation.
func (s *Server) validateTaskCreationWithAgent(c *gin.Context, agentID, taskType, command, data, path string, callerUserID uint, preloadedAgent *db.Implant) error {
	if !IsKnownTaskType(taskType) && !protocol.ValidTaskType(taskType) {
		return fmt.Errorf("unknown task type: %s", taskType)
	}

	// lportfwd opens a tunneled egress path through the teamserver; honor the
	// server.lportfwd_enabled kill switch centrally so every creation path
	// (handlers, bulk, automation, scripting) inherits it.
	if preloadedAgent != nil {
		if err := s.checkRoELoaded(*preloadedAgent, taskType, command, data, path); err != nil {
			return err
		}
	} else if err := s.checkRoE(agentID, taskType, command, data, path); err != nil {
		return err
	}

	if (taskType == protocol.TaskTypeLPortFwdStart) && !s.lportFwdAllowed() {
		slog.Warn("lportfwd task refused: disabled by configuration", "agent_id", agentID)
		return fmt.Errorf("lportfwd is disabled by server configuration (server.lportfwd_enabled)")
	}

	// Adaptive OPSEC gate: agents at critical threat level cannot launch
	// credential-access / injection operations. Enforced at creation so every
	// path (handlers, bulk, automation, workflow, scripting) inherits the gate.
	if s.opsecAdaptive != nil && s.opsecAdaptive.ShouldBlockAction(agentID, taskType) {
		slog.Warn("Task blocked by adaptive opsec (critical threat level)",
			"agent_id", agentID, "task_type", taskType)
		s.LogAuditRecord(nil, "opsec_block", "agent", agentID,
			"blocked "+taskType+" (critical threat level)", false, nil)
		return fmt.Errorf("blocked by adaptive opsec: %s is not allowed on a critical-threat host", taskType)
	}

	// Engagement deadline: an implant built from a secret with an expiry must stop accepting work when
	// that deadline passes. Enforced here, at the single creation gate every path goes through, so a
	// handler, a bulk action, an automation rule and a script cannot disagree about it.
	if agent := s.agentForExpiryCheck(agentID, preloadedAgent); agent != nil {
		// Load the engagement ONCE: the same row answers "has the window opened" and "has it closed".
		engagement := s.engagementFor(*agent)
		if s.engagementNotStarted(*agent, engagement, time.Now()) {
			// The end of the window was enforced in an earlier round; refusing to start early is the
			// symmetric half, and without it the agreed window does not bound anything on the left.
			slog.Warn("Task refused: engagement has not started", "agent_id", agentID, "task_type", taskType,
				"starts_at", engagement.StartsAt.UTC().Format(time.RFC3339))
			s.LogAuditRecord(nil, "agent_not_started_block", "agent", agentID,
				fmt.Sprintf("refused %s: engagement starts %s", taskType, engagement.StartsAt.UTC().Format(time.RFC3339)), false, nil)
			return fmt.Errorf("agent not in scope yet: the engagement for this implant starts at %s",
				engagement.StartsAt.UTC().Format(time.RFC3339))
		}
		if deadline := effectiveDeadlineFor(*agent, engagement); deadline != nil && time.Now().After(*deadline) {
			// Name the EFFECTIVE deadline: it may be the engagement window rather than the implant own,
			// and an operator needs to know which one stopped the work.
			when := deadline.UTC().Format(time.RFC3339)
			slog.Warn("Task refused: agent engagement expired", "agent_id", agentID, "task_type", taskType, "deadline", when)
			s.LogAuditRecord(nil, "agent_expired_block", "agent", agentID,
				fmt.Sprintf("refused %s: deadline passed (%s)", taskType, when), false, nil)
			return fmt.Errorf("agent expired: the engagement deadline for this implant passed at %s", when)
		}
	}

	// Soft-lock: if another operator is actively viewing this agent, reject
	// the task to prevent conflicting concurrent commands.
	if callerUserID != 0 && s.operatorSessions != nil {
		if others := s.operatorSessions.ActiveOperatorsForAgent(agentID, callerUserID); len(others) > 0 {
			return fmt.Errorf("agent conflict: %s is being actively operated by %s", agentID, joinUsernames(others))
		}
	}

	return nil
}

// isUniqueViolation reports whether err is a unique-constraint violation on
// either supported driver (sqlite: "UNIQUE constraint failed", postgres:
// "duplicate key value violates unique constraint" / SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "23505")
}

// taskRequestError marks a createTask failure that was caused by the request
// itself (unknown type, missing parameter, RoE/OPSEC refusal, queue or lock
// limits) instead of a server fault. API handlers answer 400 with the message
// so operators see why nothing was queued; internal/DB failures keep the
// generic 500.
type taskRequestError struct{ msg string }

func (e *taskRequestError) Error() string { return e.msg }

func taskRequestErrorf(format string, args ...any) error {
	return &taskRequestError{msg: fmt.Sprintf(format, args...)}
}

// createTask creates and persists a new pending task. Returns the task or error.
func (s *Server) createTask(agentID, taskType, command, shell, path, data string, offset, size int64, opts ...TaskOption) (*db.Task, error) {
	var tOpts taskOptions
	for _, opt := range opts {
		opt(&tOpts)
	}

	if err := s.validateTaskCreation(nil, agentID, taskType, command, data, path, tOpts.callerUserID); err != nil {
		return nil, &taskRequestError{msg: err.Error()}
	}

	// Collaboration lock: another operator's live lock blocks creation unless
	// this call carries an explicit admin force override (audited inside).
	if err := s.checkAgentLockForCreate(agentID, &tOpts); err != nil {
		return nil, &taskRequestError{msg: err.Error()}
	}

	if len(command) > MaxCommandLength {
		return nil, taskRequestErrorf("command too long (max %d characters)", MaxCommandLength)
	}

	// set_sleep bounds: the agent rejects interval <= 0 but honors absurd
	// values — a huge interval parks the agent for years, a huge jitter
	// swings sleeps negative (floored to zero) into a beacon-hammering tight
	// loop. Clamp centrally so every creation path (operator API, batch,
	// macros, scheduler, rerun) inherits the same floors/ceilings as the
	// automation and AI paths already enforce.
	if taskType == "set_sleep" {
		var err error
		command, err = clampSetSleepCommand(command)
		if err != nil {
			return nil, &taskRequestError{msg: err.Error()}
		}
	}

	// Idempotency: a live task with the same key short-circuits creation.
	// Checked before the pending-counter increment so dedup hits never
	// consume quota. Terminal tasks (completed/failed/cancelled/timeout)
	// never block key reuse.
	if key := strings.TrimSpace(tOpts.idempotencyKey); key != "" {
		if len(key) > 64 {
			return nil, taskRequestErrorf("idempotency key too long (max 64 characters)")
		}
		var existing db.Task
		if err := s.db.Where("agent_id = ? AND idempotency_key = ? AND status IN ?",
			agentID, key, []string{"pending", TaskStatusPendingApproval, "running"}).
			Order("id DESC").First(&existing).Error; err == nil {
			return &existing, nil
		}
		tOpts.idempotencyKey = key
	}

	if info, ok := getTaskTypeInfo(taskType); ok {
		for _, p := range info.Parameters {
			if p.Required {
				switch p.Name {
				case "command":
					if command == "" {
						return nil, taskRequestErrorf("task type %s requires 'command' parameter", taskType)
					}
				case "shell":
					if shell == "" {
						return nil, taskRequestErrorf("task type %s requires 'shell' parameter", taskType)
					}
				case "path":
					if path == "" {
						return nil, taskRequestErrorf("task type %s requires 'path' parameter", taskType)
					}
				case "data":
					if data == "" {
						return nil, taskRequestErrorf("task type %s requires 'data' parameter", taskType)
					}
				}
			}
		}
	}

	s.agentPendingTasksMu.Lock()
	pending := s.agentPendingTasks[agentID]
	if pending >= MaxPendingTasksPerAgent {
		s.agentPendingTasksMu.Unlock()
		return nil, taskRequestErrorf("agent %s has %d pending tasks (limit %d)", agentID, pending, MaxPendingTasksPerAgent)
	}
	s.agentPendingTasks[agentID] = pending + 1
	s.agentPendingTasksMu.Unlock()

	task := db.Task{
		AgentID: agentID,
		Type:    taskType,
		Command: command,
		Shell:   shell,
		Path:    path,
		Data:    data,
		Offset:  offset,
		Size:    size,
		Status:  "pending",
	}
	// Inherit the agent's tenant so tenant-scoped single-task reads
	// (handleGetTaskStatus/apiGetTask) see the row. Previously tasks were
	// always tenant 0 while operators sit on tenant 1, so list (agent-gated,
	// no task-tenant filter) showed tasks that single-fetch 404d.
	var ag db.Implant
	if err := s.db.Select("tenant_id").Where("id = ?", agentID).First(&ag).Error; err == nil {
		task.TenantID = ag.TenantID
	}
	if tOpts.idempotencyKey != "" {
		task.IdempotencyKey = tOpts.idempotencyKey
	}

	task.Status = s.resolveInitialTaskStatus(taskType)
	if task.Status == TaskStatusPendingApproval {
		exp := time.Now().Add(ApprovalExpiryDuration)
		task.ApprovalExpiresAt = &exp
	}
	if err := s.db.Create(&task).Error; err != nil {
		s.decPendingTasks(agentID)
		// Close the SELECT-then-INSERT race at the database: a concurrent
		// creation with the same key hits the partial unique index. Return
		// the winner instead of an error so double-clicks/retries collapse.
		if tOpts.idempotencyKey != "" && isUniqueViolation(err) {
			var existing db.Task
			if e2 := s.db.Where("agent_id = ? AND idempotency_key = ? AND status IN ?",
				agentID, tOpts.idempotencyKey, []string{"pending", TaskStatusPendingApproval, "running"}).
				Order("id DESC").First(&existing).Error; e2 == nil {
				return &existing, nil
			}
		}
		return nil, err
	}
	// lportfwd bookkeeping: register the operator-declared target so
	// connect frames can be validated against it, and clear declarations on
	// stop (P1 — closes the arbitrary-dial SSRF via agent-controlled frames).
	switch taskType {
	case protocol.TaskTypeLPortFwdStart:
		if _, target, ok := parseLPortFwdCommand(command); ok {
			s.registerLPortFwdDecl(agentID, target)
		}
	case protocol.TaskTypeLPortFwdStop:
		s.clearLPortFwdDecl(agentID)
	}
	if s.pluginManager != nil {
		s.fireTaskCreatedHook(agentID, task.ID, taskType, command)
	}
	if s.metrics != nil {
		s.metrics.TasksTotal.Inc()
	}
	return &task, nil
}

// createSystemTask inserts a task from a non-operator path (automation
// rules, AI tools, abort injection helpers) with the same accounting
// createTask applies: per-agent cap via the shared counter, tenant
// inheritance, the task_created plugin hook, and TasksTotal.
//
// Approval routing and broadcast stay the caller's job: the caller computes
// status (pending vs pending_approval) before calling, and broadcasts when
// appropriate. Operator-only gates (RoE, collab soft-lock) are intentionally
// not applied — system paths are not interactive operators.
func (s *Server) createSystemTask(agentID, taskType, command, shell, status, createdBy string) (*db.Task, error) {
	return s.createSystemTaskWithData(agentID, taskType, command, shell, "", status, createdBy)
}

// createSystemTaskWithData is the Data-carrying form of createSystemTask for
// system tasks whose payload is a structured blob (profile rotation, config
// push). Same accounting as createSystemTask.
func (s *Server) createSystemTaskWithData(agentID, taskType, command, shell, data, status, createdBy string) (*db.Task, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent id required")
	}
	if taskType == "" {
		return nil, fmt.Errorf("task type required")
	}
	if status == "" {
		status = "pending"
	}
	if status != "pending" && status != TaskStatusPendingApproval {
		return nil, fmt.Errorf("invalid system task status %q", status)
	}
	if err := s.trackPendingTask(agentID); err != nil {
		return nil, err
	}
	task := db.Task{
		AgentID: agentID, Type: taskType, Command: command,
		Shell: shell, Data: data, Status: status, CreatedBy: createdBy,
	}
	// Same tenant inheritance as createTask: without it tenant-scoped
	// single-task reads 404 on system-created rows.
	var ag db.Implant
	if err := s.db.Select("tenant_id").Where("id = ?", agentID).First(&ag).Error; err == nil {
		task.TenantID = ag.TenantID
	}
	if status == TaskStatusPendingApproval {
		exp := time.Now().Add(ApprovalExpiryDuration)
		task.ApprovalExpiresAt = &exp
	}
	if err := s.db.Create(&task).Error; err != nil {
		s.decPendingTasks(agentID)
		return nil, err
	}
	s.fireTaskCreatedHook(agentID, task.ID, taskType, command)
	if s.metrics != nil {
		s.metrics.TasksTotal.Inc()
	}
	return &task, nil
}

// fireTaskCreatedHook runs the task_created plugin hook best-effort in the
// background. Shared by createTask and createSystemTask so direct inserts
// can't silently skip subscriber automation.
func (s *Server) fireTaskCreatedHook(agentID string, taskID uint, taskType, command string) {
	if s.pluginManager == nil {
		return
	}
	// Route through the bounded worker pool, not a raw goroutine: a bulk
	// dispatch of N agents used to spawn N unbounded goroutines, each running
	// plugin hooks that can block on SQLITE_BUSY, stalling shutdown in wg.Wait.
	s.runTaskWorker("task_created", func() {
		if err := s.pluginManager.ExecuteHook(s.ctx, plugin.Event{
			Type:      plugin.EventTaskCreated,
			Timestamp: time.Now(),
			AgentID:   agentID,
			Payload: map[string]interface{}{
				"task_id":   taskID,
				"task_type": taskType,
				"command":   command,
			},
		}); err != nil {
			slog.Warn("Hook errors on task_created event", "agent_id", agentID, "task_id", taskID, "err", err)
		}
	})
}

// clampSetSleepCommand bounds a set_sleep "interval,jitter" command to the
// agent-safe range (interval 1..86400s, jitter 0..100%). Malformed input
// passes through untouched so downstream validation still reports the
// original error (same contract as clampSleepString).
func clampSetSleepCommand(command string) (string, error) {
	parts := strings.Split(command, ",")
	if len(parts) != 2 {
		return command, nil
	}
	interval, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	jitter, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return command, nil
	}
	if interval < 1 {
		interval = 1
	}
	if interval > 86400 {
		interval = 86400
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 100 {
		jitter = 100
	}
	return fmt.Sprintf("%d,%d", interval, jitter), nil
}

// resolveInitialTaskStatus returns the status a newly created task should start
// in. When operator approval is required and the task type is flagged, the task
// waits in pending_approval; otherwise it is immediately pending. Centralizing
// this keeps the single-task and bulk/batch paths consistent so the two-man
// rule cannot be bypassed through the batch endpoint.
func (s *Server) resolveInitialTaskStatus(taskType string) string {
	s.configMu.RLock()
	requireApproval := s.cfg != nil && s.cfg.Security.RequireApproval
	s.configMu.RUnlock()
	if requireApproval {
		if info, ok := getTaskTypeInfo(taskType); ok && info.RequiresApproval {
			return TaskStatusPendingApproval
		}
	}
	return "pending"
}

// TaskSpec describes a single agent task issued from an HTTP handler.
type TaskSpec struct {
	Type           string
	Command        string
	Shell          string
	Path           string
	Data           string
	Offset         int64
	Size           int64
	IdempotencyKey string
}

// issueAgentTask is the single choke point for handler-issued tasks: it runs
// the tenant-scoped agent lookup, creates the task with the caller's operator
// identity (so the soft-lock gate applies), and maps creation failures to the
// correct client status via respondTaskError. On failure it writes the error
// response and returns nil; on success it returns the task WITHOUT writing a
// response, leaving the caller to dispatch (dispatchTask) or render custom JSON.
func (s *Server) issueAgentTask(c *gin.Context, id string, spec TaskSpec) *db.Task {
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return nil
	}
	opts := append(callerOpts(c), WithIdempotencyKey(spec.IdempotencyKey))
	// Admin force override for another operator's collaboration lock. The
	// reason is mandatory and audited inside createTask's lock gate.
	if c.Query("force") == "true" && s.isAdmin(c) {
		reason := strings.TrimSpace(c.Query("reason"))
		if reason == "" {
			reason = "no reason given"
		}
		opts = append(opts, WithForceWrite(reason))
	}
	task, err := s.createTask(id, spec.Type, spec.Command, spec.Shell, spec.Path, spec.Data, spec.Offset, spec.Size, opts...)
	if err != nil {
		respondTaskError(c, err)
		return nil
	}
	return task
}

// checkAgentLockForCreate enforces another operator's live collaboration lock
// on every createTask path (handlers, bulk, macros, scheduler): non-holders
// get a 409 naming the holder, unless an explicit admin force override
// (WithForceWrite) carries an audited reason. System/automation callers
// (callerUserID == 0) bypass UI-level locks by design and are documented as
// doing so; direct AI row inserts bypass all creation gates (pre-existing).
func (s *Server) checkAgentLockForCreate(agentID string, tOpts *taskOptions) error {
	var lock db.AgentLock
	if err := s.db.Where("agent_id = ?", agentID).First(&lock).Error; err != nil {
		return nil // no lock row: free to write
	}
	holder, live := lockHolderOf(&lock, time.Now())
	if !live || holder == "" {
		return nil
	}
	if tOpts.callerUserID != 0 {
		var me string
		if err := s.db.Model(&db.User{}).Where("id = ?", tOpts.callerUserID).Pluck("username", &me).Error; err == nil && me == holder {
			return nil
		}
	} else {
		return nil // system/automation path: not subject to UI locks
	}
	if tOpts.forceWrite != "" {
		s.LogAuditRecord(nil, "collab_lock_force_write", "agent", agentID,
			"task created on agent locked by "+holder+": "+tOpts.forceWrite, true, nil)
		return nil
	}
	return fmt.Errorf("agent conflict: agent is locked by %s", holder)
}

// taskArgsHash is a short non-reversible fingerprint of a task's arguments
// for audit trails. Raw commands may carry credentials, so only the hash is
// logged — enough to correlate create/approve/execute records of one task.
func taskArgsHash(t *db.Task) string {
	sum := sha256.Sum256([]byte(t.Type + "\x00" + t.Command + "\x00" + t.Shell + "\x00" + t.Path + "\x00" + t.Data))
	return hex.EncodeToString(sum[:])[:16]
}

// persistTaskCreator stamps the creator onto the stored task.
//
// The in-memory value already feeds the audit record, so an ignored write failure left the database
// disagreeing with the audit trail about who ordered a task - the one field an investigation needs.
func (s *Server) persistTaskCreator(task *db.Task, username string) {
	if task == nil || username == "" {
		return
	}
	task.CreatedBy = username
	if err := s.db.Model(task).Update("created_by", username).Error; err != nil {
		slog.Error("Failed to persist task creator", "task_id", task.ID, "creator", username, "error", err)
	}
}

// dispatchTask logs the audit action, broadcasts the update via WS, and returns success JSON.
func (s *Server) dispatchTask(c *gin.Context, task *db.Task, auditAction, details string) {
	user, _ := c.Get("user")
	if username, ok := user.(string); ok && username != "" && task.CreatedBy == "" {
		s.persistTaskCreator(task, username)
	}
	// Dangerous tasks carry creator + args fingerprint so the audit chain
	// answers who ordered what, approved by whom (approve_task record), with
	// which exact arguments — without persisting secrets in the log.
	if dangerousTaskTypes[task.Type] {
		details = fmt.Sprintf("%s [created_by=%s args=%s]", details, task.CreatedBy, taskArgsHash(task))
	}
	s.LogAuditRecord(c, auditAction, "agent", task.AgentID, details, true, nil)
	s.broadcastTaskUpdate(task.AgentID, *task)
	respond(c, gin.H{"task_id": task.ID})
}

// flippedSet re-reads which of ids now carry status. It lets sweep passes
// restrict side effects (broadcasts, counter decs) to rows their guarded
// UPDATE actually flipped, instead of the stale SELECT list — a result
// landing between the statements is skipped by the UPDATE guard yet would
// otherwise still be broadcast as failed and double-dec the agent counter.
// ok=false means the re-read itself failed; callers then fall back to the
// full list (today's behavior) rather than dropping side effects.
func (s *Server) flippedSet(ids []uint, status string) (map[uint]bool, bool) {
	out := make(map[uint]bool, len(ids))
	if len(ids) == 0 {
		return out, true
	}
	var rows []struct {
		ID uint
	}
	if err := s.db.Model(&db.Task{}).Select("id").Where("id IN ? AND status = ?", ids, status).Find(&rows).Error; err != nil {
		slog.Warn("Sweep calibration re-read failed, keeping full side-effect set", "error", err)
		for _, id := range ids {
			out[id] = true
		}
		return out, false
	}
	for _, r := range rows {
		out[r.ID] = true
	}
	return out, true
}

// maxSweepPasses bounds the per-sweep paging loop below: a fleet with more
// than one LIMIT page of stale tasks drains progressively instead of
// starving everything past the first 1000 rows every 5 minutes.
const maxSweepPasses = 5

// requeueStaleTasks retries only tasks whose delivery was never acknowledged,
// capped at 3 delivery attempts; exhausted tasks are failed instead of
// being requeued forever.
//
// The static StaleRunningTaskTimeout is a floor: agents with long sleep
// intervals get max(static, 3x their interval) so a healthy long-sleep
// agent's in-flight task is neither requeued nor failed prematurely.
func (s *Server) requeueStaleTasks() {
	for pass := 0; pass < maxSweepPasses; pass++ {
		if !s.requeueStaleTasksPass() {
			return
		}
	}
}

// requeueStaleTasksPass runs one LIMIT page; it reports whether a full page
// was seen (caller pages again to avoid starving large fleets).
func (s *Server) requeueStaleTasksPass() bool {
	cutoff := time.Now().Add(-StaleRunningTaskTimeout)
	fullPage := false

	// Legacy "sent" rows: nothing creates or claims sent anymore (fetch only
	// moves pending→running), so any row still sitting in sent is a zombie.
	// Fail it outright — never requeue, or ancient rows would resurrect and
	// redeliver stale commands to live agents.
	var sentTasks []db.Task
	if err := s.db.Where("status = ? AND created_at < ? AND claimed_at < ?", "sent", cutoff, cutoff).Limit(1000).Find(&sentTasks).Error; err != nil {
		slog.Error("Failed to find legacy sent tasks", "error", err)
		return false
	}
	fullPage = len(sentTasks) == 1000
	if len(sentTasks) > 0 {
		sentIDs := make([]uint, len(sentTasks))
		for i, t := range sentTasks {
			sentIDs[i] = t.ID
		}
		res := s.withBusyRetryDB("sweep", func() *gorm.DB {
			return s.db.Model(&db.Task{}).Where("id IN ? AND status = ?", sentIDs, "sent").
				Updates(map[string]interface{}{
					"status": "failed",
					"error":  "legacy sent status retired (never dispatched)",
				})
		})
		if res.Error != nil {
			slog.Error("Failed to retire legacy sent tasks", "count", len(sentIDs), "error", res.Error)
			return false
		}
		// Calibrate side effects to rows actually flipped: a concurrent
		// transition narrows RowsAffected, and the skipped rows must get
		// neither broadcast nor dec. Full batches skip the re-read.
		flipped := make(map[uint]bool, len(sentIDs))
		for _, id := range sentIDs {
			flipped[id] = true
		}
		if res.RowsAffected != int64(len(sentIDs)) {
			flipped, _ = s.flippedSet(sentIDs, "failed")
		}
		for i := range sentTasks {
			t := sentTasks[i]
			if !flipped[t.ID] {
				continue
			}
			t.Status = "failed"
			t.Error = "legacy sent status retired (never dispatched)"
			s.broadcastTaskUpdate(t.AgentID, t)
			s.decPendingTasks(t.AgentID)
		}
		slog.Info("Retired legacy sent tasks", "count", len(sentIDs))
	}

	// Tasks already at the delivery-attempt cap: delivered repeatedly but
	// never acknowledged. Fail them so they stop cycling through the queue.
	var exhaustedTasks []db.Task
	if err := s.db.Where("status = ? AND claimed_at < ? AND acknowledged_at IS NULL AND delivery_attempts >= 3", "running", cutoff).Limit(1000).Find(&exhaustedTasks).Error; err != nil {
		slog.Error("Failed to find stale running tasks past attempt cap", "error", err)
		return false
	}
	fullPage = fullPage || len(exhaustedTasks) == 1000
	exhaustedTasks = s.filterLongSleepTasks(exhaustedTasks, cutoff)
	if len(exhaustedTasks) > 0 {
		exhaustedIDs := make([]uint, len(exhaustedTasks))
		for i, t := range exhaustedTasks {
			exhaustedIDs[i] = t.ID
		}
		// Status guard in the UPDATE (not just the SELECT): a result landing
		// between the two statements must not be stomped — previously a
		// just-completed task could be flipped to "failed" here.
		res := s.withBusyRetryDB("sweep", func() *gorm.DB {
			return s.db.Model(&db.Task{}).Where("id IN ? AND status = ? AND acknowledged_at IS NULL", exhaustedIDs, "running").
				Updates(map[string]interface{}{
					"status": "failed",
					"error":  "delivered but unacknowledged after 3 attempts",
				})
		})
		if res.Error != nil {
			slog.Error("Failed to fail stale running tasks past attempt cap", "count", len(exhaustedIDs), "error", res.Error)
			return false
		}
		flippedEx := make(map[uint]bool, len(exhaustedIDs))
		for _, id := range exhaustedIDs {
			flippedEx[id] = true
		}
		if res.RowsAffected != int64(len(exhaustedIDs)) {
			flippedEx, _ = s.flippedSet(exhaustedIDs, "failed")
		}
		for i := range exhaustedTasks {
			t := exhaustedTasks[i]
			if !flippedEx[t.ID] {
				continue
			}
			t.Status = "failed"
			t.Error = "delivered but unacknowledged after 3 attempts"
			s.broadcastTaskUpdate(t.AgentID, t)
			// Failed tasks leave the pending/running count: without this
			// the in-memory counter leaks until the next reconcile and
			// falsely holds MaxPendingTasksPerAgent slots.
			s.decPendingTasks(t.AgentID)
		}
		slog.Info("Failed stale running tasks past delivery-attempt cap", "count", len(exhaustedIDs))
	}

	var staleTasks []db.Task
	// Select only the columns the requeue pass uses: full rows drag
	// Result/Error blobs and fire a pointless per-row AES-GCM decrypt in
	// Task.AfterFind (same pattern as the beacon-claim path).
	if err := s.db.Where("status = ? AND claimed_at < ? AND acknowledged_at IS NULL AND delivery_attempts < 3", "running", cutoff).
		Select("id, agent_id, claimed_at").Limit(1000).Find(&staleTasks).Error; err != nil {
		slog.Error("Failed to find stale running tasks", "error", err)
		return false
	}
	fullPage = fullPage || len(staleTasks) == 1000
	staleTasks = s.filterLongSleepTasks(staleTasks, cutoff)
	if len(staleTasks) == 0 {
		return fullPage
	}
	taskIDs := make([]uint, len(staleTasks))
	for i, t := range staleTasks {
		taskIDs[i] = t.ID
	}
	// Status guard: without it a result arriving between SELECT and UPDATE
	// flipped a completed task back to "pending", re-delivering and
	// double-executing it on the agent.
	res := s.withBusyRetryDB("sweep", func() *gorm.DB {
		return s.db.Model(&db.Task{}).Where("id IN ? AND status = ? AND acknowledged_at IS NULL", taskIDs, "running").
			Updates(map[string]interface{}{"status": "pending", "claimed_by": "", "claimed_at": time.Time{}, "delivery_attempts": gorm.Expr("delivery_attempts + 1")})
	})
	if res.Error != nil {
		slog.Error("Failed to requeue stale running tasks", "count", len(taskIDs), "error", res.Error)
		return false
	}
	slog.Info("Requeued stale running tasks to pending", "count", res.RowsAffected)
	// Requeued tasks stay in the pending/running count: no dec here.
	return fullPage
}

// filterLongSleepTasks drops tasks whose agent sleeps so long that the
// static cutoff has not actually elapsed for them yet: each task gets
// max(StaleRunningTaskTimeout, 3x its agent's current interval). Agents
// without a row or interval fall back to the static cutoff.
func (s *Server) filterLongSleepTasks(tasks []db.Task, staticCutoff time.Time) []db.Task {
	if len(tasks) == 0 {
		return tasks
	}
	agentIDs := make([]string, 0, len(tasks))
	seen := make(map[string]struct{}, len(tasks))
	for _, t := range tasks {
		if _, ok := seen[t.AgentID]; !ok {
			seen[t.AgentID] = struct{}{}
			agentIDs = append(agentIDs, t.AgentID)
		}
	}
	intervals := make(map[string]int, len(agentIDs))
	var rows []struct {
		ID              string
		CurrentInterval int
	}
	if err := s.db.Model(&db.Implant{}).Where("id IN ?", agentIDs).
		Select("id, current_interval").Scan(&rows).Error; err != nil {
		slog.Warn("Stale sweep: interval lookup failed, using static cutoff", "err", err)
		return tasks
	}
	for _, r := range rows {
		intervals[r.ID] = r.CurrentInterval
	}
	now := time.Now()
	kept := tasks[:0]
	for _, t := range tasks {
		perTaskCutoff := staticCutoff
		if iv := intervals[t.AgentID]; iv > 0 {
			if d := now.Add(-3 * time.Duration(iv) * time.Second); d.Before(perTaskCutoff) {
				perTaskCutoff = d
			}
		}
		if t.ClaimedAt.Before(perTaskCutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// failStaleAcknowledgedTasks marks tasks that were acknowledged but never produced a result.
// Renewal first: if the agent beaconed after acknowledging (it's alive and
// still working), the deadline is extended by one more window instead of
// failing a healthy long task. At most one renewal per task — a wedged agent
// that beacons forever without finishing still gets failed.
func (s *Server) failStaleAcknowledgedTasks() {
	for pass := 0; pass < maxSweepPasses; pass++ {
		if !s.failStaleAcknowledgedTasksPass() {
			return
		}
	}
}

// failStaleAcknowledgedTasksPass runs one LIMIT page; true means a full page
// was seen and the caller should page again.
func (s *Server) failStaleAcknowledgedTasksPass() bool {
	cutoff := time.Now().Add(-AckedTaskResultTimeout)
	var staleTasks []db.Task
	// Column Select: the pass only needs id/agent_id/acknowledged_at —
	// full rows would drag Result/Error blobs and fire per-row decryption.
	if err := s.db.Where("status = ? AND acknowledged_at IS NOT NULL AND acknowledged_at < ?", "running", cutoff).
		Select("id, agent_id, acknowledged_at").Limit(1000).Find(&staleTasks).Error; err != nil {
		slog.Error("Failed to find stale acknowledged tasks", "error", err)
		return false
	}
	fullPage := len(staleTasks) == 1000
	if len(staleTasks) == 0 {
		return false
	}
	// Agent liveness per involved agent (single query).
	agentIDs := make([]string, 0, len(staleTasks))
	seenAgents := make(map[string]struct{}, len(staleTasks))
	for _, t := range staleTasks {
		if _, ok := seenAgents[t.AgentID]; !ok {
			seenAgents[t.AgentID] = struct{}{}
			agentIDs = append(agentIDs, t.AgentID)
		}
	}
	lastSeen := make(map[string]time.Time, len(agentIDs))
	var implants []struct {
		ID       string
		LastSeen time.Time
	}
	if err := s.db.Model(&db.Implant{}).Where("id IN ?", agentIDs).
		Select("id, last_seen").Scan(&implants).Error; err != nil {
		slog.Warn("Acked sweep: agent lookup failed, failing all stale", "err", err)
	} else {
		for _, im := range implants {
			lastSeen[im.ID] = im.LastSeen
		}
	}
	now := time.Now()
	var failIDs []uint
	var renewIDs []uint
	for _, t := range staleTasks {
		// Renew once: agent alive since ack, and within 2 windows of the ack
		// (bounds wedged agents to a single extension).
		if t.AcknowledgedAt != nil &&
			now.Sub(*t.AcknowledgedAt) < 2*AckedTaskResultTimeout &&
			lastSeen[t.AgentID].After(*t.AcknowledgedAt) {
			renewIDs = append(renewIDs, t.ID)
			continue
		}
		failIDs = append(failIDs, t.ID)
	}
	renewed := 0
	if len(renewIDs) > 0 {
		// One guarded batch UPDATE instead of one busy-retry round trip per
		// task: the status guard still skips rows whose result arrived
		// between SELECT and UPDATE.
		res := s.withBusyRetryDB("sweep", func() *gorm.DB {
			return s.db.Model(&db.Task{}).Where("id IN ? AND status = ?", renewIDs, "running").
				Update("acknowledged_at", now)
		})
		if res.Error != nil {
			slog.Warn("Acked sweep: batch renewal failed", "count", len(renewIDs), "err", res.Error)
		} else {
			renewed = int(res.RowsAffected)
		}
	}
	if renewed > 0 {
		slog.Info("Renewed acked tasks for live agents", "count", renewed)
	}
	if len(failIDs) == 0 {
		return fullPage
	}
	// Status guard plus keep any partial result: the previous unconditional
	// update blanked "result" on tasks whose output arrived between SELECT
	// and UPDATE, destroying real data.
	res := s.withBusyRetryDB("sweep", func() *gorm.DB {
		return s.db.Model(&db.Task{}).Where("id IN ? AND status = ?", failIDs, "running").
			Updates(map[string]interface{}{
				"status": "failed",
				"error":  "task acknowledged but no result received within timeout",
			})
	})
	if res.Error != nil {
		slog.Error("Failed to fail stale acknowledged tasks", "count", len(failIDs), "error", res.Error)
		return false
	}
	flippedAck := make(map[uint]bool, len(failIDs))
	for _, id := range failIDs {
		flippedAck[id] = true
	}
	if res.RowsAffected != int64(len(failIDs)) {
		flippedAck, _ = s.flippedSet(failIDs, "failed")
	}
	failedByID := make(map[uint]db.Task, len(failIDs))
	for _, t := range staleTasks {
		failedByID[t.ID] = t
	}
	for _, id := range failIDs {
		t := failedByID[id]
		if !flippedAck[id] {
			continue
		}
		t.Status = "failed"
		t.Error = "task acknowledged but no result received within timeout"
		t.Result = ""
		s.broadcastTaskUpdate(t.AgentID, t)
		// Failed tasks leave the pending/running count (same leak as the
		// exhausted path had): dec here, reconcile heals any guard race.
		s.decPendingTasks(t.AgentID)
	}
	slog.Info("Failed stale acknowledged tasks with no result", "count", len(failIDs))
	return fullPage
}

// rejectExpiredApprovals auto-rejects approval-gated tasks past their
// expiry: a stale approval prompt is a confused-deputy risk (the world
// changed since creation). Operators are notified once per sweep when any
// were rejected.
func (s *Server) rejectExpiredApprovals() {
	now := time.Now()
	var stale []db.Task
	if err := s.db.Where("status = ? AND approval_expires_at IS NOT NULL AND approval_expires_at < ?",
		TaskStatusPendingApproval, now).Limit(1000).Find(&stale).Error; err != nil {
		slog.Error("Failed to find expired approvals", "error", err)
		return
	}
	if len(stale) == 0 {
		return
	}
	ids := make([]uint, len(stale))
	for i, t := range stale {
		ids[i] = t.ID
	}
	res := s.withBusyRetryDB("sweep", func() *gorm.DB {
		return s.db.Model(&db.Task{}).Where("id IN ? AND status = ?", ids, TaskStatusPendingApproval).
			Updates(map[string]interface{}{
				"status": "cancelled",
				"error":  "approval expired without a second operator",
			})
	})
	if res.Error != nil {
		slog.Error("Failed to reject expired approvals", "count", len(ids), "error", res.Error)
		return
	}
	flippedAppr := make(map[uint]bool, len(ids))
	for _, id := range ids {
		flippedAppr[id] = true
	}
	if res.RowsAffected != int64(len(ids)) {
		flippedAppr, _ = s.flippedSet(ids, "cancelled")
	}
	for _, t := range stale {
		if !flippedAppr[t.ID] {
			continue
		}
		t.Status = "cancelled"
		t.Error = "approval expired without a second operator"
		s.broadcastTaskUpdate(t.AgentID, t)
		s.decPendingTasks(t.AgentID)
	}
	s.LogAuditRecord(nil, "approval_expired", "task", "", "auto-rejected stale approvals", true, nil)
	s.DispatchNotification(&db.Notification{
		Type:     "approval_expired",
		Title:    "Stale task approvals auto-rejected",
		Message:  fmt.Sprintf("%d tasks awaited approval past expiry and were cancelled", len(stale)),
		Severity: "warning",
	})
	slog.Info("Auto-rejected expired approvals", "count", len(stale))
}

// updateTaskBacklogMetrics refreshes per-agent backlog gauges and warns when
// an agent sits above 80% of its pending cap (early signal before the queue
// starts refusing work, including abort injections).
func (s *Server) updateTaskBacklogMetrics() {
	if s.metrics == nil || s.db == nil {
		return
	}
	var rows []struct {
		AgentID string
		Count   int
		Oldest  time.Time
	}
	if err := s.db.Model(&db.Task{}).
		Select("agent_id, COUNT(*) as count, MIN(created_at) as oldest").
		Where("status = ?", "pending").
		Group("agent_id").
		Find(&rows).Error; err != nil {
		slog.Error("Failed to compute task backlog metrics", "err", err)
		return
	}
	seen := make(map[string]struct{}, len(rows))
	now := time.Now()
	for _, r := range rows {
		seen[r.AgentID] = struct{}{}
		s.metrics.TasksPendingByAgent.WithLabelValues(r.AgentID).Set(float64(r.Count))
		s.metrics.OldestPendingSeconds.WithLabelValues(r.AgentID).Set(now.Sub(r.Oldest).Seconds())
		if r.Count >= MaxPendingTasksPerAgent*8/10 {
			slog.Warn("Agent task backlog above 80% of cap", "agent_id", r.AgentID, "pending", r.Count, "cap", MaxPendingTasksPerAgent)
		}
	}
	// Prune series for agents that drained, so deleted-agent labels do not
	// accumulate forever.
	s.backlogAgentsMu.Lock()
	if s.backlogAgents == nil {
		s.backlogAgents = make(map[string]struct{})
	}
	for id := range s.backlogAgents {
		if _, ok := seen[id]; !ok {
			s.metrics.TasksPendingByAgent.DeleteLabelValues(id)
			s.metrics.OldestPendingSeconds.DeleteLabelValues(id)
			delete(s.backlogAgents, id)
		}
	}
	for id := range seen {
		s.backlogAgents[id] = struct{}{}
	}
	s.backlogAgentsMu.Unlock()
}

// reconcilePendingTaskCounts recomputes the in-memory pending task counter from the DB.
func (s *Server) reconcilePendingTaskCounts() {
	var results []struct {
		AgentID string
		Count   int
	}
	// Include pending_approval: createTask counts approval-queued slots and
	// cancel/reject release them, so the recount must use the same semantics
	// or every pass silently shrinks counts and defeats MaxPendingTasksPerAgent.
	if err := s.db.Model(&db.Task{}).
		Select("agent_id, COUNT(*) as count").
		Where("status IN ?", []string{"pending", "running", TaskStatusPendingApproval}).
		Group("agent_id").
		Find(&results).Error; err != nil {
		slog.Error("Failed to reconcile pending task counts", "error", err)
		return
	}
	s.agentPendingTasksMu.Lock()
	clear(s.agentPendingTasks)
	for _, r := range results {
		s.agentPendingTasks[r.AgentID] = r.Count
	}
	s.agentPendingTasksMu.Unlock()
}

// decPendingTasks decrements an agent's pending task counter and removes the
// map entry when it reaches zero, preventing unbounded map growth across the
// server lifetime (and leaked counts after an agent is purged).
func (s *Server) decPendingTasks(agentID string) {
	s.agentPendingTasksMu.Lock()
	if n := s.agentPendingTasks[agentID]; n > 0 {
		if n-1 <= 0 {
			delete(s.agentPendingTasks, agentID)
		} else {
			s.agentPendingTasks[agentID] = n - 1
		}
	}
	s.agentPendingTasksMu.Unlock()
}

// trackPendingTask enforces MaxPendingTasksPerAgent and bumps the in-memory
// counter for tasks created OUTSIDE the standard createTask/dispatch path
// (the AI assistant inserts Task rows directly). Callers must decPendingTasks
// if the subsequent insert fails so the counter never drifts high.
func (s *Server) trackPendingTask(agentID string) error {
	return s.trackPendingTaskReserved(agentID, 0)
}

// trackPendingTaskReserved is trackPendingTask plus extra headroom slots for
// priority injections (abort tasks): cancelling a running task must be able
// to queue its abort even when the backlog is at the normal cap.
func (s *Server) trackPendingTaskReserved(agentID string, extra int) error {
	s.agentPendingTasksMu.Lock()
	defer s.agentPendingTasksMu.Unlock()
	// A Server built without the map (tests, future constructors) must not
	// turn a task insert into a nil-map panic.
	if s.agentPendingTasks == nil {
		s.agentPendingTasks = make(map[string]int)
	}
	if n := s.agentPendingTasks[agentID]; n >= MaxPendingTasksPerAgent+extra {
		return fmt.Errorf("agent %s has %d pending tasks (limit %d)", agentID, n, MaxPendingTasksPerAgent+extra)
	}
	s.agentPendingTasks[agentID]++
	return nil
}

// taskWorkerAcquireTimeout bounds how long a background worker waits for a
// pool slot. Without it, a saturated pool (callbacks/hooks wedged on
// SQLITE_BUSY) parks wg-tracked goroutines forever and stalls graceful
// shutdown inside wg.Wait.
const taskWorkerAcquireTimeout = 30 * time.Second

// runTaskWorker runs fn in a wg-tracked goroutine holding one pool slot. When
// the pool stays saturated past the timeout the work is dropped with a log
// instead of wedging shutdown. Payloads are value-captured by the caller.
func (s *Server) runTaskWorker(what string, fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Task worker panicked", "what", what, "recover", r)
			}
		}()
		select {
		case s.taskWorkerSem <- struct{}{}:
		case <-time.After(taskWorkerAcquireTimeout):
			slog.Warn("Task worker pool saturated, dropping background work", "what", what)
			return
		}
		defer func() { <-s.taskWorkerSem }()
		fn()
	}()
}

// joinUsernames joins a slice of usernames with commas for error messages.
func joinUsernames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return names[0] + " and " + fmt.Sprintf("%d others", len(names)-1)
	}
}

// isConflictError returns true if the error is an agent conflict (soft-lock).
func isConflictError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "agent conflict:")
}

// respondTaskError writes the appropriate HTTP error for a createTask failure.
// Agent conflicts are surfaced as 409. Validation/policy errors raised before
// any DB write (unknown task type, missing required parameter, opsec block,
// lportfwd disabled, pending-task limit) are client errors (400/403/429) so the
// operator UI can render them instead of a generic 500. Only genuine internal
// failures (DB errors) become 500.
func respondTaskError(c *gin.Context, err error) {
	if isConflictError(err) {
		respondError(c, http.StatusConflict, err.Error())
		return
	}
	if status, ok := clientErrorStatus(err); ok {
		respondError(c, status, err.Error())
		return
	}
	slog.Error("Failed to create task", "error", err)
	respondError(c, http.StatusInternalServerError, "failed to create task")
}

// clientErrorStatus maps the validation/policy errors returned by createTask /
// validateTaskCreation to the correct client HTTP status, keeping them out of
// the 500 bucket.
func clientErrorStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "too many pending tasks"),
		strings.Contains(msg, "pending tasks (limit"):
		return http.StatusTooManyRequests, true
	case strings.Contains(msg, "requires a chrome-tagged agent"),
		strings.Contains(msg, "is not supported on chrome extension agents"),
		strings.Contains(msg, "lportfwd is disabled"),
		strings.Contains(msg, "blocked by adaptive opsec"),
		strings.Contains(msg, "agent expired"),
		strings.Contains(msg, "agent not in scope yet"):
		return http.StatusForbidden, true
	case strings.Contains(msg, "unknown task type"),
		strings.Contains(msg, "requires 'command' parameter"),
		strings.Contains(msg, "requires 'shell' parameter"),
		strings.Contains(msg, "requires 'path' parameter"),
		strings.Contains(msg, "requires 'data' parameter"),
		strings.Contains(msg, "command too long"):
		return http.StatusBadRequest, true
	}
	return 0, false
}

// agentForExpiryCheck returns the agent row used to evaluate the engagement deadline, reusing an
// already-loaded row when the caller has one.
func (s *Server) agentForExpiryCheck(agentID string, preloaded *db.Implant) *db.Implant {
	if preloaded != nil {
		return preloaded
	}
	var agent db.Implant
	// engagement_id must be projected: without it the merged deadline silently ignored the engagement
	// and the whole feature was inert on the real path (the enforcement test caught exactly that).
	if err := s.db.Select("id", "expires_at", "engagement_id").Where("id = ?", agentID).First(&agent).Error; err != nil {
		return nil
	}
	return &agent
}

// agentExpired reports whether the engagement deadline has passed. A nil deadline is unbounded, which
// keeps every existing installation behaving exactly as before.
func (s *Server) agentExpired(agent db.Implant) bool {
	deadline := s.effectiveAgentDeadline(agent)
	return deadline != nil && !deadline.IsZero() && time.Now().After(*deadline)
}

// effectiveAgentDeadline merges the two deadlines an implant can carry: its own (inherited from the
// registration secret) and the window of the engagement it belongs to. The EARLIER one wins, because
// the engagement is the promise made to the customer - an implant built to run for a month must still
// stop when the exercise it belongs to ends, or the next day, whichever is sooner.
//
// The engagement lookup only happens when the implant is linked to one, so installations without
// engagements pay nothing for the feature. A closed engagement ends at ClosedAt, not at its agreed
// EndsAt: closing early is a decision, and the earlier end is the one that applies.
func (s *Server) effectiveAgentDeadline(agent db.Implant) *time.Time {
	return effectiveDeadlineFor(agent, s.engagementFor(agent))
}

// engagementFor loads the engagement an implant belongs to, or nil. One query, shared by the "has it
// started" check and the deadline merge so a task creation reads the engagement once.
//
// An unreadable row returns nil, which means "no engagement constraints": the implant deadline still
// applies, and a database hiccup must not silently stop work.
func (s *Server) engagementFor(agent db.Implant) *db.Engagement {
	if agent.EngagementID == nil {
		return nil
	}
	var engagement db.Engagement
	if err := s.db.Select("id", "starts_at", "ends_at", "closed_at").
		Where("id = ?", *agent.EngagementID).First(&engagement).Error; err != nil {
		return nil
	}
	return &engagement
}

// effectiveDeadlineFor merges the implant deadline with an already-loaded engagement window.
func effectiveDeadlineFor(agent db.Implant, engagement *db.Engagement) *time.Time {
	effective := agent.ExpiresAt
	if engagement == nil {
		return effective
	}
	end := engagement.EndsAt
	if engagement.ClosedAt != nil && engagement.ClosedAt.Before(end) {
		end = *engagement.ClosedAt
	}
	if end.IsZero() {
		return effective
	}
	if effective == nil || end.Before(*effective) {
		return &end
	}
	return effective
}

// engagementNotStarted reports the window an implant must wait for: work is only allowed while the
// engagement it belongs to is open. The end of the window was enforced in an earlier round; the start is
// the symmetric half, and without it an operator can task a host before the exercise has begun - the
// exact thing the agreed window exists to prevent.
func (s *Server) engagementNotStarted(agent db.Implant, engagement *db.Engagement, now time.Time) bool {
	if engagement == nil || engagement.StartsAt.IsZero() {
		return false
	}
	return now.Before(engagement.StartsAt)
}
