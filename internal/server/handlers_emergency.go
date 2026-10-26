package server

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) handleEmergencyStop(c *gin.Context) {
	password := c.PostForm("password")
	if password == "" {
		respondError(c, http.StatusBadRequest, "Password confirmation required")
		return
	}

	// Resolve the caller first: First(&user, any) with a nil primary key
	// matched the first row in the table, so a context without user_id
	// confirmed the password against an arbitrary account and then armed the
	// kill switch. Unknown principals are refused outright.
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	var user db.User
	if err := s.db.First(&user, uid).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "User not found")
		return
	}

	if !middleware.CheckPassword(user.PasswordHash, password) {
		respondError(c, http.StatusUnauthorized, "Invalid password")
		return
	}

	killDate := time.Now()
	groupID := c.PostForm("group_id")

	query := s.db.Model(&db.Implant{}).Where("status != ?", "offline")

	if groupID != "" {
		// Group-scoped kill: only kill agents in the specified group
		var group db.AgentGroup
		if err := s.db.Where("id = ?", groupID).First(&group).Error; err != nil {
			respondError(c, http.StatusNotFound, "Agent group not found")
			return
		}
		query = query.Joins("JOIN agent_group_assignments ON agent_group_assignments.implant_id = implants.id").
			Where("agent_group_assignments.agent_group_id = ?", groupID)
	}

	result := query.Update("kill_date", killDate)

	if err := result.Error; err != nil {
		slog.Error("Emergency stop: failed to set kill dates", "err", err)
		respondError(c, http.StatusInternalServerError, "Emergency stop failed")
		return
	}

	scope := "all online agents"
	if groupID != "" {
		scope = "group " + groupID
	}

	s.LogAuditRecord(c, "emergency_stop", "security", user.Username,
		"Emergency stop activated ("+scope+")", true, nil)

	s.broadcastSystemAlert("EMERGENCY STOP",
		"Emergency stop activated by "+user.Username+". "+scope+" will self-terminate.",
		"emergency_stop")

	slog.Warn("EMERGENCY STOP ACTIVATED",
		"user", user.Username,
		"scope", scope,
		"agents_affected", result.RowsAffected)

	msg := "Emergency stop activated"
	if result.RowsAffected == 0 {
		msg = "No online agents to stop (fleet already idle)"
	}
	respond(c, gin.H{
		"message":       msg,
		"scope":         scope,
		"agents_killed": result.RowsAffected,
	})
}

// --- Fleet kill-switch broadcast ---

// killSwitchState returns the cached armed flag and token (beacon hot path).
func (s *Server) killSwitchState() (bool, string) {
	s.killSwitchMu.RLock()
	defer s.killSwitchMu.RUnlock()
	return s.killSwitchArmed, s.killSwitchToken
}

// reloadKillSwitchState refreshes the in-memory cache from the singleton DB
// row. Called at server startup and after every arm/disarm.
func (s *Server) reloadKillSwitchState() {
	var ks db.KillSwitch
	if err := s.db.First(&ks, 1).Error; err != nil {
		s.killSwitchMu.Lock()
		s.killSwitchArmed = false
		s.killSwitchToken = ""
		s.killSwitchMu.Unlock()
		return
	}
	s.killSwitchMu.Lock()
	s.killSwitchArmed = ks.Armed
	s.killSwitchToken = ks.Token
	s.killSwitchMu.Unlock()
}

// setKillSwitch persists the kill-switch state and refreshes the cache.
func (s *Server) setKillSwitch(armed bool, token, operator string) {
	now := time.Now()
	var ks db.KillSwitch
	err := s.db.First(&ks, 1).Error
	if err != nil {
		ks = db.KillSwitch{ID: 1}
	}
	ks.Armed = armed
	ks.Token = token
	ks.UpdatedAt = now
	if armed {
		ks.TriggeredAt = &now
		ks.TriggeredBy = operator
		ks.DisarmedAt = nil
		ks.DisarmedBy = ""
	} else {
		ks.DisarmedAt = &now
		ks.DisarmedBy = operator
	}
	if err := s.db.Save(&ks).Error; err != nil {
		slog.Error("Kill switch: failed to persist state", "armed", armed, "err", err)
	}
	s.reloadKillSwitchState()
}

