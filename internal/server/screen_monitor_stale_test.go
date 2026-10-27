package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIsScreenMonitoringExpiresDeadStreams pins the heartbeat: a stream entry
// older than the timeout belongs to a monitor that died without a stop (agent
// crash, console closed). Treating it as active meant saveScreenshot silently
// stopped persisting that agent's screenshots forever.
func TestIsScreenMonitoringExpiresDeadStreams(t *testing.T) {
	s := mustTenantServer(t)

	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorImplants["live"] = time.Now()
	s.screenMonitorImplants["dead"] = time.Now().Add(-2 * screenMonitorStaleAfter)
	s.screenMonitorMu.Unlock()

	if !s.IsScreenMonitoring("live") {
		t.Fatal("a fresh stream was reported as inactive")
	}
	if s.IsScreenMonitoring("dead") {
		t.Fatal("a dead stream is still reported as active")
	}
	// The dead entry is removed, not just ignored.
	s.screenMonitorMu.Lock()
	_, still := s.screenMonitorImplants["dead"]
	s.screenMonitorMu.Unlock()
	if still {
		t.Fatal("the stale entry was not removed")
	}

	// Case-insensitive key, as everywhere else in this file.
	if !s.IsScreenMonitoring("LIVE") {
		t.Fatal("the lookup is not case-insensitive")
	}
	if s.IsScreenMonitoring("never-started") {
		t.Fatal("an unknown agent is reported as active")
	}
}

// TestScreenMonitorSweepFreesSlots covers the sweep used before the limit check:
// dead entries are dropped, live ones survive.
func TestScreenMonitorSweepFreesSlots(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorImplants["a"] = now.Add(-time.Hour)
	s.screenMonitorImplants["b"] = now.Add(-time.Minute)
	s.screenMonitorMu.Unlock()

	s.screenMonitorMu.Lock()
	dropped := s.sweepScreenMonitorsLocked(now)
	remaining := len(s.screenMonitorImplants)
	s.screenMonitorMu.Unlock()
	if dropped != 1 {
		t.Fatalf("dropped %d entries, want 1", dropped)
	}
	if remaining != 1 {
		t.Fatalf("remaining = %d, want 1", remaining)
	}
}

// TestSaveScreenshotResumesAfterDeadStream is the operator-visible consequence:
// once a dead stream expires, screenshots are persisted again.
func TestSaveScreenshotResumesAfterDeadStream(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	b64 := base64.StdEncoding.EncodeToString([]byte("frame"))
	dir := filepath.Join(dataDir, "screenshots", "agent-9")

	// A LIVE stream is relayed, not retained.
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorImplants["agent-9"] = time.Now()
	s.screenMonitorMu.Unlock()
	s.saveScreenshot(dataDir, "agent-9", 1, b64)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Fatalf("a frame was retained during a live stream: %d", len(entries))
	}

	// A DEAD stream expires, so the frame is persisted again. Before the fix this
	// stayed blocked forever and screenshots silently stopped appearing.
	s.screenMonitorMu.Lock()
	s.screenMonitorImplants["agent-9"] = time.Now().Add(-2 * screenMonitorStaleAfter)
	s.screenMonitorMu.Unlock()
	s.saveScreenshot(dataDir, "agent-9", 2, b64)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("files = %d, want 1", len(entries))
	}
	if !strings.HasPrefix(entries[0].Name(), "screenshot_2_") {
		t.Fatalf("filename = %q", entries[0].Name())
	}
}

// TestBroadcastScreenshotRefreshesHeartbeat pins the other direction: a live
// stream must not expire just because it started a while ago.
func TestBroadcastScreenshotRefreshesHeartbeat(t *testing.T) {
	s := mustTenantServer(t)
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorImplants["agent-hb"] = time.Now().Add(-screenMonitorStaleAfter + 10*time.Second)
	s.screenMonitorMu.Unlock()

	s.BroadcastScreenshot("agent-hb", base64.StdEncoding.EncodeToString([]byte("png")))

	s.screenMonitorMu.Lock()
	last, ok := s.screenMonitorImplants["agent-hb"]
	s.screenMonitorMu.Unlock()
	if !ok {
		t.Fatal("the live stream was removed")
	}
	if time.Since(last) > time.Minute {
		t.Fatalf("the heartbeat was not refreshed: %v", time.Since(last))
	}
	if !s.IsScreenMonitoring("agent-hb") {
		t.Fatal("a refreshed stream is not reported as active")
	}
}
