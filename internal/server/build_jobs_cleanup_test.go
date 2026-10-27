package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAbandonBuildJob covers the "never submitted" cleanup: a job that was
// created but rejected (queue full) must not linger in the map as a phantom
// failed build.
func TestAbandonBuildJob(t *testing.T) {
	s := mustTenantServer(t)

	job := &BuildJob{ID: "job-abandon", Status: "queued", CreatedAt: time.Now()}
	s.buildJobsMu.Lock()
	if s.buildJobs == nil {
		s.buildJobs = make(map[string]*BuildJob)
	}
	s.buildJobs[job.ID] = job
	s.buildJobsMu.Unlock()

	s.abandonBuildJob(job)

	s.buildJobsMu.Lock()
	_, present := s.buildJobs[job.ID]
	remaining := len(s.buildJobs)
	s.buildJobsMu.Unlock()
	if present {
		t.Fatal("the abandoned job is still in the map")
	}
	if remaining != 0 {
		t.Fatalf("map has %d entries after abandoning the only job", remaining)
	}

	// Abandoning an unknown job is harmless.
	s.abandonBuildJob(&BuildJob{ID: "never-there"})
}

// TestSweepBuildJobsOnce covers one cleanup pass: finished jobs past the cutoff
// are removed with their output files, still-building and recent jobs stay, a
// job whose output is already gone does not abort the sweep, and expired
// one-shot artifacts are reaped.
func TestSweepBuildJobsOnce(t *testing.T) {
	s := mustTenantServer(t)

	dir := t.TempDir()
	oldOutput := filepath.Join(dir, "old.bin")
	if err := os.WriteFile(oldOutput, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	recentOutput := filepath.Join(dir, "recent.bin")
	if err := os.WriteFile(recentOutput, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	expiredArtifact := filepath.Join(dir, "transient.bin")
	if err := os.WriteFile(expiredArtifact, []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	s.buildJobsMu.Lock()
	s.buildJobs = map[string]*BuildJob{
		"old-done":    {ID: "old-done", Status: "success", CompletedAt: now.Add(-2 * time.Hour), Output: oldOutput},
		"old-running": {ID: "old-running", Status: "building", CompletedAt: now.Add(-2 * time.Hour)},
		"recent-done": {ID: "recent-done", Status: "success", CompletedAt: now.Add(-1 * time.Minute), Output: recentOutput},
		"old-missing": {ID: "old-missing", Status: "failed", CompletedAt: now.Add(-3 * time.Hour), Output: filepath.Join(dir, "already-gone.bin")},
	}
	s.buildJobsMu.Unlock()

	s.transientArtifactsMu.Lock()
	if s.transientArtifacts == nil {
		s.transientArtifacts = make(map[string]time.Time)
	}
	s.transientArtifacts[expiredArtifact] = now.Add(-time.Minute)
	s.transientArtifactsMu.Unlock()

	s.sweepBuildJobsOnce()

	s.buildJobsMu.Lock()
	ids := map[string]bool{}
	for id := range s.buildJobs {
		ids[id] = true
	}
	s.buildJobsMu.Unlock()

	if ids["old-done"] {
		t.Error("an old finished job was kept")
	}
	if ids["old-missing"] {
		t.Error("an old job with a missing output file was kept")
	}
	if !ids["old-running"] {
		t.Error("a still-building job was removed")
	}
	if !ids["recent-done"] {
		t.Error("a recent finished job was removed")
	}
	if _, err := os.Stat(oldOutput); !os.IsNotExist(err) {
		t.Error("the old job's output file was not removed")
	}
	if _, err := os.Stat(recentOutput); err != nil {
		t.Error("the recent job's output file was removed")
	}
	if _, err := os.Stat(expiredArtifact); !os.IsNotExist(err) {
		t.Error("the expired one-shot artifact was not reaped")
	}

	s.transientArtifactsMu.Lock()
	left := len(s.transientArtifacts)
	s.transientArtifactsMu.Unlock()
	if left != 0 {
		t.Fatalf("transient artifacts left = %d", left)
	}
}

// TestBroadcastBulkAgentDeleteAlert covers the notification: a single delete is
// not worth an alert, a bulk one is, and neither panics without clients.
func TestBroadcastBulkAgentDeleteAlert(t *testing.T) {
	s := mustTenantServer(t)
	s.broadcastBulkAgentDeleteAlert("admin1", 1)
	s.broadcastBulkAgentDeleteAlert("admin1", 6)
}