// handleKillSwitch arms or disarms the fleet kill-switch. Arming requires an
// operator password confirmation (like emergency-stop), broadcasts uninstall
// tasks to every registered implant immediately, and attaches the armed token
// to every subsequent beacon response so offline, sleeping, and newly
// registered implants self-destruct when they next check in.
func (s *Server) handleKillSwitch(c *gin.Context) {
	var req struct {
		Action   string `json:"action" binding:"required"` // "arm" | "disarm"
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "Invalid request")
		return
	}

	// Resolve the caller first: First(&user, any) with a nil primary key
	// matched the first row in the table, so a context without user_id
	// confirmed the password against an arbitrary account and then armed the
	// kill switch. Unknown principals are refused outright.
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	var user db.User
	if err := s.db.First(&user, uid).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "User not found")
		return
	}
	if !middleware.CheckPassword(user.PasswordHash, req.Password) {
		respondError(c, http.StatusUnauthorized, "Invalid password")
		return
	}

	switch req.Action {
	case "arm":
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			respondError(c, http.StatusInternalServerError, "Failed to generate kill-switch token")
			return
		}
		token := hex.EncodeToString(tokenBytes)
		s.setKillSwitch(true, token, user.Username)

		// Dispatch self-destruct (uninstall) tasks to the whole fleet so
		// online implants react immediately; the beacon-level token covers
		// everyone else (offline, sleeping, future registrations).
		var agents []db.Implant
		dispatched, skipped := 0, 0
		if err := s.db.Select("id", "tenant_id", "public_ip", "ip").Find(&agents).Error; err == nil {
			dispatched, skipped = s.dispatchFleetUninstall(agents)
		}
		s.LogAuditRecord(c, "kill_switch_arm", "security", user.Username,
			"Fleet kill-switch ARMED ("+strconv.Itoa(dispatched)+" uninstall tasks dispatched)", true, nil)
		s.LogEmergencyAction(c, "KILL SWITCH ARMED", dispatched)
		s.broadcastSystemAlert("KILL SWITCH ARMED",
			"Fleet kill-switch armed by "+user.Username+". All implants will self-destruct on next beacon.", "kill_switch")
		slog.Warn("KILL SWITCH ARMED",
			"user", user.Username,
			"uninstall_tasks_dispatched", dispatched,
			"agents_skipped_at_capacity", skipped)
		respond(c, gin.H{
			"message":          "Kill switch armed. All implants will self-destruct on next beacon.",
			"armed":            true,
			"tasks_dispatched": dispatched,
		})
	case "disarm":
		s.setKillSwitch(false, "", user.Username)

		// Reclaim uninstall tasks that were dispatched at arm time but never
		// executed. An "armed then disarmed" kill switch is a false alarm in
		// many ops; those pending/failed uninstall jobs must not fire hours
		// later when the affected implants wake up. (An implant that already
		// obtained the task before disarm cannot be recalled — that is
		// inherent to a one-shot kill switch, not a recoverable state.)
		reclaimed := 0
		var queued []db.Task
		if err := s.db.Where("type = ? AND status IN ?", "uninstall", []string{"pending", "running"}).Find(&queued).Error; err != nil {
			slog.Warn("Kill switch disarm: failed to query queued uninstall tasks", "err", err)
		} else if len(queued) > 0 {
			// Per-row conditional flip: a task that completed between the
			// SELECT and the UPDATE must keep its terminal state, and its
			// slot must not be released twice (the completion path already
			// decremented it).
			for _, t := range queued {
				res := s.db.Model(&db.Task{}).
					Where("id = ? AND status IN ?", t.ID, []string{"pending", "running"}).
					Update("status", "cancelled")
				if res.Error != nil {
					slog.Warn("Kill switch disarm: failed to cancel uninstall task", "task_id", t.ID, "err", res.Error)
					continue
				}
				if res.RowsAffected == 0 {
					continue // already terminal; slot already accounted for
				}
				reclaimed++
				s.decPendingTasks(t.AgentID)
			}
		}

		s.LogAuditRecord(c, "kill_switch_disarm", "security", user.Username,
			"Fleet kill-switch disarmed ("+strconv.Itoa(reclaimed)+" uninstall tasks reclaimed)", true, nil)
		s.broadcastSystemAlert("KILL SWITCH DISARMED",
			"Fleet kill-switch disarmed by "+user.Username+".", "kill_switch")
		slog.Warn("KILL SWITCH DISARMED", "user", user.Username, "uninstall_tasks_reclaimed", reclaimed)
		respond(c, gin.H{
			"message":   "Kill switch has been disarmed.",
			"armed":     false,
			"reclaimed": reclaimed,
		})
	default:
		respondError(c, http.StatusBadRequest, "action must be 'arm' or 'disarm'")
	}
}

