package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const bloodHoundDir = "data/bloodhound"

// handleBloodHoundList returns all collected BloodHound results.
func (s *Server) handleBloodHoundList(c *gin.Context) {
	var results []db.BloodHoundResult
	if err := s.db.Order("created_at desc").Limit(BloodHoundResultLimit).Find(&results).Error; err != nil {
		// An empty 200 here reads as "nothing collected yet".
		handleQueryError(c, err, "Failed to list bloodhound results")
		return
	}
	respond(c, gin.H{"results": results, "total": len(results)})
}

// handleBloodHoundStatus returns a summary of BloodHound collection state.
func (s *Server) handleBloodHoundStatus(c *gin.Context) {
	var total int64
	if err := s.db.Model(&db.BloodHoundResult{}).Count(&total).Error; err != nil {
		// A zero count here reads as "no collections ever": a failed
		// count must not impersonate a clean bill of health.
		handleQueryError(c, err, "Failed to count bloodhound results")
		return
	}

	var last db.BloodHoundResult
	if err := s.db.Order("created_at desc").First(&last).Error; err != nil {
		// Not-found is the legitimate "no collections yet" state; only a
		// real failure is an error.
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			handleQueryError(c, err, "Failed to fetch last bloodhound result")
			return
		}
	}

	respond(c, gin.H{
		"total_collections": total,
		"last_collection":   last.CreatedAt,
		"last_agent_id":     last.AgentID,
	})
}

// handleBloodHoundCollect dispatches a SharpHound collection task to an agent.
func (s *Server) handleBloodHoundCollect(c *gin.Context) {
	var req struct {
		AgentID string `json:"agent_id"`
		Method  string `json:"method"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AgentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}
	if req.Method == "" {
		req.Method = "Default"
	}

	// agent_id is client-supplied: verify it belongs to the caller before queueing work on it.
	if _, ok := s.getAgentOrFail(c, req.AgentID); !ok {
		return
	}
	task, err := s.createTask(req.AgentID, protocol.TaskTypeSharpHound, req.Method, "", "", "", 0, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Create task"))
		return
	}
	s.dispatchTask(c, task, protocol.TaskTypeSharpHound, "dispatched "+req.Method+" collection")
}

// handleBloodHoundDownload serves the collected ZIP for a result.
func (s *Server) handleBloodHoundDownload(c *gin.Context) {
	id := c.Param("id")
	var result db.BloodHoundResult
	if err := s.db.First(&result, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if result.FilePath == "" {
		respondError(c, http.StatusNotFound, "no file")
		return
	}
	if err := validateFilePath(result.FilePath, bloodHoundDir); err != nil {
		slog.Warn("Bloodhound: path traversal blocked", "path", result.FilePath, "error", err)
		respondError(c, http.StatusForbidden, "invalid file path")
		return
	}
	serveFileSafe(c, result.FilePath, bloodHoundDir, "")
}

// handleBloodHoundDelete removes a collection result and its file.
func (s *Server) handleBloodHoundDelete(c *gin.Context) {
	id := c.Param("id")
	var result db.BloodHoundResult
	if err := s.db.First(&result, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if result.FilePath != "" {
		if err := validateFilePath(result.FilePath, bloodHoundDir); err == nil {
			if err := os.Remove(result.FilePath); err != nil {
				slog.Warn("Failed to remove bloodhound result file", "path", result.FilePath, "err", err)
			}
		}
	}
	if err := s.db.Delete(&result).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete bloodhound result")
		return
	}
	respond(c, gin.H{})
}

// handleBloodHoundUpload accepts a manually uploaded SharpHound ZIP.
func (s *Server) handleBloodHoundUpload(c *gin.Context) {
	agentID := c.PostForm("agent_id")
	data, _, ok := readFileUpload(c, "file")
	if !ok {
		return
	}

	if err := os.MkdirAll(bloodHoundDir, 0o755); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "File operation"))
		return
	}
	stored := filepath.Join(bloodHoundDir, util.NewString()+".zip")
	if err := os.WriteFile(stored, data, 0o600); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "File operation"))
		return
	}

	result := db.BloodHoundResult{
		AgentID:   agentID,
		FilePath:  stored,
		Summary:   c.PostForm("summary"),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&result).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to save bloodhound result")
		return
	}
	respond(c, gin.H{"id": result.ID})
}

// handleBloodHoundResult ingests a collection result POSTed back by an agent.
// Expects multipart: file=<zip>, plus form fields agent_id, task_id,
// collection_method and summary stats.
func (s *Server) handleBloodHoundResult(c *gin.Context) {
	agentID := c.PostForm("agent_id")
	taskID := c.PostForm("task_id")
	method := c.PostForm("collection_method")

	if err := os.MkdirAll(bloodHoundDir, 0o755); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "File operation"))
		return
	}

	result := db.BloodHoundResult{
		AgentID:          agentID,
		CollectionMethod: method,
		Summary:          c.PostForm("summary"),
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if file, err := c.FormFile("file"); err == nil {
		stored := filepath.Join(bloodHoundDir, util.NewString()+".zip")
		if saveErr := c.SaveUploadedFile(file, stored); saveErr == nil {
			result.FilePath = stored
		}
	}

	// Parse simple summary counters if provided (user_count, computer_count, ...).
	for field, dest := range map[string]*int{
		"user_count":         &result.UserCount,
		"computer_count":     &result.ComputerCount,
		"group_count":        &result.GroupCount,
		"session_count":      &result.SessionCount,
		"domain_admin_count": &result.DomainAdminCount,
		"spn_count":          &result.SPNCount,
	} {
		if v := strings.TrimSpace(c.PostForm(field)); v != "" {
			var n int
			if _, e := fmt.Sscanf(v, "%d", &n); e == nil {
				*dest = n
			}
		}
	}

	if err := s.db.Create(&result).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to store bloodhound result")
		return
	}

	// Mark the originating task completed.
	if taskID != "" {
		var task db.Task
		if s.db.First(&task, taskID).Error == nil {
			task.Status = "completed"
			task.Result = "bloodhound collection stored"
			if err := s.db.Save(&task).Error; err != nil {
				slog.Error("Failed to update bloodhound task", "error", err)
			}
			s.broadcastTaskUpdate(task.AgentID, task)
			s.decPendingTasks(task.AgentID)
		}
	}

	respond(c, gin.H{"id": result.ID})
}
