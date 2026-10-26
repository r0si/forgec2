package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

// smTest returns a fresh session manager for tests.
func smTest(t testing.TB) *crypto.SessionManager {
	t.Helper()
	sm, err := crypto.NewSessionManager()
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	return sm
}

// batchTestServer builds a Server with the fields handleBatchCommand and
// handleKillSwitch rely on (metrics + in-memory pending counts).
func batchTestServer(t testing.TB) (*Server, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	s := &Server{
		db:                database,
		cfg:               &config.Config{},
		sessionManager:    smTest(t),
		beaconDedupCache:  make(map[string]time.Time),
		eventManager:      NewEventManager(database),
		socksEngine:       newSocksRelayEngine(),
		agentPendingTasks: make(map[string]int),
		wsClients:         make(map[*websocket.Conn]*wsClientConn),
		ctx:               context.Background(),
		metrics:           NewMetricsCollector(&Server{}),
	}
	return s, database
}

// TestBatchCommandEnforcesCommandLengthGate verifies a batch command with an
// oversized command is rejected outright (same MaxCommandLength gate as the
// single-task path).
func TestBatchCommandRejectsOversizedCommand(t *testing.T) {
	s, _ := batchTestServer(t)
	s.db.Create(&db.Implant{ID: "b1", Hostname: "H", Status: "online"})

	body := `{"agent_ids":["b1"],"task_type":"shell","command":"` + strings.Repeat("A", MaxCommandLength+1) + `"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/batch/command", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchCommand(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized command, got %d; body=%s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.Task{}).Count(&count)
	if count != 0 {
		t.Fatalf("no tasks should be created for rejected batch, got %d", count)
	}
}

// TestBatchCommandHonorsPerAgentPendingGate verifies the bulk path enforces the
// same per-agent pending ceiling as createTask (K5): an agent already at the
// limit is skipped instead of overloaded.
func TestBatchCommandHonorsPerAgentPendingGate(t *testing.T) {
	s, database := batchTestServer(t)
	agentID := "batch-overload-1"
	database.Create(&db.Implant{ID: agentID, Hostname: "H", Status: "online"})
	database.Create(&db.Task{AgentID: agentID, Type: "shell", Command: "x", Status: "pending"})
	s.agentPendingTasksMu.Lock()
	s.agentPendingTasks[agentID] = MaxPendingTasksPerAgent
	s.agentPendingTasksMu.Unlock()

	body := `{"agent_ids":["` + agentID + `"],"task_type":"shell","command":"whoami"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/tasks/command", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchCommand(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		TasksCreated int `json:"tasks_created"`
		Failed       int `json:"failed"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.TasksCreated != 0 || resp.Failed != 1 {
		t.Fatalf("expected agent skipped (0 created, 1 failed), got created=%d failed=%d", resp.TasksCreated, resp.Failed)
	}
	s.agentPendingTasksMu.Lock()
	pending := s.agentPendingTasks[agentID]
	s.agentPendingTasksMu.Unlock()
	if pending != MaxPendingTasksPerAgent {
		t.Fatalf("pending counter must not change for skipped agent, got %d", pending)
	}
}

// TestBatchCommandAcceptsWithinLimit verifies the gate still allows a healthy
// agent through and the per-agent counter is incremented.
func TestBatchCommandAcceptsWithinLimit(t *testing.T) {
	s, _ := batchTestServer(t)
	s.db.Create(&db.Implant{ID: "b2", Hostname: "H", Status: "online"})

	body := `{"agent_ids":["b2"],"task_type":"shell","command":"whoami"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/tasks/command", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchCommand(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.Task{}).Where("agent_id = ?", "b2").Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 task, got %d", count)
	}
}