// dispatchFleetUninstall queues one self-destruct task per implant when the
// kill switch arms. It replaces a per-agent createTask loop that issued three
// point SELECTs plus one INSERT per implant — at fleet scale thousands of
// round trips on the single-writer pool, measured at ~360ms for 1000 agents
// (BenchmarkKillSwitchArmFleet) at the exact moment the operator needs the
// fleet gone.
//
// The gates are kept identical to createTask(nil, id, "uninstall", ...): every
// agent still passes the RoE and adaptive-opsec checks, evaluated against the
// rows the caller loaded (which is why checkRoELoaded exists), and the
// per-agent pending ceiling still applies. The collaboration soft-lock check
// is intentionally not replicated: on the system path (callerUserID == 0) it
// short-circuits to nil after a per-agent SELECT, so skipping it is
// behavior-preserving, not a relaxation.
//
// Tasks are committed in one transaction and the in-memory counters are
// released together on failure, so a partial insert can never leave durably
// queued rows that are uncounted (a later disarm must be able to reclaim
// exactly what arm queued).
//
// Agents skipped at the ceiling are still covered by the beacon-level kill
// token, so no implant escapes the kill switch.
func (s *Server) dispatchFleetUninstall(agents []db.Implant) (dispatched, skippedAtCapacity int) {
	if len(agents) == 0 {
		return 0, 0
	}

	status := s.resolveInitialTaskStatus("uninstall")
	var approvalExpiresAt *time.Time
	if status == TaskStatusPendingApproval {
		exp := time.Now().Add(ApprovalExpiryDuration)
		approvalExpiresAt = &exp
	}

	// Phase 1 (no lock): validation only. Held outside the pending-tasks
	// mutex because a blocked agent writes an audit record, and audit I/O
	// must not serialize task accounting for the rest of the fleet.
	eligible := make([]db.Implant, 0, len(agents))
	for _, a := range agents {
		if err := s.validateTaskCreationWithAgent(nil, a.ID, "uninstall", "", "", "", 0, &a); err != nil {
			continue
		}
		eligible = append(eligible, a)
	}

	// Phase 2 (locked, memory-only): capacity checks and counters, same
	// section createTask runs under the same mutex.
	tasks := make([]db.Task, 0, len(eligible))
	s.agentPendingTasksMu.Lock()
	for _, a := range eligible {
		if s.agentPendingTasks[a.ID] >= MaxPendingTasksPerAgent {
			skippedAtCapacity++
			continue
		}
		s.agentPendingTasks[a.ID]++
		tasks = append(tasks, db.Task{
			AgentID:           a.ID,
			Type:              "uninstall",
			Status:            status,
			TenantID:          a.TenantID,
			ApprovalExpiresAt: approvalExpiresAt,
		})
	}
	s.agentPendingTasksMu.Unlock()

	if len(tasks) == 0 {
		return 0, skippedAtCapacity
	}

	// All-or-nothing: CreateInBatches commits per chunk, so without the
	// transaction a mid-way failure would leave earlier chunks durably
	// queued while the rollback below releases every counter.
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(tasks, 100).Error
	}); err != nil {
		for i := range tasks {
			s.decPendingTasks(tasks[i].AgentID)
		}
		slog.Error("Kill switch: failed to queue fleet uninstall tasks",
			"err", err, "count", len(tasks))
		return 0, skippedAtCapacity
	}

	// Same per-task bookkeeping createTask runs after a successful INSERT.
	// The per-task task_updated broadcast used by the batch endpoint is not
	// fired here: the arm path never sent one before, and the system-wide
	// alert already announced the arm to every operator.
	for i := range tasks {
		if s.pluginManager != nil {
			s.fireTaskCreatedHook(tasks[i].AgentID, tasks[i].ID, tasks[i].Type, tasks[i].Command)
		}
		if s.metrics != nil {
			s.metrics.TasksTotal.Inc()
		}
	}
	return len(tasks), skippedAtCapacity
}

// handleKillSwitchStatus reports the current kill-switch state. The token
// itself is never exposed: it only authenticates the per-implant broadcast.
func (s *Server) handleKillSwitchStatus(c *gin.Context) {
	var ks db.KillSwitch
	status := gin.H{"success": true, "armed": false}
	if err := s.db.First(&ks, 1).Error; err == nil {
		status = gin.H{
			"success":      true,
			"armed":        ks.Armed,
			"triggered_at": ks.TriggeredAt,
			"triggered_by": ks.TriggeredBy,
			"disarmed_at":  ks.DisarmedAt,
			"disarmed_by":  ks.DisarmedBy,
		}
	}
	respond(c, status)
}

func (s *Server) handleEmergencyStatus(c *gin.Context) {
	// Never fall through with zeros here: this is the panel an operator reads
	// while deciding whether the fleet is down, and a failed count would look
	// exactly like "nothing is online / nothing is pending".
	var onlineCount int64
	if err := s.db.Model(&db.Implant{}).Where("status != ?", "offline").Count(&onlineCount).Error; err != nil {
		slog.Error("Failed to count online agents", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to count online agents")
		return
	}

	var pendingKill int64
	if err := s.db.Model(&db.Task{}).Where("type = ? AND status IN ?", "kill", []string{"pending", "running"}).Count(&pendingKill).Error; err != nil {
		slog.Error("Failed to count pending kills", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to count pending kills")
		return
	}

	respond(c, gin.H{
		"online_agents": onlineCount,
		"pending_kills": pendingKill,
	})
}
