package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestEngagementBacklogNamesTheHost covers the populated case: which beacon was behind, and for how long.
//
// It is separate from the bundle test because that fixture finishes every task, and adding a queued task there would shift every
// row-count assertion in the archive test - the kind of unrelated breakage that makes a suite expensive to read.
func TestEngagementBacklogNamesTheHost(t *testing.T) {
	now := time.Now()
	tasks := []db.Task{
		// Two queued for db-02, one of them old; one running for web-01.
		{AgentID: "a-2", Status: "pending", CreatedAt: now.Add(-90 * time.Minute)},
		{AgentID: "a-2", Status: "pending", CreatedAt: now.Add(-2 * time.Minute)},
		{AgentID: "a-1", Status: "running", CreatedAt: now.Add(-5 * time.Minute)},
		// A finished task must not appear anywhere in the backlog.
		{AgentID: "a-1", Status: "completed", CreatedAt: now.Add(-30 * time.Minute)},
	}
	failures := buildEngagementFailures(tasks, map[string]string{"a-1": "web-01", "a-2": "db-02"})

	if failures.Unfinished.Pending != 2 || failures.Unfinished.Running != 1 {
		t.Fatalf("counts = %d pending / %d running", failures.Unfinished.Pending, failures.Unfinished.Running)
	}
	if failures.Unfinished.AgentsHeld != 2 {
		t.Fatalf("agents_holding_work = %d, want 2", failures.Unfinished.AgentsHeld)
	}
	if len(failures.Unfinished.Backlog) != 2 {
		t.Fatalf("backlog = %+v, want both hosts", failures.Unfinished.Backlog)
	}
	// Busiest first: db-02 holds two, so it leads and carries its hostname and the age of its oldest task.
	first := failures.Unfinished.Backlog[0]
	if first.AgentID != "a-2" || first.Pending != 2 || first.Host != "db-02" {
		t.Fatalf("first backlog entry = %+v", first)
	}
	if first.OldestPendingSeconds < 89*60 || first.OldestPendingSeconds > 91*60 {
		t.Fatalf("oldest pending = %ds, want about 90 minutes", first.OldestPendingSeconds)
	}
	second := failures.Unfinished.Backlog[1]
	if second.AgentID != "a-1" || second.Running != 1 || second.Pending != 0 {
		t.Fatalf("second backlog entry = %+v", second)
	}
}
