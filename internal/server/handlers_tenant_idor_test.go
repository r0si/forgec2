package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
)

func newIDORTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := config.DefaultConfig()
	cfg.Server.DataDir = t.TempDir()
	s := &Server{db: testutil.SetupTestDB(t), cfg: cfg, ctx: ctx}
	s.agentPendingTasks = make(map[string]int)
	s.metrics = NewMetricsCollector(s)
	return s
}

func seedIDORTenants(t *testing.T, s *Server) {
	t.Helper()
	users := []db.User{
		{Username: "alice-op", Role: "admin", TenantID: 1, IsActive: true},
		{Username: "bob-op", Role: "admin", TenantID: 2, IsActive: true},
	}
	if err := s.db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	agents := []db.Implant{
		{ID: "agent-a", TenantID: 1, Hostname: "ALPHA", Status: "online"},
		{ID: "agent-b", TenantID: 2, Hostname: "BRAVO", Status: "online"},
	}
	if err := s.db.Create(&agents).Error; err != nil {
		t.Fatalf("seed agents: %v", err)
	}
}

// opCtx builds an authenticated operator request context.
func opCtx(method, target, user string, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r *strings.Reader
	if body == "" {
		r = strings.NewReader("")
	} else {
		r = strings.NewReader(body)
	}
	c.Request, _ = http.NewRequest(method, target, r)
	if body != "" {
		ct := "application/x-www-form-urlencoded"
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			ct = "application/json"
		}
		c.Request.Header.Set("Content-Type", ct)
	}
	if user != "" {
		c.Set("user", user)
	}
	c.Set("user_role", "admin")
	c.Params = params
	return c, w
}

func seedPendingTask(t *testing.T, s *Server, agentID string) db.Task {
	t.Helper()
	task := db.Task{AgentID: agentID, Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval, CreatedBy: "ai"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task
}

// ── A1: agent task reads, deletes, batch ─────────────────────────────────

func TestTenantIDOR_GetAgentTasks(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)

	c, w := opCtx(http.MethodGet, "/x", "alice-op", "", gin.Params{{Key: "id", Value: "agent-b"}})
	s.handleGetAgentTasks(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant task list: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}

	c, w = opCtx(http.MethodGet, "/x", "alice-op", "", gin.Params{{Key: "id", Value: "agent-a"}})
	s.handleGetAgentTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own task list: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestTenantIDOR_DeleteAgent(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)

	c, w := opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "id", Value: "agent-b"}})
	s.handleDeleteAgent(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
	var n int64
	s.db.Model(&db.Implant{}).Where("id = ?", "agent-b").Count(&n)
	if n != 1 {
		t.Fatalf("cross-tenant delete removed foreign agent: count=%d", n)
	}

	c, w = opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "id", Value: "agent-a"}})
	s.handleDeleteAgent(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own delete: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestTenantIDOR_BulkDeleteFailClosed(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)

	c, w := opCtx(http.MethodPost, "/x", "alice-op", `{"agent_ids":["agent-a","agent-b"]}`, nil)
	s.handleBulkDeleteAgents(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("mixed bulk delete: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
	var n int64
	s.db.Model(&db.Implant{}).Count(&n)
	if n != 2 {
		t.Fatalf("mixed bulk delete removed agents: count=%d", n)
	}
}

func TestTenantIDOR_BatchCommandSkipsForeign(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)

	c, w := opCtx(http.MethodPost, "/x", "alice-op", `{"agent_ids":["agent-a","agent-b"],"task_type":"shell","command":"whoami"}`, nil)
	s.handleBatchCommand(c)
	if w.Code != http.StatusOK {
		t.Fatalf("batch command: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var foreign, own int64
	s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-b").Count(&foreign)
	s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-a").Count(&own)
	if foreign != 0 {
		t.Fatalf("batch queued task on foreign agent: count=%d", foreign)
	}
	if own != 1 {
		t.Fatalf("batch missed own agent: count=%d", own)
	}
}

// ── A2: loot ─────────────────────────────────────────────────────────────

func seedLootKeylog(t *testing.T, s *Server, agentID string) uint {
	t.Helper()
	task := db.Task{AgentID: agentID, Type: "keylogger_dump", Status: "completed", Result: "keys"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed keylog: %v", err)
	}
	return task.ID
}

func TestTenantIDOR_LootBulkDelete(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	ownID := seedLootKeylog(t, s, "agent-a")
	foreignID := seedLootKeylog(t, s, "agent-b")

	c, w := opCtx(http.MethodPost, "/x", "alice-op", fmt.Sprintf(`{"ids":["keylog:%d"]}`, foreignID), nil)
	s.handleLootBulkDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("loot delete: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"deleted":0`) {
		t.Fatalf("cross-tenant loot delete removed foreign row: body=%s", w.Body.String())
	}
	var n int64
	s.db.Model(&db.Task{}).Where("id = ?", foreignID).Count(&n)
	if n != 1 {
		t.Fatalf("foreign keylog row gone: count=%d", n)
	}

	c, w = opCtx(http.MethodPost, "/x", "alice-op", fmt.Sprintf(`{"ids":["keylog:%d"]}`, ownID), nil)
	s.handleLootBulkDelete(c)
	if !strings.Contains(w.Body.String(), `"deleted":1`) {
		t.Fatalf("own loot delete should remove 1: body=%s", w.Body.String())
	}
}

func TestTenantIDOR_ServeScreenshot(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	dir := filepath.Join(s.cfg.Server.DataDir, "screenshots", "agent-b")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("fakepng"), 0600); err != nil {
		t.Fatalf("write shot: %v", err)
	}

	c, w := opCtx(http.MethodGet, "/x", "alice-op", "", gin.Params{
		{Key: "agent_id", Value: "agent-b"},
		{Key: "filename", Value: "shot.png"},
	})
	s.handleServeScreenshot(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant screenshot: expected 404, got %d", w.Code)
	}
}

func TestTenantIDOR_LootPageFiltersScreenshots(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	for _, aid := range []string{"agent-a", "agent-b"} {
		dir := filepath.Join(s.cfg.Server.DataDir, "screenshots", aid)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("fakepng"), 0600); err != nil {
			t.Fatalf("write shot: %v", err)
		}
	}

	c, w := opCtx(http.MethodGet, "/x", "alice-op", "", nil)
	s.handleLootPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("loot page: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "agent-a") {
		t.Fatalf("own screenshots missing from loot page")
	}
	if strings.Contains(body, "agent-b") {
		t.Fatalf("foreign screenshots leaked into loot page")
	}
}

// ── A3: approvals / collab ───────────────────────────────────────────────

func TestTenantIDOR_ApproveReject(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	foreign := seedPendingTask(t, s, "agent-b")
	own := seedPendingTask(t, s, "agent-a")

	c, w := opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "taskId", Value: fmt.Sprint(foreign.ID)}})
	s.handleApproveTask(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant approve: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
	var st db.Task
	if err := s.db.Select("status").First(&st, foreign.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if st.Status != TaskStatusPendingApproval {
		t.Fatalf("cross-tenant approve flipped status: %q", st.Status)
	}

	c, w = opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "taskId", Value: fmt.Sprint(foreign.ID)}})
	s.handleRejectTask(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant reject: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}

	c, w = opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "taskId", Value: fmt.Sprint(own.ID)}})
	s.handleApproveTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own approve: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestTenantIDOR_CollabClaim(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	foreign := seedPendingTask(t, s, "agent-b")

	c, w := opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "taskId", Value: fmt.Sprint(foreign.ID)}})
	s.handleCollabClaimTask(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant claim: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
	var claimed string
	s.db.Model(&db.Task{}).Where("id = ?", foreign.ID).Pluck("claimed_by", &claimed)
	if claimed != "" {
		t.Fatalf("cross-tenant claim wrote claimed_by=%q", claimed)
	}
}

