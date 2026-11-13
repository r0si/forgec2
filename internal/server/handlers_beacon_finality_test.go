package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/gorm"
)

// newBeaconFinalityTestServer builds the minimal Server that processTaskResults
// and the cancel handler need, mirroring the event test's construction.
func newBeaconFinalityTestServer(t *testing.T) (*Server, *gorm.DB) {
	t.Helper()
	database := testutil.SetupTestDB(t)
	sm, err := crypto.NewSessionManager()
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	s := &Server{
		db:                    database,
		cfg:                   &config.Config{},
		ctx:                   context.Background(),
		wsClients:             make(map[*websocket.Conn]*wsClientConn),
		sessionManager:        sm,
		eventManager:          NewEventManager(database),
		agentPendingTasks:     make(map[string]int),
		beaconDedupCache:      make(map[string]time.Time),
		screenMonitorImplants: make(map[string]time.Time),
		fileChains:            newFileChainState(),
	}
	t.Cleanup(func() { s.eventManager.Shutdown() })
	return s, database
}

// TestRelayedTaskBroadcastKeepsResultPlaintext ensures encryption at rest does
// not leak the FC2ENC storage representation into an operator shell session.
func TestRelayedTaskBroadcastKeepsResultPlaintext(t *testing.T) {
	crypto.InitLootEncryption(testStorageKeyHex)
	s, database := newBeaconFinalityTestServer(t)
	parent := "aaaa1111-5555-4333-8444-111111111111"
	child := "bbbb2222-5555-4333-8444-222222222222"
	output := `CHILD\\operator`

	if err := database.Create(&db.Implant{ID: child, Hostname: "CHILD-01", ParentID: parent}).Error; err != nil {
		t.Fatalf("create child agent: %v", err)
	}
	task := db.Task{AgentID: child, Type: "shell", Command: "whoami", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	client := &wsClientConn{ch: make(chan []byte, 1), done: make(chan struct{})}
	s.wsClients[nil] = client
	s.agentPendingTasks[child] = 1
	s.processRelayedResults([]relayedData{{
		AgentID: child,
		Results: []taskResult{{TaskID: task.ID, Type: "shell", Output: output, ResultID: "rid-plain-ws"}},
	}}, parent, time.Now())

	select {
	case raw := <-client.ch:
		var message map[string]interface{}
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode websocket message: %v", err)
		}
		if got := message["result"]; got != output {
			t.Fatalf("broadcast result = %q, want plaintext %q", got, output)
		}
		if strings.Contains(string(raw), "FC2ENC:") {
			t.Fatalf("broadcast leaked encrypted storage value: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed task broadcast")
	}

	var stored struct{ Result string }
	if err := database.Raw("SELECT result FROM tasks WHERE id = ?", task.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read raw stored task: %v", err)
	}
	if !strings.HasPrefix(stored.Result, "FC2ENC:") {
		t.Fatalf("task result is not encrypted at rest: %q", stored.Result)
	}
}

func TestBroadcastTaskUpdateDecryptsAtRestResult(t *testing.T) {
	crypto.InitLootEncryption(testStorageKeyHex)
	s, _ := newBeaconFinalityTestServer(t)
	client := &wsClientConn{ch: make(chan []byte, 1), done: make(chan struct{})}
	s.wsClients[nil] = client

	ciphertext, err := crypto.EncryptLoot(`DOMAIN\\operator`)
	if err != nil {
		t.Fatalf("encrypt task result: %v", err)
	}
	s.broadcastTaskUpdate("agent-1", db.Task{ID: 42, AgentID: "agent-1", Type: "shell", Status: "completed", Result: ciphertext})

	select {
	case raw := <-client.ch:
		var message map[string]interface{}
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode websocket message: %v", err)
		}
		if got := message["result"]; got != `DOMAIN\\operator` {
			t.Fatalf("broadcast result = %q, want plaintext", got)
		}
		if strings.Contains(string(raw), "FC2ENC:") {
			t.Fatalf("broadcast leaked encrypted storage value: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task broadcast")
	}
}

// TestTaskExecuteDurationObserved proves task finalization feeds the
// execution-duration histogram (previously declared but never observed,
// so the panel was permanently empty).
func TestTaskExecuteDurationObserved(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	s.metrics = NewMetricsCollector(s)

	uuid := "eeee5555-6666-4333-8444-555555555555"
	agent := db.Implant{ID: uuid, Hostname: "DUR-01", IP: "10.0.0.11"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "whoami", Status: "running",
		CreatedAt: time.Now().Add(-2 * time.Minute)}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	s.agentPendingTasks[uuid] = 1

	s.processTaskResults(agent, []taskResult{
		{TaskID: task.ID, Type: "shell", Output: "ok", ResultID: "rid-dur"},
	}, uuid, time.Now())

	if n := promtestutil.CollectAndCount(s.metrics.TaskExecuteDuration); n == 0 {
		t.Fatal("TaskExecuteDuration has no observation after finalization")
	}
}

// TestDedupEvictsOldestFirst proves an over-cap dedup cache evicts the
// oldest entries (previously Go-map-random, so a fresh hot key could be
// dropped while ancient ones survived).
func TestDedupEvictsOldestFirst(t *testing.T) {
	s, _ := newBeaconFinalityTestServer(t)
	now := time.Now()
	s.beaconDedupCache = make(map[string]time.Time, MaxBeaconDedupEntries+10)
	for i := 0; i < MaxBeaconDedupEntries+5; i++ {
		s.beaconDedupCache["fp-"+strconv.Itoa(i)] = now.Add(-time.Duration(MaxBeaconDedupEntries+5-i) * time.Millisecond)
	}

	req := beaconRequest{UUID: "dedup-agent", Seq: 9999}
	if s.isDuplicateBeacon(req) {
		t.Fatal("fresh beacon must not be a duplicate")
	}
	if _, ok := s.beaconDedupCache["fp-0"]; ok {
		t.Fatal("oldest entry must be evicted first")
	}
	if len(s.beaconDedupCache) > MaxBeaconDedupEntries+1 {
		t.Fatalf("cache still over cap: %d", len(s.beaconDedupCache))
	}
}

// TestCancelledTaskResultNotApplied pins the P6 finality guarantee with the
// late-result contract: a result that arrives for a task the operator already
// cancelled must not resurrect the row, but its output is preserved for the
// audit under LateResult. The cancellation reason stays in Error.
func TestCancelledTaskResultNotApplied(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	crypto.InitLootEncryption(testStorageKeyHex)

	uuid := "dddd4444-5555-4333-8444-666666666666"
	now := time.Now()
	agent := db.Implant{ID: uuid, Hostname: "WIN-DC01", IP: "10.0.0.5"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}

	task := db.Task{
		AgentID: uuid,
		Type:    "shell",
		Command: "whoami",
		Status:  "cancelled",
		Error:   "cancelled by operator",
	}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	s.agentPendingTasks[uuid] = 1

	// A result for the cancelled task arrives (agent had already executed it).
	s.processTaskResults(agent, []taskResult{
		{TaskID: task.ID, Type: "shell", Output: "administrator", Encoding: "base64", ResultID: "rid-late"},
	}, uuid, now)

	var reloaded db.Task
	if err := database.First(&reloaded, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.Status != "cancelled" {
		t.Errorf("cancelled task resurrected: status=%q", reloaded.Status)
	}
	if reloaded.Result != "administrator" {
		t.Errorf("late result not preserved: result=%q", reloaded.Result)
	}
	if reloaded.Error != "cancelled by operator" {
		t.Errorf("cancellation reason overwritten: error=%q", reloaded.Error)
	}
	if !reloaded.LateResult || reloaded.LateResultStatus != "completed" || reloaded.LateResultAt == nil {
		t.Errorf("late result metadata missing: %+v", reloaded)
	}
	if reloaded.LastResultID != "rid-late" {
		t.Errorf("late result id not recorded: %q", reloaded.LastResultID)
	}
	// The cancelled task's pending slot is NOT released here: terminal-state
	// transitions own the decrement (cancel endpoint / kill-switch disarm /
	// reject all release the slot at flip time, guarded by status conditions).
	// A result arriving for an already-cancelled task is a pure no-op —
	// decrementing again double-counted the release and could zero the
	// per-agent gate early.
	if n := s.agentPendingTasks[uuid]; n != 1 {
		t.Errorf("cancelled result must not touch the pending counter: %d", n)
	}
}

// TestTaskResultDurableDedupAcrossRestart pins the P8 durable idempotency: the
// same agent result re-sent after a server restart (in-memory dedup cache is
// gone) must be dropped based on the persisted LastResultID.
func TestTaskResultDurableDedupAcrossRestart(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)

	uuid := "eeee5555-5555-4333-8444-777777777777"
	now := time.Now()
	agent := db.Implant{ID: uuid, Hostname: "LINUX-01", IP: "10.0.0.6"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "id", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	s.agentPendingTasks[uuid] = 1

	result := taskResult{TaskID: task.ID, Type: "shell", Output: "uid=0(root)", ResultID: "rid-4242"}
	s.processTaskResults(agent, []taskResult{result}, uuid, now)

	var first db.Task
	if err := database.First(&first, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if first.Status != "completed" {
		t.Fatalf("expected completed, got %q", first.Status)
	}
	if first.LastResultID != "rid-4242" {
		t.Fatalf("expected last_result_id rid-4242, got %q", first.LastResultID)
	}
	if n := s.agentPendingTasks[uuid]; n != 0 {
		t.Fatalf("pending counter should be 0, got %d", n)
	}

	// Simulate a server restart: the in-memory dedup window is cleared.
	s.resultDedupeCache = make(map[string]time.Time)

	// The agent re-sends the same result (dropped-frame retry). With a mutated
	// payload, a naive re-apply would overwrite the stored output.
	s.processTaskResults(agent, []taskResult{
		{TaskID: task.ID, Type: "shell", Output: "EVIL-OVERWRITE", ResultID: "rid-4242"},
	}, uuid, now)

	var second db.Task
	if err := database.First(&second, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if second.Result != "uid=0(root)" {
		t.Errorf("duplicate result overwrote stored result: %q", second.Result)
	}
	if second.LastResultID != "rid-4242" {
		t.Errorf("duplicate result changed last_result_id: %q", second.LastResultID)
	}
	if n := s.agentPendingTasks[uuid]; n != 0 {
		t.Errorf("duplicate result corrupted pending counter: %d", n)
	}
}

// TestRelayedCancelledTaskResultNotApplied pins finality for P2P child results:
// a cancelled child task must stay cancelled even when the result is relayed
// through its parent.
func TestRelayedCancelledTaskResultNotApplied(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)

	parent := "aaaa1111-5555-4333-8444-111111111111"
	child := "bbbb2222-5555-4333-8444-222222222222"
	now := time.Now()

	childAgent := db.Implant{ID: child, Hostname: "CHILD-01", IP: "10.0.0.7"}
	if err := database.Create(&childAgent).Error; err != nil {
		t.Fatalf("create child agent: %v", err)
	}
	if err := database.Model(&db.Implant{}).Where("id = ?", child).Update("parent_id", parent).Error; err != nil {
		t.Fatalf("set parent: %v", err)
	}

	task := db.Task{AgentID: child, Type: "shell", Command: "whoami", Status: "cancelled", Error: "cancelled by operator"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	s.agentPendingTasks[child] = 1

	s.processRelayedResults([]relayedData{
		{AgentID: child, Results: []taskResult{
			{TaskID: task.ID, Type: "shell", Output: "child\\admin", ResultID: "rid-relayed"},
		}},
	}, parent, now)

	var reloaded db.Task
	if err := database.First(&reloaded, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.Status != "cancelled" {
		t.Errorf("relayed result resurrected cancelled task: status=%q", reloaded.Status)
	}
	if reloaded.Result != "" {
		t.Errorf("relayed result overwrote cancelled task result=%q", reloaded.Result)
	}
}

// TestCancelRunningTaskInjectsAbortTask pins the P6 abort wiring: cancelling a
// running task flips it to cancelled AND injects a high-priority "abort" task
// targeting the original task id.
func TestCancelRunningTaskInjectsAbortTask(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)

	uuid := "cccc3333-5555-4333-8444-333333333333"
	agent := db.Implant{ID: uuid, Hostname: "WS-01", IP: "10.0.0.8"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "sleep 3600", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: uuid}, {Key: "taskId", Value: strconv.FormatUint(uint64(task.ID), 10)}}
	c.Set("user", "tester")

	s.handleCancelTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", w.Code, w.Body.String())
	}

	var cancelled db.Task
	if err := database.First(&cancelled, task.ID).Error; err != nil {
		t.Fatalf("reload cancelled task: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %q", cancelled.Status)
	}

	var abortTasks []db.Task
	if err := database.Where("type = ? AND agent_id = ?", "abort", uuid).Find(&abortTasks).Error; err != nil {
		t.Fatalf("query abort tasks: %v", err)
	}
	if len(abortTasks) != 1 {
		t.Fatalf("expected exactly one abort task, got %d", len(abortTasks))
	}
	abort := abortTasks[0]
	if abort.Command != strconv.FormatUint(uint64(task.ID), 10) {
		t.Errorf("abort command=%q, want %q", abort.Command, strconv.FormatUint(uint64(task.ID), 10))
	}
	if abort.Priority != 3 {
		t.Errorf("abort priority=%d, want 3", abort.Priority)
	}
	if abort.Status != "pending" {
		t.Errorf("abort status=%q, want pending", abort.Status)
	}
	if abort.ClaimedBy != uuid {
		t.Errorf("abort claimed_by=%q, want %q", abort.ClaimedBy, uuid)
	}
}

// TestCancelRunningTaskSkipsAbortWhenQueueFull verifies the abort injection
// respects the per-agent pending cap INCLUDING its reserved headroom: with a
// queue at/above cap+reserve the cancel still succeeds but no abort task is
// injected (and the counter is untouched).
func TestCancelRunningTaskSkipsAbortWhenQueueFull(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)

	uuid := "dddd4444-5555-4333-8444-444444444444"
	agent := db.Implant{ID: uuid, Hostname: "WS-02", IP: "10.0.0.9"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "sleep 3600", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Simulate a drifted counter above cap+reserve: cancel releases one slot
	// (back to exactly cap+reserve), so even the reserved abort injection
	// must defer instead of pushing past the ceiling.
	s.agentPendingTasks[uuid] = MaxPendingTasksPerAgent + AbortReserveSlots + 1

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: uuid}, {Key: "taskId", Value: strconv.FormatUint(uint64(task.ID), 10)}}
	c.Set("user", "tester")

	s.handleCancelTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", w.Code, w.Body.String())
	}
	var abortCount int64
	if err := database.Model(&db.Task{}).Where("type = ? AND agent_id = ?", "abort", uuid).Count(&abortCount).Error; err != nil {
		t.Fatalf("count abort tasks: %v", err)
	}
	if abortCount != 0 {
		t.Errorf("full queue injected %d abort tasks, want 0", abortCount)
	}
	// Cancel released exactly one slot; the skipped abort added none back.
	if n := s.agentPendingTasks[uuid]; n != MaxPendingTasksPerAgent+AbortReserveSlots {
		t.Errorf("counter drifted: got %d, want %d", n, MaxPendingTasksPerAgent+AbortReserveSlots)
	}
}

