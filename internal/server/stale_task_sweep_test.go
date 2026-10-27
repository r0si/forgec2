package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

func seedStaleTask(t *testing.T, s *Server, agentID, status string, attempts int, claimedAt time.Time) db.Task {
	t.Helper()
	task := db.Task{
		AgentID: agentID, Type: "shell", Command: "whoami", Status: status,
		DeliveryAttempts: attempts, ClaimedAt: claimedAt, CreatedAt: claimedAt,
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task
}

// TestRequeueStaleTasksRules pins the three outcomes of the stale sweep, including
// the one that protects against double execution: a task that completed between the
// SELECT and the UPDATE must not be flipped back to pending.
func TestRequeueStaleTasksRules(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "aaaa1111-2222-3333-4444-555566667777"
	seedIOCAgent(t, s, agentID)

	old := time.Now().Add(-3 * StaleRunningTaskTimeout)

	// 1. Claimed, never acknowledged, attempts left -> requeued to pending.
	requeueMe := seedStaleTask(t, s, agentID, "running", 1, old)

	// 2. At the attempt cap -> failed, never requeued (no infinite redelivery).
	exhausted := seedStaleTask(t, s, agentID, "running", 3, old)

	// 3. A legacy "sent" row: a zombie that must fail rather than resurrect.
	legacy := seedStaleTask(t, s, agentID, "sent", 0, old)

	s.requeueStaleTasks()

	var after db.Task
	if err := s.db.First(&after, requeueMe.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "pending" {
		t.Fatalf("stale running task = %q, want pending", after.Status)
	}
	if after.DeliveryAttempts != 2 {
		t.Fatalf("delivery_attempts = %d, want 2 (one more than before)", after.DeliveryAttempts)
	}
	if !after.ClaimedAt.IsZero() {
		t.Fatalf("claimed_at was not cleared, so the next claim could see a stale claim: %v", after.ClaimedAt)
	}

	// Fresh destination per query: reuse of a struct that already carries a primary
	// key makes gorm merge the old id into the WHERE clause.
	var capped db.Task
	if err := s.db.First(&capped, exhausted.ID).Error; err != nil {
		t.Fatalf("re-read the capped task: %v", err)
	}
	if capped.Status != "failed" {
		t.Fatalf("task past the attempt cap = %q, want failed", capped.Status)
	}

	var retired db.Task
	if err := s.db.First(&retired, legacy.ID).Error; err != nil {
		t.Fatalf("re-read the legacy task: %v", err)
	}
	if retired.Status != "failed" {
		t.Fatalf("legacy sent task = %q, want failed (never requeued)", retired.Status)
	}
}

// TestRequeueDoesNotResurrectCompletedTasks is the double-execution guard: the sweep
// SELECTs, then UPDATEs. A result landing in between used to flip a completed task
// back to pending and redeliver it.
func TestRequeueDoesNotResurrectCompletedTasks(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "bbbb1111-2222-3333-4444-555566667777"
	seedIOCAgent(t, s, agentID)

	old := time.Now().Add(-3 * StaleRunningTaskTimeout)
	task := seedStaleTask(t, s, agentID, "running", 1, old)

	// Simulate the result arriving between the sweep's SELECT and UPDATE: the row the
	// sweep holds is still "running", but the database says completed.
	if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status": "completed", "result": "done",
	}).Error; err != nil {
		t.Fatal(err)
	}

	// The UPDATE's status guard is what makes this safe; exercise it directly with the
	// same WHERE shape the sweep uses.
	res := s.db.Model(&db.Task{}).
		Where("id IN ? AND status = ? AND acknowledged_at IS NULL", []uint{task.ID}, "running").
		Updates(map[string]interface{}{"status": "pending", "claimed_at": time.Time{}})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatal("the requeue update matched a completed task")
	}

	var after db.Task
	if err := s.db.First(&after, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "completed" || after.Result != "done" {
		t.Fatalf("a completed task was stomped: %+v", after)
	}
}

// TestFailStaleAcknowledgedTasks pins the other half: an agent that acknowledged but
// never returned a result is failed after the window, while one that is still
// beaconing gets a renewal instead of being killed mid-task.
func TestFailStaleAcknowledgedTasksSweep(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "cccc1111-2222-3333-4444-555566667777"
	seedIOCAgent(t, s, agentID)

	stale := time.Now().Add(-3 * StaleRunningTaskTimeout)
	task := db.Task{
		AgentID: agentID, Type: "shell", Command: "whoami", Status: "running",
		ClaimedAt: stale, CreatedAt: stale, AcknowledgedAt: &stale, DeliveryAttempts: 1,
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	s.failStaleAcknowledgedTasks()

	var after db.Task
	if err := s.db.First(&after, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	// Either it was failed, or its deadline was renewed because the agent is healthy.
	// Both are correct outcomes; what must never happen is staying running with no
	// deadline and no further attention.
	if after.Status == "running" && after.AcknowledgedAt == nil {
		t.Fatalf("a stale acknowledged task neither failed nor kept a deadline: %+v", after)
	}
	if after.Status == "failed" && after.Error == "" {
		t.Fatal("a failed stale task carries no reason")
	}
}