func TestTenantIDOR_CollabLock(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)

	c, w := opCtx(http.MethodPost, "/x", "alice-op", "", gin.Params{{Key: "id", Value: "agent-b"}})
	s.handleCollabLock(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant lock: expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
	var n int64
	s.db.Model(&db.AgentLock{}).Where("agent_id = ?", "agent-b").Count(&n)
	if n != 0 {
		t.Fatalf("cross-tenant lock created row")
	}
}

// ── A4: listeners + AI ───────────────────────────────────────────────────

func TestTenantIDOR_ListenerDetailFiltersAgents(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	lis := db.Listener{Name: "main", Scheme: "http", Host: "0.0.0.0", Port: 8443, TenantID: 1}
	if err := s.db.Create(&lis).Error; err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	s.db.Model(&db.Implant{}).Where("id IN ?", []string{"agent-a", "agent-b"}).Update("listener_id", lis.ID)

	c, w := opCtx(http.MethodGet, "/x", "alice-op", "", gin.Params{{Key: "id", Value: fmt.Sprint(lis.ID)}})
	s.handleAPIGetListener(c)
	if w.Code != http.StatusOK {
		t.Fatalf("listener detail: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Agents []db.Implant `json:"agents"`
		Total  int          `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 || len(resp.Agents) != 1 || resp.Agents[0].ID != "agent-a" {
		t.Fatalf("listener leaked foreign agents: total=%d %+v", resp.Total, resp.Agents)
	}

	// New semantics (H3): unowned (tenant-0) listener rows are directly
	// invisible to scoped operators — not just agent-filtered.
	legacy := db.Listener{Name: "legacy", Scheme: "http", Host: "0.0.0.0", Port: 18443}
	if err := s.db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy listener: %v", err)
	}
	c2, w2 := opCtx(http.MethodGet, "/x", "alice-op", "", gin.Params{{Key: "id", Value: fmt.Sprint(legacy.ID)}})
	s.handleAPIGetListener(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("legacy listener detail: expected 404, got %d; body=%s", w2.Code, w2.Body.String())
	}
}

func TestTenantIDOR_AITools(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)
	seedPendingTask(t, s, "agent-a")
	foreign := seedPendingTask(t, s, "agent-b")
	aliceCtx := &aiReqCtx{Principal: aiPrincipal{UserID: 10, Username: "alice-op", TenantID: 1}}

	// list_pending_tasks must not surface the foreign task.
	out := s.executeToolSwitchCtx(aliceCtx, "list_pending_tasks", `{"limit":50}`)
	if strings.Contains(out, fmt.Sprintf(`"id":%d`, foreign.ID)) {
		t.Fatalf("AI list_pending_tasks leaked foreign task: %s", out)
	}

	// bulk cancel of the foreign task must report not-found and change nothing.
	out = s.executeToolSwitchCtx(aliceCtx, "bulk_task_action", fmt.Sprintf(`{"task_ids":[%d],"action":"cancel"}`, foreign.ID))
	if !strings.Contains(out, "not found") {
		t.Fatalf("AI bulk cancel should report not-found: %s", out)
	}
	var st db.Task
	if err := s.db.Select("status").First(&st, foreign.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if st.Status != TaskStatusPendingApproval {
		t.Fatalf("AI bulk cancel flipped foreign task: %q", st.Status)
	}
}
