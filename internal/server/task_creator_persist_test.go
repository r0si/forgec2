package server

import (
	"bytes"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestPersistTaskCreatorReportsFailure covers the attribution write that used to be fire-and-forget.
//
// Both call sites (manual dispatch and the WebRTC start) set task.CreatedBy in memory and then wrote it
// to the row with the error discarded. The audit record reads the in-memory value, so the audit trail
// stayed right while the row could silently say "creator unknown" - the one field an investigation
// needs. This closes it with a log line, which is what is testable: a real write failure is forced by
// closing the pool.
func TestPersistTaskCreatorReportsFailure(t *testing.T) {
	s := mustTenantServer(t)

	// Happy path: the value lands on the row.
	task := &db.Task{AgentID: "a-1", Type: "shell", Command: "whoami", Status: "pending", CreatedAt: time.Now()}
	if err := s.db.Create(task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	s.persistTaskCreator(task, "operator-1")
	if task.CreatedBy != "operator-1" {
		t.Fatalf("in-memory creator = %q", task.CreatedBy)
	}
	var stored db.Task
	if err := s.db.First(&stored, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.CreatedBy != "operator-1" {
		t.Fatalf("stored creator = %q", stored.CreatedBy)
	}

	// Empty creator is a no-op, not an UPDATE to "".
	s.persistTaskCreator(task, "")
	if task.CreatedBy != "operator-1" {
		t.Fatalf("an empty creator overwrote the value: %q", task.CreatedBy)
	}

	// Failure path: close the pool so the UPDATE errors, and require a log line instead of silence.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	sqlDB, err := s.db.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close pool: %v", err)
	}
	_ = sql.ErrConnDone

	s.persistTaskCreator(&db.Task{AgentID: "a-1", Type: "shell"}, "operator-2")
	if !strings.Contains(logs.String(), "Failed to persist task creator") {
		t.Fatalf("a failed attribution write was silent: %q", logs.String())
	}
}
