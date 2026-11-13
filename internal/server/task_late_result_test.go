package server

import (
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestCancelledTaskKeepsLateResult pins the operator-visible answer to "did it
// run before the abort landed?": the task remains cancelled, but the agent's
// late output and final status are preserved for the audit/detail view.
func TestCancelledTaskKeepsLateResult(t *testing.T) {
	s := partialTestServer(t)
	const uuid = "late-agent"
	if err := s.db.Create(&db.Implant{ID: uuid, Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "whoami", Status: "cancelled"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.processTaskResults(db.Implant{ID: uuid}, []taskResult{{
		TaskID:   task.ID,
		Type:     "shell",
		Output:   "NT AUTHORITY\\SYSTEM",
		ResultID: "late-1",
	}}, uuid, time.Now())

	var got db.Task
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancelled" {
		t.Fatalf("status = %q, want the task to stay cancelled", got.Status)
	}
	if !got.LateResult || got.LateResultStatus != "completed" || got.LateResultAt == nil {
		t.Fatalf("late result not recorded: %+v", got)
	}
	if got.Result != "NT AUTHORITY\\SYSTEM" {
		t.Fatalf("late result = %q", got.Result)
	}
	if got.LastResultID != "late-1" {
		t.Fatalf("late result id = %q", got.LastResultID)
	}

	// A second late result must not overwrite the first: the first terminal
	// answer is the one the agent actually produced.
	s.processTaskResults(db.Implant{ID: uuid}, []taskResult{{
		TaskID: task.ID, Type: "shell", Output: "different", ResultID: "late-2",
	}}, uuid, time.Now())
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Result != "NT AUTHORITY\\SYSTEM" {
		t.Fatalf("second late result overwrote the first: %q", got.Result)
	}
}

// TestCancelledTaskKeepsLateFailure covers the other late outcome: a command
// that failed after cancellation should still say so, without changing the
// cancelled status.
func TestCancelledTaskKeepsLateFailure(t *testing.T) {
	s := partialTestServer(t)
	const uuid = "late-fail-agent"
	if err := s.db.Create(&db.Implant{ID: uuid, Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "blocked", Status: "cancelled", Error: "cancelled by operator"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.processTaskResults(db.Implant{ID: uuid}, []taskResult{{
		TaskID: task.ID,
		Type:   "shell",
		Error:  "access denied",
	}}, uuid, time.Now())

	var got db.Task
	if err := s.db.First(&got, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancelled" || !got.LateResult || got.LateResultStatus != "failed" {
		t.Fatalf("late failure not recorded: %+v", got)
	}
	if !strings.Contains(got.Result, "access denied") {
		t.Fatalf("result = %q, want the late failure detail", got.Result)
	}
	if !strings.Contains(got.Error, "cancelled") {
		t.Fatalf("error = %q, want the cancellation reason preserved", got.Error)
	}
}