// TestKillSwitchDisarmReclaimsUninstallTasks verifies disarming the kill switch
// cancels uninstall tasks that were dispatched at arm time but never executed,
// and rolls back the per-agent pending counters.
func TestKillSwitchDisarmReclaimsUninstallTasks(t *testing.T) {
	s, database := batchTestServer(t)

	agentID := "emergency-1"
	database.Create(&db.Implant{ID: agentID, Hostname: "H", Status: "online"})
	database.Create(&db.Task{AgentID: agentID, Type: "uninstall", Command: "", Status: "pending"})

	hashBytes, err := middleware.HashPassword("killpw")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := db.User{Username: "op", PasswordHash: hashBytes}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	s.agentPendingTasksMu.Lock()
	s.agentPendingTasks[agentID] = 1
	s.agentPendingTasksMu.Unlock()

	body := `{"action":"disarm","password":"killpw"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/killswitch", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", user.ID)
	s.handleKillSwitch(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Reclaimed int `json:"reclaimed"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Reclaimed != 1 {
		t.Fatalf("expected 1 reclaimed uninstall task, reclaimed=%d; body=%s", resp.Reclaimed, w.Body.String())
	}

	var task db.Task
	if err := database.Where("type = ?", "uninstall").First(&task).Error; err != nil {
		t.Fatalf("uninstall task missing: %v", err)
	}
	if task.Status != "cancelled" {
		t.Fatalf("expected uninstall task cancelled, got %q", task.Status)
	}
	s.agentPendingTasksMu.Lock()
	pending := s.agentPendingTasks[agentID]
	s.agentPendingTasksMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending counter not rolled back, got %d", pending)
	}
}

// TestKillSwitchArmBatchesFleetUninstall verifies arming the kill switch fans
// out one uninstall task per implant in a single batched insert: every task
// inherits its own agent's tenant (loaded up front, no per-agent re-SELECT),
// a saturated agent is skipped without touching its counter, and the armed
// state is persisted alongside the dispatch.
func TestKillSwitchArmBatchesFleetUninstall(t *testing.T) {
	s, database := batchTestServer(t)

	agents := []db.Implant{
		{ID: "ks-arm-1", Hostname: "H1", Status: "online", TenantID: 1},
		{ID: "ks-arm-2", Hostname: "H2", Status: "online", TenantID: 0},
		{ID: "ks-arm-full", Hostname: "H3", Status: "online", TenantID: 1},
	}
	if err := database.Create(&agents).Error; err != nil {
		t.Fatalf("seed implants: %v", err)
	}
	s.agentPendingTasksMu.Lock()
	s.agentPendingTasks["ks-arm-full"] = MaxPendingTasksPerAgent
	s.agentPendingTasksMu.Unlock()

	hashBytes, err := middleware.HashPassword("killpw")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := db.User{Username: "op", PasswordHash: hashBytes}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	body := `{"action":"arm","password":"killpw"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/killswitch", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", user.ID)
	s.handleKillSwitch(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Dispatched int `json:"tasks_dispatched"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Dispatched != 2 {
		t.Fatalf("expected 2 dispatched (one agent saturated), got %d; body=%s", resp.Dispatched, w.Body.String())
	}

	var ks db.KillSwitch
	if err := database.First(&ks, 1).Error; err != nil || !ks.Armed {
		t.Fatalf("kill switch not armed: err=%v armed=%v", err, ks.Armed)
	}

	var tasks []db.Task
	if err := database.Where("type = ?", "uninstall").Find(&tasks).Error; err != nil {
		t.Fatalf("load uninstall tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 uninstall tasks, got %d", len(tasks))
	}
	wantTenant := map[string]uint{"ks-arm-1": 1, "ks-arm-2": 0}
	for _, task := range tasks {
		if task.Status != "pending" {
			t.Fatalf("task for %s: expected pending, got %q", task.AgentID, task.Status)
		}
		if got, ok := wantTenant[task.AgentID]; !ok {
			t.Fatalf("unexpected task for agent %s", task.AgentID)
		} else if task.TenantID != got {
			t.Fatalf("task for %s: tenant %d, want %d", task.AgentID, task.TenantID, got)
		}
	}
	for _, id := range []string{"ks-arm-1", "ks-arm-2"} {
		s.agentPendingTasksMu.Lock()
		pending := s.agentPendingTasks[id]
		s.agentPendingTasksMu.Unlock()
		if pending != 1 {
			t.Fatalf("pending counter for %s: got %d, want 1", id, pending)
		}
	}
	s.agentPendingTasksMu.Lock()
	full := s.agentPendingTasks["ks-arm-full"]
	s.agentPendingTasksMu.Unlock()
	if full != MaxPendingTasksPerAgent {
		t.Fatalf("saturated agent counter changed: got %d, want %d", full, MaxPendingTasksPerAgent)
	}
}
