package server

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) handleBOFPage(c *gin.Context) {
	stats := s.getNavStats(c)

	scope, ok := s.bofScope(c)
	if !ok {
		respond(c, gin.H{"bofs": []gin.H{}})
		return
	}
	var bofs []db.BOFFile
	if err := scope.Order("created_at desc").Limit(500).Find(&bofs).Error; err != nil {
		handleQueryError(c, err, "Failed to list BOFs")
		return
	}

	data := gin.H{
		"Title":     "ForgeC2 - BOF Manager",
		"ActiveNav": "bof",
		"BOFFiles":  bofs,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleBOFUpload(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		respondError(c, http.StatusBadRequest, "BOF file (.o) required")
		return
	}

	if file.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("BOF too large: %d bytes (max %d)", file.Size, MaxUploadSize))
		return
	}

	f, err := file.Open()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read BOF file")
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read BOF data")
		return
	}

	// Same gates the command-path BOF uploader enforces: extension allowlist
	// plus magic-byte rejection. The library upload previously accepted ANY
	// bytes (including PE/MZ binaries) that were later shipped to implants.
	//
	// The first argument is the allowlist KEY, not the filename: passing
	// (file.Filename, ".o,.obo") looked up the extension map under the
	// uploaded name, fell through to the generic text allowlist and rejected
	// every real upload.
	if err := validateUploadExtension("bof", file.Filename); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "BOF operation"))
		return
	}
	if err := validateUploadMagicBytes("bof", file.Filename, data); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "BOF operation"))
		return
	}

	desc := c.PostForm("description")

	bof := db.BOFFile{
		Name:        file.Filename,
		Data:        data,
		Size:        file.Size,
		Description: desc,
		CreatedBy:   c.GetString("user"),
	}

	if err := s.db.Create(&bof).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "BOF operation"))
		return
	}

	slog.Info("BOF uploaded", "name", file.Filename, "size", file.Size, "user", bof.CreatedBy)
	s.LogAuditRecord(c, "bof_upload", "bof", "", fmt.Sprintf("BOF: %s (%d bytes)", file.Filename, file.Size), true, nil)
	respond(c, gin.H{"bof": bof})
}

// bofScope restricts BOF blobs to the caller's tenant. bof_files has no tenant column, so
// ownership runs through the uploader (created_by), exactly like scripts. Without it ANY operator
// could list, download, edit, delete and RUN another tenant's uploaded blob - and a BOF is an
// executable the implant loads.
func (s *Server) bofScope(c *gin.Context) (*gorm.DB, bool) {
	tid, ok := s.resolveTenant(c)
	if !ok {
		return nil, false
	}
	q := s.db.Model(&db.BOFFile{})
	if tid == 0 {
		return q, true
	}
	users := s.db.Model(&db.User{}).Select("username").Where("tenant_id = ?", tid)
	return q.Where("created_by IN (?)", users), true
}

// findVisibleBOF loads a BOF by id within the caller's scope; false answers 404 like a miss.
func (s *Server) findVisibleBOF(c *gin.Context, id string) (db.BOFFile, bool) {
	var bof db.BOFFile
	scope, ok := s.bofScope(c)
	if !ok {
		respondError(c, http.StatusNotFound, "BOF not found")
		return bof, false
	}
	if err := scope.Where("id = ?", id).First(&bof).Error; err != nil {
		respondError(c, http.StatusNotFound, "BOF not found")
		return bof, false
	}
	return bof, true
}

func (s *Server) handleBOFList(c *gin.Context) {
	scope, ok := s.bofScope(c)
	if !ok {
		respond(c, gin.H{"bofs": []gin.H{}})
		return
	}
	var bofs []db.BOFFile
	if err := scope.Order("created_at desc").Limit(500).Find(&bofs).Error; err != nil {
		handleQueryError(c, err, "Failed to list BOFs")
		return
	}

	results := make([]gin.H, 0, len(bofs))
	for _, b := range bofs {
		results = append(results, gin.H{
			"id":          b.ID,
			"name":        b.Name,
			"size":        b.Size,
			"description": b.Description,
			"created_by":  b.CreatedBy,
			"created_at":  b.CreatedAt,
		})
	}
	respond(c, gin.H{"bofs": results})
}

