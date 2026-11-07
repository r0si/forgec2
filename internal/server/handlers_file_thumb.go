package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
)

const (
	fileThumbMaxItems      = 12
	fileThumbMaxPathLength = 4096
	fileThumbMaxBytes      = 512 * 1024
	fileThumbTTL           = 7 * 24 * time.Hour
	fileThumbMaxWireResult = 2 * MaxResultSize
	fileThumbCacheDirName  = "thumbs"
	fileThumbContentType   = "image/jpeg"
	fileThumbIDHexLength   = sha256.Size * 2
)

type fileThumbRequest struct {
	Paths []string `json:"paths" form:"-"`
	Path  string   `json:"path" form:"path"`
}

type fileThumbWireItem struct {
	Path       string `json:"path"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	MIME       string `json:"mime,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	SourceSize int64  `json:"source_size,omitempty"`
	Modified   string `json:"modified,omitempty"`
	ThumbID    string `json:"thumb_id,omitempty"`
	Data       string `json:"data,omitempty"`
}

type fileThumbWireResult struct {
	Items []fileThumbWireItem `json:"items"`
}

// handleFileThumb queues a bounded batch thumbnail task. The agent returns
// base64 JPEGs once; storeFileThumbs writes them under data/thumbs and rewrites
// the task result to metadata plus an opaque thumb id, so task rows never
// become a second unbounded copy of the image cache.
func (s *Server) handleFileThumb(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")

	var req fileThumbRequest
	bindJSONOrForm(c, &req)
	paths := req.Paths
	if len(paths) == 0 {
		raw := strings.TrimSpace(c.PostForm("paths"))
		if raw != "" {
			if strings.HasPrefix(raw, "[") {
				_ = json.Unmarshal([]byte(raw), &paths)
			} else {
				paths = strings.Split(raw, "\n")
			}
		}
	}
	if len(paths) == 0 && strings.TrimSpace(req.Path) != "" {
		paths = []string{req.Path}
	}
	if len(paths) == 0 && strings.TrimSpace(c.PostForm("path")) != "" {
		paths = []string{c.PostForm("path")}
	}

	normalized := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if len(path) > fileThumbMaxPathLength {
			respondError(c, http.StatusBadRequest, fmt.Sprintf("thumbnail path too long (max %d characters)", fileThumbMaxPathLength))
			return
		}
		if strings.ContainsRune(path, '\x00') {
			respondError(c, http.StatusBadRequest, "thumbnail path contains a NUL byte")
			return
		}
		key := strings.ToLower(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, path)
	}
	if len(normalized) == 0 {
		respondError(c, http.StatusBadRequest, "at least one thumbnail path required")
		return
	}
	if len(normalized) > fileThumbMaxItems {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("too many thumbnail paths (max %d per request)", fileThumbMaxItems))
		return
	}

	data, err := json.Marshal(map[string]any{"paths": normalized})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not encode thumbnail options")
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{
		Type:    protocol.TaskTypeFileThumb,
		Command: fmt.Sprintf("%d file(s)", len(normalized)),
		Data:    string(data),
	})
	if task == nil {
		return
	}

	slog.Info("File thumbnails requested", "agent_id", id, "paths", len(normalized))
	details := fmt.Sprintf("paths=%d first=%s", len(normalized), truncateString(normalized[0], 512))
	s.dispatchTask(c, task, "file_thumb", details)
}

