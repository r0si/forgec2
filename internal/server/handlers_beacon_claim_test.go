package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
)

func TestFetchPendingTasksClaimsOnlyPendingTasks(t *testing.T) {
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}
	agentID := "agent-1"
	now := time.Now()

	oldRunning := db.Task{AgentID: agentID, Type: "old", Status: "running", Priority: 3, CreatedAt: now.Add(-time.Minute)}
	pendingLow := db.Task{AgentID: agentID, Type: "low", Status: "pending", Priority: 0, CreatedAt: now}
	pendingHigh := db.Task{AgentID: agentID, Type: "high", Status: "pending", Priority: 3, CreatedAt: now.Add(time.Second)}
	for _, task := range []*db.Task{&oldRunning, &pendingLow, &pendingHigh} {
		if err := database.Create(task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	claimed := s.fetchPendingTasks(agentID)
	if len(claimed) != 2 {
		t.Fatalf("expected 2 newly claimed tasks, got %d", len(claimed))
	}
	if claimed[0].ID != pendingHigh.ID || claimed[1].ID != pendingLow.ID {
		t.Fatalf("unexpected claimed task order: %+v", claimed)
	}

	var persistedOld db.Task
	if err := database.First(&persistedOld, oldRunning.ID).Error; err != nil {
		t.Fatalf("load old running task: %v", err)
	}
	if persistedOld.ClaimedBy != "" {
		t.Fatalf("existing running task was changed: %+v", persistedOld)
	}

	var persistedClaimed []db.Task
	if err := database.Where("id IN ?", []uint{pendingHigh.ID, pendingLow.ID}).Find(&persistedClaimed).Error; err != nil {
		t.Fatalf("load claimed tasks: %v", err)
	}
	for _, claimedTask := range persistedClaimed {
		if claimedTask.Status != "running" || claimedTask.ClaimedBy != agentID || claimedTask.ClaimedAt.IsZero() {
			t.Fatalf("task was not recorded as claimed: %+v", claimedTask)
		}
	}
}

// TestFetchPendingTasksOnPostgres pins the task-claim SQL against the only
// production driver. The claim query selects `offset` with manual quoting; a
// MySQL-style backtick makes PostgreSQL fail the whole claim with a syntax
// error (42601) while the SQLite claim tests stayed green, so no dispatched
// task was ever delivered under PostgreSQL. Skips when Postgres is absent.
func TestFetchPendingTasksOnPostgres(t *testing.T) {
	gdb := newMigratedDB(t)
	s := &Server{db: gdb}
	// Unique per run: newMigratedDB shares one database across test runs, so a
	// fixed id would collide with the row a previous run left behind.
	agentID := fmt.Sprintf("pg-claim-agent-%d", time.Now().UnixNano())
	if err := gdb.Create(&db.Implant{ID: agentID, Hostname: "pg-claim-host"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	task := db.Task{AgentID: agentID, Type: "shell", Status: "pending", Command: "whoami", Offset: 7}
	if err := gdb.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	claimed := s.fetchPendingTasks(agentID)
	if len(claimed) != 1 || claimed[0].ID != task.ID || claimed[0].Offset != 7 {
		t.Fatalf("PostgreSQL claim returned %+v, want the seeded task with its offset", claimed)
	}
}

func TestFetchPendingTasksRespectsCapacity(t *testing.T) {
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}
	agentID := "agent-capacity"
	for i := 0; i < 2; i++ {
		if err := database.Create(&db.Task{AgentID: agentID, Type: "shell", Status: "pending"}).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}
	if tasks := s.fetchPendingTasks(agentID, 0); len(tasks) != 0 {
		t.Fatalf("expected no tasks at zero capacity, got %d", len(tasks))
	}
	if tasks := s.fetchPendingTasks(agentID, 1); len(tasks) != 1 {
		t.Fatalf("expected one task at capacity one, got %d", len(tasks))
	}
}

func TestFetchRelayedChildTasksClaimsOnlyDispatchablePendingTasks(t *testing.T) {
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}
	parentID := "relay-parent"
	childID := "relay-child"
	if err := database.Create(&db.Implant{ID: childID, ParentID: parentID}).Error; err != nil {
		t.Fatalf("create child: %v", err)
	}

	now := time.Now()
	oldRunning := db.Task{AgentID: childID, Type: "old", Status: "running", ClaimedBy: parentID, CreatedAt: now.Add(-time.Minute)}
	invalidUpload := db.Task{AgentID: childID, Type: "upload", Status: "pending", Path: "chunk.bin", CreatedAt: now}
	validUpload := db.Task{AgentID: childID, Type: "upload", Status: "pending", Path: "chunk.bin", PrevMAC: "previous", MAC: "current", Priority: 2, CreatedAt: now.Add(time.Second)}
	shellTask := db.Task{AgentID: childID, Type: "shell", Status: "pending", Command: "whoami", CreatedAt: now.Add(2 * time.Second)}
	for _, task := range []*db.Task{&oldRunning, &invalidUpload, &validUpload, &shellTask} {
		if err := database.Create(task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	relayed := s.fetchRelayedChildTasks(parentID)
	if len(relayed) != 1 || relayed[0].AgentID != childID {
		t.Fatalf("expected one child relay batch, got %+v", relayed)
	}
	// Two newly claimed + the old unacked running task re-carried for
	// lost-response recovery (status untouched: still running/claimed).
	if len(relayed[0].Tasks) != 3 {
		t.Fatalf("expected two claimed plus one recarried task, got %+v", relayed[0].Tasks)
	}
	if relayed[0].Tasks[0].ID != validUpload.ID || relayed[0].Tasks[0].PrevMAC != "previous" || relayed[0].Tasks[0].MAC != "current" {
		t.Fatalf("upload integrity chain was not relayed: %+v", relayed[0].Tasks[0])
	}
	if relayed[0].Tasks[1].ID != shellTask.ID {
		t.Fatalf("unexpected relayed task order: %+v", relayed[0].Tasks)
	}
	if relayed[0].Tasks[2].ID != oldRunning.ID {
		t.Fatalf("unacked running task was not recarried last: %+v", relayed[0].Tasks)
	}

	var persistedOld, persistedInvalid db.Task
	if err := database.First(&persistedOld, oldRunning.ID).Error; err != nil {
		t.Fatalf("load old running task: %v", err)
	}
	if err := database.First(&persistedInvalid, invalidUpload.ID).Error; err != nil {
		t.Fatalf("load invalid upload: %v", err)
	}
	if persistedOld.Status != "running" || persistedInvalid.Status != "pending" {
		t.Fatalf("non-dispatchable tasks were mutated: old=%+v invalid=%+v", persistedOld, persistedInvalid)
	}
	if persistedOld.ClaimedBy != parentID {
		t.Fatalf("recarried task lost its claim: %+v", persistedOld)
	}
}

func TestFetchPendingTasksRecarriesUnackedRunning(t *testing.T) {
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}
	agentID := "agent-recarry"

	lost := db.Task{AgentID: agentID, Type: "shell", Status: "running", ClaimedBy: agentID, ClaimedAt: time.Now().Add(-time.Minute)}
	acked := db.Task{AgentID: agentID, Type: "shell", Status: "running", ClaimedBy: agentID, ClaimedAt: time.Now().Add(-time.Minute)}
	now := time.Now()
	acked.AcknowledgedAt = &now
	other := db.Task{AgentID: "other-agent", Type: "shell", Status: "running", ClaimedBy: "other-agent", ClaimedAt: time.Now().Add(-time.Minute)}
	for _, task := range []*db.Task{&lost, &acked, &other} {
		if err := database.Create(task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	claimed := s.fetchPendingTasks(agentID)
	if len(claimed) != 1 || claimed[0].ID != lost.ID {
		t.Fatalf("expected only the lost unacked task recarried, got %+v", claimed)
	}

	// Re-carry must not mutate the claim: stale sweeps still key on the
	// original claimed_at.
	var reloaded db.Task
	if err := database.First(&reloaded, lost.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != "running" || reloaded.ClaimedBy != agentID {
		t.Fatalf("recarry mutated the claim: %+v", reloaded)
	}
}

func TestTaskAcknowledgementPreventsStaleRequeue(t *testing.T) {
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}
	agentID := "agent-ack"
	claimedAt := time.Now().Add(-StaleRunningTaskTimeout - time.Minute)
	acknowledgedAt := claimedAt.Add(time.Minute)

	unacknowledged := db.Task{AgentID: agentID, Type: "unacknowledged", Status: "running", ClaimedBy: agentID, ClaimedAt: claimedAt}
	acknowledged := db.Task{AgentID: agentID, Type: "acknowledged", Status: "running", ClaimedBy: agentID, ClaimedAt: claimedAt, AcknowledgedAt: &acknowledgedAt}
	fresh := db.Task{AgentID: agentID, Type: "fresh", Status: "running", ClaimedBy: agentID, ClaimedAt: time.Now()}
	for _, task := range []*db.Task{&unacknowledged, &acknowledged, &fresh} {
		if err := database.Create(task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}
	s.processTaskAcknowledgements(agentID, []uint{fresh.ID, fresh.ID}, time.Now())
	if err := database.First(&fresh, fresh.ID).Error; err != nil {
		t.Fatalf("load acknowledged task: %v", err)
	}
	if fresh.AcknowledgedAt == nil {
		t.Fatal("running task acknowledgement was not recorded")
	}

	s.requeueStaleTasks()

	var reloadedUnacknowledged, reloadedAcknowledged db.Task
	if err := database.First(&reloadedUnacknowledged, unacknowledged.ID).Error; err != nil {
		t.Fatalf("load unacknowledged task: %v", err)
	}
	if err := database.First(&reloadedAcknowledged, acknowledged.ID).Error; err != nil {
		t.Fatalf("load acknowledged task: %v", err)
	}
	if reloadedUnacknowledged.Status != "pending" || reloadedUnacknowledged.ClaimedBy != "" || !reloadedUnacknowledged.ClaimedAt.IsZero() {
		t.Fatalf("unacknowledged task was not released: %+v", reloadedUnacknowledged)
	}
	if reloadedAcknowledged.Status != "running" || reloadedAcknowledged.AcknowledgedAt == nil {
		t.Fatalf("acknowledged task was requeued: %+v", reloadedAcknowledged)
	}

	s.processTaskAcknowledgements(agentID, []uint{unacknowledged.ID, unacknowledged.ID}, time.Now())
	if err := database.First(&reloadedUnacknowledged, unacknowledged.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloadedUnacknowledged.AcknowledgedAt != nil {
		t.Fatal("pending task acknowledgement should be ignored")
	}
}

func TestRequeueStaleTasksRespectsLongSleep(t *testing.T) {
	database := testutil.SetupTestDB(t)
	s := &Server{db: database}

	// Long-sleep agent (1h interval): a task claimed 30min ago is healthy.
	sleepy := "agent-sleepy"
	if err := database.Create(&db.Implant{ID: sleepy, CurrentInterval: 3600}).Error; err != nil {
		t.Fatalf("create implant: %v", err)
	}
	sleepyTask := db.Task{AgentID: sleepy, Type: "shell", Status: "running", ClaimedBy: sleepy, ClaimedAt: time.Now().Add(-30 * time.Minute)}
	// Chatty agent (default interval): a task claimed 30min ago is stale.
	chatty := "agent-chatty"
	chattyTask := db.Task{AgentID: chatty, Type: "shell", Status: "running", ClaimedBy: chatty, ClaimedAt: time.Now().Add(-30 * time.Minute)}
	for _, task := range []*db.Task{&sleepyTask, &chattyTask} {
		if err := database.Create(task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	s.requeueStaleTasks()

	var reloadedSleepy, reloadedChatty db.Task
	if err := database.First(&reloadedSleepy, sleepyTask.ID).Error; err != nil {
		t.Fatalf("load sleepy task: %v", err)
	}
	if err := database.First(&reloadedChatty, chattyTask.ID).Error; err != nil {
		t.Fatalf("load chatty task: %v", err)
	}
	if reloadedSleepy.Status != "running" {
		t.Fatalf("long-sleep agent task was requeued early: %+v", reloadedSleepy)
	}
	if reloadedChatty.Status != "pending" {
		t.Fatalf("stale chatty task was not requeued: %+v", reloadedChatty)
	}
}
