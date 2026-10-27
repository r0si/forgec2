package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupBuildJobsReapsExpiredArtifacts pins the loop behind a claim that only lived in
// comments: registerTransientArtifact promises "automatic cleanup after one hour", and the
// reaper that enforces it is cleanupBuildJobs - a long-running loop whose interval was
// hardcoded, so nothing ever exercised the reaping without waiting fifteen minutes.
//
// The name misleads ("cleanup" reads like a one-shot call, and a previous reader concluded
// the startup goroutine swept once and returned); the loop is real, so this test now holds it
// to the promise.
func TestCleanupBuildJobsReapsExpiredArtifacts(t *testing.T) {
	s := mustTenantServer(t)
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())

	old := buildJobSweepInterval
	buildJobSweepInterval = 20 * time.Millisecond
	t.Cleanup(func() { buildJobSweepInterval = old })

	victim := filepath.Join(t.TempDir(), "stager.bin")
	if err := os.WriteFile(victim, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.transientArtifactsMu.Lock()
	s.transientArtifacts = map[string]time.Time{victim: time.Now().Add(-time.Minute)}
	s.transientArtifactsMu.Unlock()

	done := make(chan struct{})
	go func() { defer close(done); s.cleanupBuildJobs() }()
	t.Cleanup(s.ctxCancel)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(victim); os.IsNotExist(err) {
			return // reaped on a tick, without a restart
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("an expired artifact was never reaped")
}

// TestCleanupBuildJobsStopsWithTheServer keeps shutdown working: the loop must exit on the
// server context, or s.wg.Wait() in Shutdown would block forever.
func TestCleanupBuildJobsStopsWithTheServer(t *testing.T) {
	s := mustTenantServer(t)
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())

	old := buildJobSweepInterval
	buildJobSweepInterval = time.Hour // never ticks during the test
	t.Cleanup(func() { buildJobSweepInterval = old })

	done := make(chan struct{})
	go func() { defer close(done); s.cleanupBuildJobs() }()
	s.ctxCancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep loop did not exit when the server context was cancelled")
	}
}