// handleFileThumbGet serves a cached thumbnail with the usual operator/tenant
// checks. gin's c.File path goes through http.ServeContent, so the browser can
// cache and revalidate these small images without another agent round-trip.
func (s *Server) handleFileThumbGet(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	thumbID := strings.ToLower(strings.TrimSpace(c.Param("thumb")))
	if !validFileThumbID(thumbID) {
		respondError(c, http.StatusBadRequest, "invalid thumbnail id")
		return
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	base := s.fileThumbBase(id)
	target := safeJoin(base, thumbID+".jpg")
	if base == "" || target == "" {
		respondError(c, http.StatusNotFound, "thumbnail not found")
		return
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() || info.Size() == 0 {
		respondError(c, http.StatusNotFound, "thumbnail not found")
		return
	}
	c.Header("Cache-Control", "private, max-age=86400")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Type", fileThumbContentType)
	serveFileSafe(c, target, base, "")
}

func validFileThumbID(id string) bool {
	if len(id) != fileThumbIDHexLength {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) fileThumbBase(agentID string) string {
	s.configMu.RLock()
	dataDir := s.cfg.Server.DataDir
	s.configMu.RUnlock()
	if dataDir == "" {
		dataDir = "data"
	}
	return safeJoin(filepath.Join(dataDir, fileThumbCacheDirName), agentID)
}

// storeFileThumbs converts one file_thumb task result into cache metadata. It
// is deliberately best-effort per item: one bad image must not fail the whole
// batch, and no base64 is ever persisted in the task row.
func (s *Server) storeFileThumbs(agentID, raw string) string {
	var doc fileThumbWireResult
	if len(raw) > fileThumbMaxWireResult || json.Unmarshal([]byte(raw), &doc) != nil {
		return `{"items":[],"error":"invalid thumbnail result"}`
	}
	for i := range doc.Items {
		item := &doc.Items[i]
		if !item.OK {
			item.Data = ""
			continue
		}
		if item.Data == "" {
			item.OK = false
			item.Error = "missing thumbnail data"
			continue
		}
		data, err := base64.StdEncoding.DecodeString(item.Data)
		item.Data = ""
		if err != nil {
			item.OK = false
			item.Error = "invalid thumbnail encoding"
			continue
		}
		if len(data) == 0 || len(data) > fileThumbMaxBytes {
			item.OK = false
			item.Error = "thumbnail size out of bounds"
			continue
		}
		if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF {
			item.OK = false
			item.Error = "thumbnail is not JPEG"
			continue
		}
		thumbID, err := s.saveFileThumb(agentID, data)
		if err != nil {
			slog.Error("Failed to cache thumbnail", "agent_id", agentID, "error", err)
			item.OK = false
			item.Error = "could not cache thumbnail"
			continue
		}
		item.OK = true
		item.MIME = fileThumbContentType
		item.ThumbID = thumbID
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return `{"items":[],"error":"could not encode thumbnail metadata"}`
	}
	return string(out)
}

func (s *Server) saveFileThumb(agentID string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	thumbID := hex.EncodeToString(sum[:])
	base := s.fileThumbBase(agentID)
	if base == "" {
		return "", fmt.Errorf("invalid agent id")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	target := safeJoin(base, thumbID+".jpg")
	if target == "" {
		return "", fmt.Errorf("invalid thumbnail path")
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() == int64(len(data)) {
		return thumbID, nil
	}
	tmp, err := os.CreateTemp(base, ".thumb-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, target); err != nil {
		// A concurrent batch may have written the identical content-addressed
		// file first. Treat that as a cache hit rather than failing the item.
		if _, statErr := os.Stat(target); statErr == nil {
			return thumbID, nil
		}
		return "", err
	}
	return thumbID, nil
}

// cleanupOldThumbs bounds the thumbnail cache by age. It is called by the same
// periodic cleanup loop as hosted payloads; a failed walk is best-effort.
func (s *Server) cleanupOldThumbs() {
	dataDir := s.rawDataDir()
	base := filepath.Join(dataDir, fileThumbCacheDirName)
	if dataDir == "" {
		base = filepath.Join("data", fileThumbCacheDirName)
	}
	cutoff := time.Now().Add(-fileThumbTTL)
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		// #nosec G122 -- stale-thumbnail cleanup over the data dir; the walk is cleanup, not trust.
		_ = os.Remove(path) // #nosec G122
		return nil
	})
}
