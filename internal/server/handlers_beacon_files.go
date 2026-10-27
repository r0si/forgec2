package server

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/forgec2/forgec2/internal/db"
)

// saveFileChunk writes a base64-encoded file chunk to disk for upload/download
// results. Returns true if the caller should skip normal task-result processing.
func saveFileChunk(s *Server, uuid string, task *db.Task, r taskResult, logPrefix string, resultPrefix string) bool {
	uploadBase := safeJoin(filepath.Join(s.rawDataDir(), "uploads"), uuid)
	if uploadBase == "" {
		slog.Error("Invalid agent ID for upload path", "agent_id", uuid)
		task.Result = "ERROR: invalid agent id"
		_ = s.encryptTaskFieldsOrBlank(task)
		if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; err != nil {
			slog.Error("Failed to save invalid agent id error", "task_id", task.ID, "error", err)
		}
		return true
	}
	if err := os.MkdirAll(uploadBase, 0700); err != nil {
		slog.Error("Failed to create uploads dir", "agent_id", uuid, "error", err)
	}
	filename := r.Filename
	if filename == "" {
		filename = fmt.Sprintf("file_%d", task.ID)
	}
	filePath := safeJoin(uploadBase, filename)
	if filePath == "" {
		task.Result = "ERROR: invalid filename (path traversal blocked)"
		_ = s.encryptTaskFieldsOrBlank(task)
		if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; err != nil {
			slog.Error("Failed to save file path traversal error", "task_id", task.ID, "error", err)
		}
		return true
	}
	decoded, err := base64.StdEncoding.DecodeString(r.Output)
	if err != nil {
		task.Result = fmt.Sprintf("ERROR: base64 decode failed: %v", err)
		if len(task.Result) > MaxResultSize {
			task.Result = truncateString(task.Result, MaxResultSize)
		}
		_ = s.encryptTaskFieldsOrBlank(task)
		if saveErr := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; saveErr != nil {
			slog.Error("Failed to save decode error", "task_id", task.ID, "error", saveErr)
		}
		return true
	}
	if len(decoded) > MaxTransferChunkSize {
		task.Result = fmt.Sprintf("ERROR: chunk too large (%d bytes, max %d)", len(decoded), MaxTransferChunkSize)
		_ = s.encryptTaskFieldsOrBlank(task)
		if saveErr := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; saveErr != nil {
			slog.Error("Failed to save chunk size error", "task_id", task.ID, "error", saveErr)
		}
		slog.Warn("Oversized exfil chunk rejected", "agent_id", uuid, "task_id", task.ID, "size", len(decoded))
		return true
	}
	// Remember the link this chunk is about to replace: every local failure below
	// (invalid offset, open/seek/write error) rolls it back, so a transient disk problem
	// cannot advance the chain and poison the rest of the upload.
	chainPrev := s.chainLinkFor(task.ID)
	if err := s.verifyAndCommitChain(uuid, task.ID, r.MAC, decoded); err != nil {
		task.Result = fmt.Sprintf("ERROR: %v", err)
		_ = s.encryptTaskFieldsOrBlank(task)
		if saveErr := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; saveErr != nil {
			slog.Error("Failed to save integrity error", "task_id", task.ID, "error", saveErr)
		}
		slog.Warn("File chunk integrity failure", "agent_id", uuid, "task_id", task.ID, "error", err)
		return true
	}
	off := r.Offset
	if off == 0 {
		off = task.Offset
	}
	// Offset validation (agent-controlled): a negative value skipped both the
	// O_TRUNC branch and the Seek, silently corrupting chunk 0; an astronomic
	// value made Seek+Write zero-fill terabytes of teamserver disk. Bound it
	// against the declared transfer size with a sane hard cap.
	const maxChunkOffset = int64(8) << 30 // 8 GiB
	if off < 0 || off > maxChunkOffset {
		task.Result = fmt.Sprintf("ERROR: invalid chunk offset %d", off)
		_ = s.encryptTaskFieldsOrBlank(task)
		if saveErr := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; saveErr != nil {
			slog.Error("Failed to save offset error", "task_id", task.ID, "error", saveErr)
		}
		slog.Warn("File chunk rejected: invalid offset", "agent_id", uuid, "task_id", task.ID, "offset", off)
		s.rollbackChainLink(task.ID, chainPrev)
		return true
	}
	openFlags := os.O_CREATE | os.O_WRONLY
	if off == 0 {
		openFlags |= os.O_TRUNC
	}
	f, ferr := os.OpenFile(filePath, openFlags, 0600)
	if ferr != nil {
		task.Result = fmt.Sprintf("ERROR: open file failed: %v", ferr)
		if len(task.Result) > MaxResultSize {
			task.Result = truncateString(task.Result, MaxResultSize)
		}
		_ = s.encryptTaskFieldsOrBlank(task)
		if saveErr := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; saveErr != nil {
			slog.Error("Failed to save open file error", "task_id", task.ID, "error", saveErr)
		}
		s.rollbackChainLink(task.ID, chainPrev)
		return true
	}
	defer f.Close()
	if off > 0 {
		if _, err := f.Seek(off, 0); err != nil {
			task.Result = fmt.Sprintf("ERROR: seek failed: %v", err)
			if len(task.Result) > MaxResultSize {
				task.Result = truncateString(task.Result, MaxResultSize)
			}
			_ = s.encryptTaskFieldsOrBlank(task)
			if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
				"result": task.Result,
				"error":  task.Error,
			}).Error; err != nil {
				slog.Error("Failed to save "+logPrefix+" seek error", "task_id", task.ID, "error", err)
			}
			s.rollbackChainLink(task.ID, chainPrev)
			return true
		}
	}
	if _, err := f.Write(decoded); err != nil {
		task.Result = fmt.Sprintf("ERROR: write failed: %v", err)
		if len(task.Result) > MaxResultSize {
			task.Result = truncateString(task.Result, MaxResultSize)
		}
		_ = s.encryptTaskFieldsOrBlank(task)
		if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"result": task.Result,
			"error":  task.Error,
		}).Error; err != nil {
			slog.Error("Failed to save "+logPrefix+" write error", "task_id", task.ID, "error", err)
		}
		s.rollbackChainLink(task.ID, chainPrev)
		return true
	}
	task.Result = fmt.Sprintf("%s: %s offset %d (%d bytes)", resultPrefix, filename, off, r.Size)
	if len(task.Result) > MaxResultSize {
		task.Result = truncateString(task.Result, MaxResultSize)
	}
	_ = s.encryptTaskFieldsOrBlank(task)
	if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"result": task.Result,
		"error":  task.Error,
	}).Error; err != nil {
		slog.Error("Failed to save "+logPrefix+" success", "task_id", task.ID, "error", err)
	}
	slog.Info("File chunk "+logPrefix, "agent_id", uuid, "file", filename, "offset", off, "size", r.Size)
	return false
}
