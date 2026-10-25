package server

import (
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *Server) handleRequestScreenshot(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "screenshot"})
	if task == nil {
		return
	}

	slog.Info("Screenshot requested", "agent_id", id)
	s.dispatchTask(c, task, "request_screenshot", "screenshot")
}

// handleGetAgentScreenshot returns the latest screenshot for an agent as base64 JSON.
func (s *Server) handleGetAgentScreenshot(c *gin.Context) {
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	if frame, ok := s.latestScreenFrame(id); ok && s.IsScreenMonitoring(id) {
		respond(c, gin.H{
			"image":       "data:" + frame.mime + ";base64," + frame.data,
			"width":       0,
			"height":      0,
			"window_name": "Desktop",
			"capture_id":  "live-" + strconv.FormatInt(frame.capturedAt.UnixNano(), 10),
			"captured_at": frame.capturedAt.Format(time.RFC3339Nano),
		})
		return
	}

	screenshotBase := filepath.Join(s.rawDataDir(), "screenshots")
	screenshotDir := safeJoin(screenshotBase, id)
	if screenshotDir == "" {
		respondError(c, http.StatusBadRequest, "invalid agent id")
		return
	}
	entries, err := os.ReadDir(screenshotDir)
	if err != nil || len(entries) == 0 {
		respondError(c, http.StatusNotFound, "no screenshot available")
		return
	}

	var newest os.DirEntry
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == nil || info.ModTime().After(newestTime) {
			newest = e
			newestTime = info.ModTime()
		}
	}
	if newest == nil {
		respondError(c, http.StatusNotFound, "no screenshot available")
		return
	}

	fp := filepath.Join(screenshotDir, newest.Name())
	data, err := os.ReadFile(fp)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read screenshot")
		return
	}
	width, height := pngDimensions(data)

	respond(c, gin.H{

		"data": gin.H{
			"image":       "data:image/png;base64," + base64.StdEncoding.EncodeToString(data),
			"width":       width,
			"height":      height,
			"window_name": "Desktop",
			"capture_id":  newest.Name(),
			"captured_at": newestTime.UTC().Format(time.RFC3339Nano),
		},
	})
}

// pngDimensions reads width/height from a PNG IHDR chunk without decoding the full image.
func pngDimensions(data []byte) (int, int) {
	if len(data) < 24 || data[0] != 0x89 || data[1] != 'P' || data[2] != 'N' || data[3] != 'G' {
		return 0, 0
	}
	return int(binary.BigEndian.Uint32(data[16:20])), int(binary.BigEndian.Uint32(data[20:24]))
}

func (s *Server) handleRequestScreenshotWindow(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "screenshot_window"})
	if task == nil {
		return
	}

	slog.Info("Window screenshot requested", "agent_id", id)
	s.dispatchTask(c, task, "request_screenshot_window", "screenshot_window")
}