// TestCancelRunningTaskUsesAbortReserve verifies the reserved headroom: with
// the queue exactly at the normal cap, cancelling a running task still queues
// its abort and reports outcome=abort_queued.
func TestCancelRunningTaskUsesAbortReserve(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)

	uuid := "eeee5555-5555-4333-8444-555555555555"
	agent := db.Implant{ID: uuid, Hostname: "WS-03", IP: "10.0.0.10"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "sleep 3600", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	s.agentPendingTasks[uuid] = MaxPendingTasksPerAgent

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: uuid}, {Key: "taskId", Value: strconv.FormatUint(uint64(task.ID), 10)}}
	c.Set("user", "tester")

	s.handleCancelTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"outcome":"abort_queued"`) {
		t.Fatalf("expected outcome=abort_queued, body=%s", w.Body.String())
	}
	var abortCount int64
	if err := database.Model(&db.Task{}).Where("type = ? AND agent_id = ?", "abort", uuid).Count(&abortCount).Error; err != nil {
		t.Fatalf("count abort tasks: %v", err)
	}
	if abortCount != 1 {
		t.Fatalf("reserved slot injected %d abort tasks, want 1", abortCount)
	}
}

// TestCancelPendingTaskDoesNotInjectAbort verifies a pending (not yet fetched)
// task is simply cancelled without an abort task, since no execution runs.
func TestCancelPendingTaskDoesNotInjectAbort(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)

	uuid := "cccc3333-5555-4333-8444-333333333333"
	agent := db.Implant{ID: uuid, Hostname: "WS-01", IP: "10.0.0.8"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := db.Task{AgentID: uuid, Type: "shell", Command: "whoami", Status: "pending"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: uuid}, {Key: "taskId", Value: strconv.FormatUint(uint64(task.ID), 10)}}
	c.Set("user", "tester")

	s.handleCancelTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", w.Code, w.Body.String())
	}

	var cancelled db.Task
	if err := database.First(&cancelled, task.ID).Error; err != nil {
		t.Fatalf("reload cancelled task: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %q", cancelled.Status)
	}

	var abortCount int64
	if err := database.Model(&db.Task{}).Where("type = ? AND agent_id = ?", "abort", uuid).Count(&abortCount).Error; err != nil {
		t.Fatalf("count abort tasks: %v", err)
	}
	if abortCount != 0 {
		t.Errorf("pending cancel injected %d abort tasks, want 0", abortCount)
	}
}
