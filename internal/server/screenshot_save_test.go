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

	s.writeScreenshotFile(dataDir, "agent-1", 42, b64)

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
	s.writeScreenshotFile(dataDir, "../../etc", 7, b64)
	after, err := os.ReadDir(escapeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != before {
		t.Fatalf("a traversal agent id created %d new entries", len(after)-before)
	}

	// Invalid base64 is dropped silently.
	s.writeScreenshotFile(dataDir, "agent-1", 43, "!!!not-base64!!!")
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
	s.saveScreenshot(dataDir, "agent-2", 1, b64)
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
	s.saveScreenshot(dataDir, "agent-2", 2, b64)
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
	s.writeScreenshotFile("", "agent-3", 9, b64)
	if _, err := os.Stat(filepath.Join(tmp, "data", "screenshots", "agent-3")); err != nil {
		t.Fatalf("default data dir not used: %v", err)
	}
	_ = db.Implant{}
}
