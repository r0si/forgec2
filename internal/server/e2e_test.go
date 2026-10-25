package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/forgec2/forgec2/pkg/encoding"
	"github.com/gin-gonic/gin"
)

func TestE2E_Smoke_HealthEndpoint(t *testing.T) {
	db := testutil.SetupTestDB(t)
	s := &Server{db: db}
	gin.SetMode(gin.TestMode)

	t.Run("health returns ok", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/health", nil)
		// The route serves handleHealthCheck (routes_public.go); handleHealth
		// was a near-duplicate that this test alone kept alive.
		s.handleHealthCheck(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		testutil.AssertValidJSON(t, w.Body.Bytes(), "health")
	})

	t.Run("ready returns ok", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/ready", nil)
		// Was calling handleHealth, so this subtest asserted the health
		// payload and never exercised readiness at all.
		s.handleReadyCheck(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		testutil.AssertValidJSON(t, w.Body.Bytes(), "ready")
	})
}

func TestE2E_Smoke_ListEndpoints(t *testing.T) {
	db := testutil.SetupTestDB(t)
	s := &Server{db: db}
	gin.SetMode(gin.TestMode)

	t.Run("list agents returns empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents", nil)
		s.handleListAgents(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		testutil.AssertKeyExists(t, w.Body.Bytes(), "list agents", "agents")
	})

	t.Run("list listeners returns empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/listeners", nil)
		s.handleListListeners(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		testutil.AssertKeyExists(t, w.Body.Bytes(), "list listeners", "listeners")
	})

	t.Run("dashboard heatmap returns ok", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/activity-heatmap?range=24h", nil)
		s.handleDashboardActivityHeatmap(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		testutil.AssertValidJSON(t, w.Body.Bytes(), "dashboard heatmap")
	})
}

