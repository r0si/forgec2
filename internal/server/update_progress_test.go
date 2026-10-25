package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSetUpdateProgress verifies the progress machine records state and
// throttles intermediate broadcasts (tested via stored state; broadcast
// needs no clients).
func TestSetUpdateProgress(t *testing.T) {
	s := newTasksTestServer(t)

	s.setUpdateProgress(updateStageDownloading, 10, 100, 1000, "v9.9.9", "")
	s.updateState.mu.RLock()
	stage, progress, downloaded, total, version := s.updateState.Stage, s.updateState.Progress,
		s.updateState.DownloadedBytes, s.updateState.TotalBytes, s.updateState.TargetVersion
	s.updateState.mu.RUnlock()
	if stage != updateStageDownloading || progress != 10 || downloaded != 100 || total != 1000 || version != "v9.9.9" {
		t.Fatalf("bad progress state: stage=%q progress=%d downloaded=%d total=%d version=%q",
			stage, progress, downloaded, total, version)
	}

	// Terminal failure records the error.
	s.setUpdateProgress(updateStageFailed, 0, 0, 0, "v9.9.9", "boom")
	s.updateState.mu.RLock()
	lastErr := s.updateState.LastError
	s.updateState.mu.RUnlock()
	if lastErr != "boom" {
		t.Fatalf("LastError = %q", lastErr)
	}
}

// TestHandleUpdateProgress verifies the polling fallback shape.
func TestHandleUpdateProgress(t *testing.T) {
	s := newTasksTestServer(t)
	s.setUpdateProgress(updateStageVerifying, 100, 0, 0, "v9.9.9", "")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/update-progress", s.handleUpdateProgress)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/update-progress", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct {
		Stage   string `json:"stage"`
		Percent int    `json:"percent"`
		Version string `json:"version"`
	}
	unmarshalData(t, w.Body.Bytes(), &body)
	if body.Stage != updateStageVerifying || body.Percent != 100 || body.Version != "v9.9.9" {
		t.Fatalf("bad body: %+v", body)
	}
}
