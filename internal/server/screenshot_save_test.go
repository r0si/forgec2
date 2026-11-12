package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestWriteScreenshotFile covers screenshot persistence: the file is written
// under <dataDir>/screenshots/<agent>/ with the task id in its name, an agent id
// that would escape the directory writes nothing, and invalid base64 is a silent
// no-op rather than a corrupt file.
func TestWriteScreenshotFile(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	payload := []byte("PNG-BYTES")
	b64 := base64.StdEncoding.EncodeToString(payload)

	if err := s.writeScreenshotFile(dataDir, "agent-1", 42, b64); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	dir := filepath.Join(dataDir, "screenshots", "agent-1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read screenshot dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("files = %d, want 1", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "screenshot_42_") || !strings.HasSuffix(name, ".png") {
		t.Fatalf("filename = %q, want screenshot_42_<unix>.png", name)
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("file content = %q", got)
	}
	// Screenshots are operator-only data: 0600. Windows has no POSIX mode
	// bits and Go synthesises 0666 for a writable file, so the assertion only
	// means something on the platforms that enforce it.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("file mode = %o, want 600", mode)
		}
	}

	// An agent id that tries to escape the screenshots root writes nothing.
	escapeDir := filepath.Join(dataDir, "screenshots")
	before := len(entries)
	if err := s.writeScreenshotFile(dataDir, "../../etc", 7, b64); err == nil {
		t.Fatal("traversal agent id returned no error")
	}
	after, err := os.ReadDir(escapeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != before {
		t.Fatalf("a traversal agent id created %d new entries", len(after)-before)
	}

	// Invalid base64 is dropped silently.
	if err := s.writeScreenshotFile(dataDir, "agent-1", 43, "!!!not-base64!!!"); err == nil {
		t.Fatal("invalid base64 returned no error")
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("invalid base64 wrote a file: %d entries", len(entries))
	}
}

// TestSaveScreenshotSkipsDuringMonitoring pins the live-monitoring rule: while a
// screen stream is active, frames are relayed but not retained on disk.
func TestSaveScreenshotSkipsDuringMonitoring(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	b64 := base64.StdEncoding.EncodeToString([]byte("frame"))

	// Not monitoring: the file lands.
	if err := s.saveScreenshot(dataDir, "agent-2", 1, b64); err != nil {
		t.Fatalf("save screenshot: %v", err)
	}
	dir := filepath.Join(dataDir, "screenshots", "agent-2")
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("expected one screenshot, got %v (err %v)", entries, err)
	}

	// Monitoring: nothing further is written. The map is guarded by
	// screenMonitorMu and keyed by the lowercased agent id.
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorImplants[strings.ToLower("agent-2")] = time.Now()
	s.screenMonitorMu.Unlock()
	t.Cleanup(func() {
		s.screenMonitorMu.Lock()
		delete(s.screenMonitorImplants, strings.ToLower("agent-2"))
		s.screenMonitorMu.Unlock()
	})
	if err := s.saveScreenshot(dataDir, "agent-2", 2, b64); err != nil {
		t.Fatalf("save screenshot while monitoring: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a live-monitoring frame was retained: %d files", len(entries))
	}
}

// TestWriteScreenshotFileDefaultDir covers the empty-dataDir fallback.
func TestWriteScreenshotFileDefaultDir(t *testing.T) {
	s := mustTenantServer(t)
	// Point the process at a temp dir so the "data" default lands somewhere
	// removable; the function joins the relative default itself.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	if err := s.writeScreenshotFile("", "agent-3", 9, b64); err != nil {
		t.Fatalf("write screenshot with default dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "data", "screenshots", "agent-3")); err != nil {
		t.Fatalf("default data dir not used: %v", err)
	}
	_ = db.Implant{}
}

// TestWriteScreenshotFileNamesJPEGCorrectly pins the format contract: the
// implant captures JPEG, so the stored filename must not claim ".png" (a
// download would carry the wrong extension and MIME).
func TestWriteScreenshotFileNamesJPEGCorrectly(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	// The JPEG magic is enough for content sniffing; the bytes need not decode
	// to a complete image for this storage-level assertion.
	payload := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00}
	b64 := base64.StdEncoding.EncodeToString(payload)

	if err := s.writeScreenshotFile(dataDir, "agent-jpeg", 5, b64); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	dir := filepath.Join(dataDir, "screenshots", "agent-jpeg")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("files = %d, want 1", len(entries))
	}
	if name := entries[0].Name(); !strings.HasSuffix(name, ".jpg") {
		t.Fatalf("filename = %q, want a .jpg capture", name)
	}
	if got := screenshotMime(payload); got != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", got)
	}
}

// TestWriteScreenshotFileReportsInvalidBase64 pins the new error return: a
// caller that claims a capture completed must be able to see that it did not.
func TestWriteScreenshotFileReportsInvalidBase64(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.writeScreenshotFile(t.TempDir(), "agent-bad", 6, "!!!not-base64!!!"); err == nil {
		t.Fatal("invalid base64 returned no error")
	}
}

// TestScreenshotResultSaveFailureFailsTask is the operator-visible half of the
// same rule: a capture that could not be written must not be reported as a
// completed screenshot task with an empty gallery.
func TestScreenshotResultSaveFailureFailsTask(t *testing.T) {
	s := mustTenantServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	// A FILE named screenshots makes MkdirAll fail, which is the same class of
	// local storage failure as a full disk or a permission error.
	if err := os.WriteFile(filepath.Join(dataDir, "screenshots"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	const agentID = "shot-save-fail"
	if err := s.db.Create(&db.Implant{ID: agentID, TenantID: 1, Hostname: "shot", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agentID, TenantID: 1, Type: "screenshot", Status: "running"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.processTaskResults(db.Implant{ID: agentID}, []taskResult{{
		TaskID: task.ID,
		Type:   "screenshot",
		Output: base64.StdEncoding.EncodeToString([]byte("jpeg-bytes")),
	}}, agentID, time.Now())

	var got db.Task
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed when the capture could not be written", got.Status)
	}
	if !strings.Contains(got.Error, "screenshot") {
		t.Fatalf("error = %q, want a screenshot save failure", got.Error)
	}
}
