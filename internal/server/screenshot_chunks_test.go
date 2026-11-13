package server

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

func chunkedScreenshotResults(taskID uint, payload []byte, chunks int) []taskResult {
	chunkSize := (len(payload) + chunks - 1) / chunks
	results := make([]taskResult, 0, chunks+1)
	for i := 0; i < chunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		results = append(results, taskResult{
			TaskID:   taskID,
			Type:     "screenshot_chunk",
			Output:   base64.StdEncoding.EncodeToString(payload[start:end]),
			Encoding: "base64",
			Offset:   int64(i),
			Size:     int64(chunks),
		})
	}
	return results
}

// TestChunkedScreenshotIsReassembled pins the multi-chunk capture contract:
// the chunks stay out of the task row, and the final marker only completes the
// task after the server has assembled and saved the image.
func TestChunkedScreenshotIsReassembled(t *testing.T) {
	s := mustTenantServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir

	const agentID = "shot-chunk-agent"
	if err := s.db.Create(&db.Implant{ID: agentID, TenantID: 1, Hostname: "shot", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agentID, TenantID: 1, Type: "screenshot", Status: "running"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	// A JPEG magic keeps the stored extension honest; the rest is synthetic.
	payload := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x42}, 700*1024)...)
	results := chunkedScreenshotResults(task.ID, payload, 3)
	results = append(results, taskResult{
		TaskID: task.ID,
		Type:   "screenshot",
		Output: "screenshot_chunked",
		Size:   3,
	})
	s.processTaskResults(db.Implant{ID: agentID}, results, agentID, time.Now())

	var got db.Task
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" {
		t.Fatalf("status = %q, want completed: %s", got.Status, got.Error)
	}
	dir := filepath.Join(dataDir, "screenshots", agentID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read screenshot dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".jpg") {
		t.Fatalf("screenshot files = %v, want one .jpg", entries)
	}
	saved, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, payload) {
		t.Fatalf("saved image is %d bytes, want the %d assembled bytes", len(saved), len(payload))
	}
}

// TestIncompleteChunkedScreenshotFailsTask is the other half: a final marker
// without every chunk must not produce a completed task with no gallery image.
func TestIncompleteChunkedScreenshotFailsTask(t *testing.T) {
	s := mustTenantServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir

	const agentID = "shot-incomplete-agent"
	if err := s.db.Create(&db.Implant{ID: agentID, TenantID: 1, Hostname: "shot", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agentID, TenantID: 1, Type: "screenshot", Status: "running"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x42}, 32*1024)...)
	results := chunkedScreenshotResults(task.ID, payload, 3)[:2] // drop the last chunk
	results = append(results, taskResult{TaskID: task.ID, Type: "screenshot", Output: "screenshot_chunked", Size: 3})
	s.processTaskResults(db.Implant{ID: agentID}, results, agentID, time.Now())

	var got db.Task
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed for an incomplete capture", got.Status)
	}
	if !strings.Contains(got.Error, "incomplete") {
		t.Fatalf("error = %q, want the incomplete-chunks reason", got.Error)
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "screenshots", agentID)); err == nil && len(entries) != 0 {
		t.Fatalf("incomplete capture wrote %d screenshot file(s)", len(entries))
	}
}