// TestE2E_Beacon_Task_ResultFlow tests the full v2 lifecycle:
// 1. Agent registers via beacon
// 2. Server assigns a task
// 3. Agent receives task on next beacon
// 4. Agent submits result
// 5. Server stores result and marks task completed
func TestE2E_Beacon_Task_ResultFlow(t *testing.T) {
	s, database := v2TestServer(t)
	r := s.router

	agentUUID := "11111111-2222-4333-8444-555555555555"
	agent := v3TestAgent(t, s, agentUUID)

	// Helper: clear dedup cache so each beacon is processed
	clearDedup := func() {
		s.beaconDedupMu.Lock()
		s.beaconDedupCache = make(map[string]time.Time)
		s.beaconDedupMu.Unlock()
	}

	// Helper: send a beacon frame and return the raw response body
	sendFrame := func(t *testing.T, frame string) *httptest.ResponseRecorder {
		t.Helper()
		clearDedup()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/beacon", strings.NewReader(frame))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("beacon: expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		return w
	}

	// Helper: decode an encrypted beacon response (AAD = uuid||last frame seq)
	decodeEncResp := func(t *testing.T, w *httptest.ResponseRecorder) beaconResponse {
		t.Helper()
		var encResp struct {
			CipherB64 string `json:"c"`
		}
		if err := encoding.Unmarshal(w.Body.Bytes(), &encResp); err != nil {
			t.Fatalf("failed to parse encrypted response: %v (body=%s)", err, w.Body.String())
		}
		plain, err := agent.decryptWithAAD(encResp.CipherB64, agent.aad(agent.seq))
		if err != nil {
			t.Fatalf("failed to decrypt response: %v", err)
		}
		var resp beaconResponse
		if err := encoding.Unmarshal(plain, &resp); err != nil {
			t.Fatalf("failed to unmarshal beacon response: %v", err)
		}
		return resp
	}

	// Step 1: Registration (v2 auth frame, binds identity, establishes session)
	t.Run("step1_agent_registers", func(t *testing.T) {
		w := v2Post(t, s, agent.registerFrame())
		if w.Code != http.StatusOK {
			t.Fatalf("registration: expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var regResp struct {
			Seq     uint64 `json:"seq"`
			RegOK   bool   `json:"reg_ok"`
			ECDHPub string `json:"ecdh_pub"`
			Mac     string `json:"mac"`
		}
		if err := encoding.Unmarshal(w.Body.Bytes(), &regResp); err != nil {
			t.Fatalf("register response parse: %v", err)
		}
		if !agent.verifyResponseMAC(regResp.Seq, regResp.ECDHPub, regResp.Mac) {
			t.Fatalf("register response MAC mismatch")
		}
		if err := agent.establishFromServerKey(regResp.ECDHPub); err != nil {
			t.Fatalf("establish session: %v", err)
		}

		// Verify agent was created
		var agentRow db.Implant
		if err := database.Where("id = ?", agentUUID).First(&agentRow).Error; err != nil {
			t.Fatalf("agent not found in DB: %v", err)
		}
		if !agentRow.Registered {
			t.Errorf("agent must be registered=true, got false")
		}
	})

	// Step 2: Create a task for the agent via direct DB insert
	var taskID uint
	t.Run("step2_create_task", func(t *testing.T) {
		task := &db.Task{
			AgentID: agentUUID,
			Type:    "shell",
			Command: "whoami",
			Status:  "pending",
		}
		if err := database.Create(task).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
		taskID = task.ID
		if taskID == 0 {
			t.Fatal("task ID should be > 0 after create")
		}
	})

	// Step 3: Agent beacons again — should receive the pending task
	var resp3 beaconResponse
	t.Run("step3_agent_receives_task", func(t *testing.T) {
		inner, _ := json.Marshal(map[string]interface{}{
			"uuid": agentUUID,
			"pv":   2,
			"info": map[string]string{"hostname": "TESTPC", "username": "testuser", "ip": "10.0.0.1"},
		})
		w := sendFrame(t, agent.encryptedFrame(inner))
		resp3 = decodeEncResp(t, w)

		if len(resp3.Tasks) == 0 {
			t.Fatal("expected at least 1 task, got 0")
		}
		found := false
		for _, task := range resp3.Tasks {
			if task.ID == taskID {
				found = true
				if task.Type != "shell" {
					t.Errorf("task type = %q, want shell", task.Type)
				}
			}
		}
		if !found {
			t.Errorf("task %d not found in response tasks (got %v)", taskID, resp3.Tasks)
		}
	})

	// Step 4: Agent submits result
	t.Run("step4_agent_submits_result", func(t *testing.T) {
		resultsJSON, _ := json.Marshal([]taskResult{{
			TaskID: taskID,
			Type:   "shell",
			Output: "testdomain\\testuser",
		}})
		inner, _ := json.Marshal(map[string]interface{}{
			"uuid":    agentUUID,
			"pv":      2,
			"results": json.RawMessage(resultsJSON),
			"acks":    []uint{taskID},
		})
		sendFrame(t, agent.encryptedFrame(inner))
	})

	// Step 5: Verify result was stored
	t.Run("step5_verify_result_stored", func(t *testing.T) {
		var task db.Task
		if err := database.Where("id = ?", taskID).First(&task).Error; err != nil {
			t.Fatalf("task not found: %v", err)
		}
		if task.Status != "completed" {
			t.Errorf("task status = %q, want completed", task.Status)
		}
		if task.Result != "testdomain\\testuser" {
			t.Errorf("task result = %q, want testdomain\\testuser", task.Result)
		}
	})

	// Step 6: Second beacon returns empty tasks (already completed)
	t.Run("step6_no_more_tasks", func(t *testing.T) {
		inner, _ := json.Marshal(map[string]interface{}{"uuid": agentUUID, "pv": 2})
		w := sendFrame(t, agent.encryptedFrame(inner))
		resp := decodeEncResp(t, w)
		if len(resp.Tasks) != 0 {
			t.Errorf("expected 0 tasks, got %d", len(resp.Tasks))
		}
	})

	// Step 7: Protocol version rejection — v1 plaintext frames are rejected outright.
	t.Run("step7_protocol_version_rejection", func(t *testing.T) {
		beaconJSON := `{"uuid":"55555555-6666-4333-8444-777777777777","pv":1,"info":{"hostname":"old"}}`
		clearDedup()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/beacon", strings.NewReader(beaconJSON))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("v1 plaintext frame must be rejected (breaking change), got %d; body=%s", w.Code, w.Body.String())
		}
	})
}
