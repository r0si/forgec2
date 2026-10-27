package server

import (
	"path/filepath"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"log/slog"
)

// uploadsRootDir resolves <data_dir>/uploads and refuses to guess.
//
// Same defect class as the hosted payloads: rawDataDir() returns "" when
// server.data_dir is empty, and filepath.Join("", "uploads") is the RELATIVE path
// "uploads" - so agent-exfiltrated files, which are writes, landed next to whatever
// directory the process started in. A misconfigured deployment would then have its
// exfiltrated loot scattered in a working directory nobody backs up or cleans.
func (s *Server) uploadsRootDir() (string, error) {
	dir := s.rawDataDir()
	if dir == "" {
		return "", errNoDataDir
	}
	return filepath.Join(dir, "uploads"), nil
}

// failUploadTask records an error on a chunk task and returns true (the caller's
// "handled" convention), so a refused write is reported to the operator instead of
// silently completing.
func (s *Server) failUploadTask(c *gin.Context, task *db.Task, msg string) bool {
	slog.Error("Refused to write a file chunk", "task_id", task.ID, "reason", msg)
	task.Result = "ERROR: " + msg
	_ = s.encryptTaskFieldsOrBlank(task)
	if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"result": task.Result,
		"error":  task.Error,
	}).Error; err != nil {
		slog.Error("Failed to save chunk refusal", "task_id", task.ID, "error", err)
	}
	return true
}