func (s *Server) handleBOFDelete(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	bofID := c.Param("id")

	bof, ok := s.findVisibleBOF(c, bofID)
	if !ok {
		return
	}

	if err := s.db.Delete(&bof).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete BOF")
		return
	}
	slog.Info("BOF deleted", "name", bof.Name, "id", bofID)
	s.LogAuditRecord(c, "bof_delete", "bof", "", fmt.Sprintf("Deleted BOF: %s", bof.Name), true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleBOFDownload(c *gin.Context) {
	bofID := c.Param("id")

	bof, ok := s.findVisibleBOF(c, bofID)
	if !ok {
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, bof.Name))
	c.Data(http.StatusOK, "application/octet-stream", bof.Data)
}

func (s *Server) handleBOFRun(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	bofID := c.Param("id")
	agentID := c.PostForm("agent_id")
	args := c.PostForm("args")

	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}

	bof, ok := s.findVisibleBOF(c, bofID)
	if !ok {
		return
	}

	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}

	b64Data := base64.StdEncoding.EncodeToString(bof.Data)

	task, err := s.createTask(agentID, "bof", args, "", "", b64Data, 0, 0)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create task")
		return
	}

	slog.Info("BOF dispatched from library", "agent_id", agentID, "bof", bof.Name, "args", truncateString(args, 100))
	s.LogAuditRecord(c, "bof_run", "agent", agentID, fmt.Sprintf("BOF: %s args=%s", bof.Name, args), true, nil)
	s.dispatchTask(c, task, "bof", fmt.Sprintf("BOF: %s args=%s", bof.Name, args))
}

func (s *Server) handleBOFQuickRun(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	file, err := c.FormFile("bof")
	if err != nil {
		respondError(c, http.StatusBadRequest, "bof file (.o) required")
		return
	}
	if file.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("BOF too large: %d bytes (max %d)", file.Size, MaxUploadSize))
		return
	}
	f, err := file.Open()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read BOF file")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read BOF data")
		return
	}
	args := c.PostForm("args")
	b64Data := base64.StdEncoding.EncodeToString(data)

	task := s.issueAgentTask(c, id, TaskSpec{Type: "bof", Command: args, Data: b64Data})
	if task == nil {
		return
	}
	slog.Info("BOF quick execute", "agent_id", id, "file", file.Filename, "size", len(data), "args", truncateString(args, 100))
	s.LogAuditRecord(c, "bof_quick", "agent", id, fmt.Sprintf("BOF quick: %s (%d bytes) args=%s", file.Filename, len(data), args), true, nil)
	respond(c, gin.H{"task_id": task.ID, "message": fmt.Sprintf("BOF %s dispatched", file.Filename)})
}

func (s *Server) handleBOFRecentResults(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "20")
	var limit int
	if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil || limit < 1 || limit > MaxBOFResultLimit {
		limit = 20
	}

	var tasks []db.Task
	if err := s.db.Where("type = ?", "bof").Preload("Agent").Order("created_at desc").Limit(limit).Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to list BOF results")
		return
	}

	results := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		elapsed := ""
		if !t.CreatedAt.IsZero() && t.UpdatedAt.After(t.CreatedAt) {
			d := t.UpdatedAt.Sub(t.CreatedAt)
			if d < time.Minute {
				elapsed = fmt.Sprintf("%ds", int(d.Seconds()))
			} else {
				elapsed = fmt.Sprintf("%dm", int(d.Minutes()))
			}
		}
		results = append(results, gin.H{
			"id":         t.ID,
			"agent_id":   t.AgentID,
			"agent_name": t.Agent.Hostname,
			"args":       t.Command,
			"result":     t.Result,
			"error":      t.Error,
			"status":     t.Status,
			"created_at": t.CreatedAt,
			"elapsed":    elapsed,
		})
	}
	respond(c, gin.H{"results": results})
}

func (s *Server) handleBOFEdit(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	bofID := c.Param("id")

	bof, ok := s.findVisibleBOF(c, bofID)
	if !ok {
		return
	}

	desc := c.PostForm("description")
	if desc != "" {
		bof.Description = desc
	}
	if name := c.PostForm("name"); name != "" {
		bof.Name = name
	}

	if err := s.db.Save(&bof).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update BOF")
		return
	}

	s.LogAuditRecord(c, "bof_edit", "bof", "", fmt.Sprintf("Edited BOF: %s", bof.Name), true, nil)
	respond(c, gin.H{"bof": bof})
}
