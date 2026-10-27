package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
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

// TestSaveFileChunkRefusesWithoutDataDir proves the refusal reaches the operator: the task
// is marked failed with a reason instead of the chunk being written into the cwd.
func TestSaveFileChunkRefusesWithoutDataDir(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex) // the refusal is stored through the vault
	s.cfg.Server.DataDir = ""                         // hand-built server, the shape tests and a bad config produce
	task := &db.Task{AgentID: "a1", Type: "download", Status: "running"}
	if err := s.db.Create(task).Error; err != nil {
		t.Fatal(err)
	}

	handled := saveFileChunk(s, "agent-1", task, taskResult{Output: "aGk=", Filename: "x.txt"}, "log", "prefix")
	if !handled {
		t.Fatal("the refusal was not reported as handled")
	}

	var after db.Task
	if err := s.db.First(&after, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Result == "" {
		t.Fatal("the task carries no refusal reason")
	}

	// Nothing may appear in the package directory.
	if _, err := os.Stat("uploads"); err == nil {
		t.Fatal("a relative uploads directory was created in the working directory")
	}
}
