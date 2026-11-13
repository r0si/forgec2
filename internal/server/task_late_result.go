package server

import (
	"log/slog"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// captureLateResult preserves a result that arrived after the operator
// cancelled the task. The status deliberately stays "cancelled": the result is
// evidence about what the agent did before the abort landed, not a resurrection
// of the task. File/screen results are skipped because writing them after a
// cancellation would be a side effect the operator explicitly stopped.
func (s *Server) captureLateResult(task *db.Task, r taskResult, agentID string, now time.Time) {
	if r.Partial || task.LateResult {
		return
	}
	switch r.Type {
	case "upload", "download", "screenshot", "screenshot_window", "screenshot_chunk",
		"screen_frame", "screen_stream_error", "screen_stream_start", "screen_stream_stop",
		"screen_trigger", "file_thumb", "webcam", "mic":
		return
	}
	if r.Output == "" && r.Error == "" {
		return
	}

	status := "completed"
	result := decodeTaskResult(r.Type, r.Encoding, r.Output)
	if r.Error != "" {
		status = "failed"
		result = "ERROR: " + r.Error
	}
	if len(result) > MaxResultSize {
		result = truncateString(result, MaxResultSize)
	}
	task.LateResult = true
	task.LateResultStatus = status
	task.LateResultAt = &now
	task.Result = result
	// Deliberately keep task.Error ("cancelled by operator"): the late result
	// is evidence, not a replacement for the reason the task is terminal. A
	// late failure is visible through LateResultStatus and the ERROR result.
	if r.ResultID != "" {
		task.LastResultID = r.ResultID
	}

	dbTask := *task
	if err := s.encryptTaskFieldsOrBlank(&dbTask); err != nil {
		slog.Error("Failed to encrypt late task result", "agent_id", agentID, "task_id", task.ID, "error", err)
		return
	}
	if err := s.db.Model(&db.Task{}).
		Where("id = ? AND status = ?", task.ID, "cancelled").
		Updates(map[string]interface{}{
			"result":             dbTask.Result,
			"error":              dbTask.Error,
			"last_result_id":     task.LastResultID,
			"late_result":        true,
			"late_result_status": status,
			"late_result_at":     now,
			"updated_at":         now,
		}).Error; err != nil {
		slog.Error("Failed to persist late task result", "agent_id", agentID, "task_id", task.ID, "error", err)
		return
	}
	slog.Info("Preserved a result that arrived after cancellation", "agent_id", agentID, "task_id", task.ID, "status", status)
	s.broadcastTaskUpdate(agentID, *task)
}
