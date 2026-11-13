package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
)

// TestUploadsRootRefusesEmptyDataDir pins the same defect class that produced hundreds of
// stray directories for hosted payloads: filepath.Join("", "uploads") is the RELATIVE path
// "uploads", so a Server without server.data_dir wrote exfiltrated files next to whatever
// directory the process started in - writes, not reads, and invisible afterwards.
func TestUploadsRootRefusesEmptyDataDir(t *testing.T) {
	bare := &Server{cfg: &config.Config{}}
	if _, err := bare.uploadsRootDir(); err == nil {
		t.Fatal("an empty data_dir produced an uploads path")
	}

	withDir := &Server{cfg: &config.Config{}}
	withDir.cfg.Server.DataDir = t.TempDir()
	got, err := withDir.uploadsRootDir()
	if err != nil {
		t.Fatalf("a configured data_dir was refused: %v", err)
	}
	if want := filepath.Join(withDir.cfg.Server.DataDir, "uploads"); got != want {
		t.Fatalf("uploads root = %q, want %q", got, want)
	}
}

// TestPersistFileChunkRefusesWithoutDataDir proves the refusal reaches the
// caller: an empty server.data_dir is an error, not a write into the cwd.
func TestPersistFileChunkRefusesWithoutDataDir(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = "" // hand-built server, the shape tests and a bad config produce
	task := &db.Task{AgentID: "a1", Type: "download", Status: "running"}

	if _, err := s.persistFileChunk("agent-1", task, taskResult{Output: "aGk=", Filename: "x.txt"}, "log", "prefix"); err == nil {
		t.Fatal("an empty data_dir produced a chunk write")
	}

	// Nothing may appear in the package directory.
	if _, err := os.Stat("uploads"); err == nil {
		t.Fatal("a relative uploads directory was created in the working directory")
	}
}

// TestPersistFileChunkWritesBytes covers the success path: the bytes land in
// <data_dir>/uploads/<agent>/<filename> and the returned summary is what the
// task row will carry.
func TestPersistFileChunkWritesBytes(t *testing.T) {
	s := newTestFileServer(t, testutil.SetupTestDB(t))
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	task := &db.Task{AgentID: "agent-1", Type: "upload"}
	payload := []byte("hello chunk")
	summary, err := s.persistFileChunk("agent-1", task, taskResult{
		Output:   base64.StdEncoding.EncodeToString(payload),
		Filename: "x.txt",
		Size:     int64(len(payload)),
	}, "upload", "File chunk saved")
	if err != nil {
		t.Fatalf("persist chunk: %v", err)
	}
	if !strings.Contains(summary, "x.txt") || !strings.Contains(summary, "offset 0") {
		t.Fatalf("summary = %q", summary)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "uploads", "agent-1", "x.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("file content = %q, want %q", got, payload)
	}
}

// TestUploadChunkSaveFailureFailsTask is the operator-visible half of the
// ordering rule: a chunk that could not be written must not leave its task
// completed, because the UI would report a successful transfer with missing
// bytes.
func TestUploadChunkSaveFailureFailsTask(t *testing.T) {
	s := mustTenantServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	// A FILE named uploads makes MkdirAll fail, the same class of local
	// storage failure as a full disk or a permission error.
	if err := os.WriteFile(filepath.Join(dataDir, "uploads"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	const agentID = "chunk-save-fail"
	if err := s.db.Create(&db.Implant{ID: agentID, TenantID: 1, Hostname: "chunk", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agentID, TenantID: 1, Type: "upload", Status: "running"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.processTaskResults(db.Implant{ID: agentID}, []taskResult{{
		TaskID:   task.ID,
		Type:     "upload",
		Output:   "aGk=",
		Filename: "x.txt",
	}}, agentID, time.Now())

	var got db.Task
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed when the chunk could not be written", got.Status)
	}
	if !strings.Contains(got.Error, "uploads") {
		t.Fatalf("error = %q, want the uploads directory failure", got.Error)
	}
}
