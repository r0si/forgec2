package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// setupToolExecServer builds a Server whose in-memory DB has every model the
// read-only AI tools touch. The tenant operator below sees tenant 1 only.
func setupToolExecServer(t *testing.T) *Server {
	t.Helper()
	key := strings.Repeat("24", 32)
	forgecrypto.InitLootEncryption(key)
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(
		&db.User{}, &db.Implant{}, &db.Task{}, &db.Listener{}, &db.Alert{}, &db.CredentialEntry{},
		&db.CommandMacro{},
		&db.AIChatSession{}, &db.AIChatMessage{}, &db.AIChatRun{}, &db.AIChatRunEvent{},
		&db.AIExecutionIntent{}, &db.AIProviderProfile{}, &db.AIAttachment{},
		&db.AIKnowledgeCollection{}, &db.AIKnowledgeSource{}, &db.AIKnowledgeChunk{},
	); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Crypto.LootKey = key
	return &Server{db: database, cfg: cfg, aiRuns: newAIRunBroker(), agentPendingTasks: make(map[string]int)}
}

func tenantOperatorCtx() *aiReqCtx {
	return &aiReqCtx{Principal: aiPrincipal{UserID: 10, Username: "alice", TenantID: 1, Role: db.RoleAdmin}}
}

func seedToolExecAgents(t *testing.T, s *Server) {
	t.Helper()
	now := time.Now()
	rows := []db.Implant{
		{ID: "a-win", TenantID: 1, Hostname: "WIN-DC01", IP: "10.0.0.5", OS: "windows", Username: "admin",
			Domain: "CORP", Status: "online", Elevated: true, Integrity: "high", CurrentInterval: 60, CurrentJitter: 20,
			LastSeen: now},
		{ID: "a-linux", TenantID: 1, Hostname: "linux-web", IP: "10.0.0.6", OS: "linux", Username: "www-data",
			Status: "offline", LastSeen: now.Add(-48 * time.Hour)},
		{ID: "a-other", TenantID: 2, Hostname: "other-tenant", IP: "10.9.9.9", OS: "windows", Status: "online", LastSeen: now},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
}

func TestAIToolListAgentsFilters(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)
	ctx := tenantOperatorCtx()

	var all struct {
		Count  int `json:"count"`
		Agents []struct {
			ID        string `json:"id"`
			Domain    string `json:"domain"`
			Integrity string `json:"integrity"`
			SleepS    int    `json:"sleep_s"`
			Stale     bool   `json:"stale"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_agents", `{}`)), &all); err != nil {
		t.Fatalf("decode list_agents: %v", err)
	}
	if all.Count != 2 {
		t.Fatalf("count = %d, want only the 2 tenant-1 agents", all.Count)
	}

	var online struct {
		Agents []struct{ ID string } `json:"agents"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_agents", `{"status":"ONLINE"}`)), &online); err != nil {
		t.Fatalf("decode online: %v", err)
	}
	if len(online.Agents) != 1 || online.Agents[0].ID != "a-win" {
		t.Fatalf("online filter = %+v", online.Agents)
	}

	var elevated struct {
		Agents []struct{ ID string } `json:"agents"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_agents", `{"elevated":true}`)), &elevated); err != nil {
		t.Fatalf("decode elevated: %v", err)
	}
	if len(elevated.Agents) != 1 || elevated.Agents[0].ID != "a-win" {
		t.Fatalf("elevated filter = %+v", elevated.Agents)
	}

	var byOS struct {
		Agents []struct{ ID string } `json:"agents"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_agents", `{"os":"linux"}`)), &byOS); err != nil {
		t.Fatalf("decode os: %v", err)
	}
	if len(byOS.Agents) != 1 || byOS.Agents[0].ID != "a-linux" {
		t.Fatalf("os filter = %+v", byOS.Agents)
	}

	var byQuery struct {
		Agents []struct{ ID string } `json:"agents"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_agents", `{"query":"DC01"}`)), &byQuery); err != nil {
		t.Fatalf("decode query: %v", err)
	}
	if len(byQuery.Agents) != 1 || byQuery.Agents[0].ID != "a-win" {
		t.Fatalf("query filter = %+v", byQuery.Agents)
	}

	// The limit clamp keeps a hostile/looping model from asking for the fleet.
	var clamped struct {
		Agents []struct{ ID string } `json:"agents"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_agents", `{"limit":10000}`)), &clamped); err != nil {
		t.Fatalf("decode clamped: %v", err)
	}
	if len(clamped.Agents) != 2 {
		t.Fatalf("clamped limit = %+v", clamped.Agents)
	}
}

func TestAIToolGetAgentDetailAndTasks(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)
	ctx := tenantOperatorCtx()
	s.db.Create(&db.Task{AgentID: "a-win", Type: "shell", Command: "whoami", Status: "completed", Result: "corp\\admin"})
	s.db.Create(&db.Task{AgentID: "a-win", Type: "ps", Status: "pending"})

	detail := s.executeToolSwitchCtx(ctx, "get_agent_detail", `{"agent_id":"a-win"}`)
	var got struct {
		ID         string `json:"id"`
		TaskCount  int64  `json:"task_count"`
		Domain     string `json:"domain"`
		Integrity  string `json:"integrity"`
		RecentTask []struct {
			Type string `json:"type"`
		} `json:"recent_tasks"`
	}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("decode detail: %v (%s)", err, detail)
	}
	if got.ID != "a-win" || got.TaskCount != 2 || got.Domain != "CORP" || got.Integrity != "high" {
		t.Fatalf("detail mismatch: %+v", got)
	}
	if len(got.RecentTask) != 2 {
		t.Fatalf("recent_tasks = %+v, want both tasks", got.RecentTask)
	}

	if out := s.executeToolSwitchCtx(ctx, "get_agent_detail", `{"agent_id":"nope"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("unknown agent detail = %s", out)
	}
	// Another tenant's agent must be invisible even when named directly.
	if out := s.executeToolSwitchCtx(ctx, "get_agent_detail", `{"agent_id":"a-other"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("cross-tenant detail leaked: %s", out)
	}

	tasks := s.executeToolSwitchCtx(ctx, "get_agent_tasks", `{"agent_id":"a-win","limit":10}`)
	if !strings.Contains(tasks, "whoami") || !strings.Contains(tasks, "completed") {
		t.Fatalf("get_agent_tasks = %s", tasks)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_agent_tasks", `{"agent_id":"nope"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("unknown agent tasks = %s", out)
	}
}

func TestAIToolListenerCredentialOperatorLists(t *testing.T) {
	s := setupToolExecServer(t)
	ctx := tenantOperatorCtx()
	s.db.Create(&db.Listener{TenantID: 1, Name: "https-443", Type: "https", Host: "0.0.0.0", Port: 443, Enabled: true})
	s.db.Create(&db.Listener{TenantID: 1, Name: "http-80", Type: "http", Host: "0.0.0.0", Port: 80, Enabled: false})
	seedToolExecAgents(t, s)
	s.db.Create(&db.CredentialEntry{AgentID: "a-win", TenantID: 1, Username: "alice", Domain: "CORP", Type: "password", Password: "secret"})
	s.db.Create(&db.CredentialEntry{AgentID: "a-win", TenantID: 1, Username: "bob", Domain: "CORP", Type: "hash", Hash: "deadbeef"})
	s.db.Create(&db.CredentialEntry{AgentID: "a-other", TenantID: 2, Username: "mallory", Domain: "CORP"})

	listeners := s.executeToolSwitchCtx(ctx, "list_listeners", `{}`)
	if !strings.Contains(listeners, "https-443") || !strings.Contains(listeners, "http-80") {
		t.Fatalf("list_listeners = %s", listeners)
	}

	creds := s.executeToolSwitchCtx(ctx, "list_credentials", `{}`)
	var credRows []struct {
		Username   string `json:"username"`
		HasPass    bool   `json:"has_password"`
		HasHash    bool   `json:"has_hash"`
		Domain     string `json:"domain"`
		SourceName string `json:"source"`
	}
	if err := json.Unmarshal([]byte(creds), &credRows); err != nil {
		t.Fatalf("decode credentials: %v (%s)", err, creds)
	}
	if len(credRows) != 2 {
		t.Fatalf("credentials = %+v, want only tenant-1 entries", credRows)
	}
	seen := map[string]bool{}
	for _, c := range credRows {
		seen[c.Username] = true
	}
	if !seen["alice"] || !seen["bob"] || seen["mallory"] {
		t.Fatalf("credential tenant scope = %+v", credRows)
	}

	// No operator-session tracker in this Server: the tool still answers.
	ops := s.executeToolSwitchCtx(ctx, "get_online_operators", `{}`)
	var opBody struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(ops), &opBody); err != nil {
		t.Fatalf("decode operators: %v (%s)", err, ops)
	}
	if opBody.Count != 0 {
		t.Fatalf("operators count = %d, want 0", opBody.Count)
	}
}

func TestAIToolSearchTasksAndTimeline(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)
	ctx := tenantOperatorCtx()
	s.db.Create(&db.Task{AgentID: "a-win", Type: "shell", Command: "net user", Status: "completed", Result: "done"})
	s.db.Create(&db.Task{AgentID: "a-other", Type: "shell", Command: "net user", Status: "completed", Result: "foreign"})

	var found []struct {
		ID      uint   `json:"id"`
		AgentID string `json:"agent_id"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "search_tasks", `{"query":"net user"}`)), &found); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(found) != 1 || found[0].AgentID != "a-win" {
		t.Fatalf("search_tasks = %+v (tenant leak or miss)", found)
	}
	if out := s.executeToolSwitchCtx(ctx, "search_tasks", `{"query":"no-such-command"}`); strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty search = %s", out)
	}

	if out := s.executeToolSwitchCtx(ctx, "get_timeline", `{}`); !strings.Contains(out, "agent_id required") {
		t.Fatalf("timeline without agent = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_timeline", `{"agent_id":"nope"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("timeline unknown agent = %s", out)
	}
}

func TestAIToolTaskDetailAndPending(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)
	ctx := tenantOperatorCtx()
	pending := db.Task{AgentID: "a-win", Type: "shell", Command: "whoami", Status: "pending", CreatedBy: "ai"}
	if err := s.db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}
	approved := db.Task{AgentID: "a-win", Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval, CreatedBy: "human"}
	if err := s.db.Create(&approved).Error; err != nil {
		t.Fatal(err)
	}

	var detail struct {
		ID      uint   `json:"id"`
		AgentID string `json:"agent_id"`
		Command string `json:"command"`
	}
	out := s.executeToolSwitchCtx(ctx, "get_task_detail", `{"task_id":`+jsonNumber(pending.ID)+`}`)
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatalf("decode task detail: %v (%s)", err, out)
	}
	if detail.ID != pending.ID || detail.Command != "whoami" {
		t.Fatalf("task detail = %+v", detail)
	}
	// String-encoded ids are accepted too (models often quote numbers).
	if out := s.executeToolSwitchCtx(ctx, "get_task_detail", `{"task_id":"`+jsonNumber(pending.ID)+`"}`); !strings.Contains(out, "whoami") {
		t.Fatalf("string task_id = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_task_detail", `{"task_id":"abc"}`); !strings.Contains(out, "numeric task_id required") {
		t.Fatalf("non-numeric task_id = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_task_detail", `{"task_id":999999}`); !strings.Contains(out, "task not found") {
		t.Fatalf("missing task = %s", out)
	}

	var pendingRows []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_pending_tasks", `{}`)), &pendingRows); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if len(pendingRows) != 2 {
		t.Fatalf("pending tasks = %d, want both", len(pendingRows))
	}
	// The creator filter is advisory (it narrows, never widens).
	if out := s.executeToolSwitchCtx(ctx, "list_pending_tasks", `{"creator":"ai"}`); !strings.Contains(out, "whoami") {
		t.Fatalf("creator filter = %s", out)
	}
}

func TestAIToolMacrosScreenshotsKeylogs(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)
	ctx := tenantOperatorCtx()
	s.db.Create(&db.CommandMacro{TenantID: 1, Name: "recon", Description: "quick recon", Steps: `[{"type":"shell"},{"type":"ps"}]`})
	s.db.Create(&db.CommandMacro{TenantID: 1, Name: "bad-steps", Steps: `not-json`})

	var macros []struct {
		Name  string `json:"name"`
		Steps int    `json:"steps"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "list_macros", `{}`)), &macros); err != nil {
		t.Fatalf("decode macros: %v", err)
	}
	if len(macros) != 2 {
		t.Fatalf("macros = %+v", macros)
	}
	for _, m := range macros {
		if m.Name == "recon" && m.Steps != 2 {
			t.Fatalf("recon steps = %d, want 2", m.Steps)
		}
		// Malformed step JSON must not fail the tool, just report zero steps.
		if m.Name == "bad-steps" && m.Steps != 0 {
			t.Fatalf("bad-steps parsed to %d", m.Steps)
		}
	}

	s.db.Create(&db.Task{AgentID: "a-win", Type: "screenshot", Status: "completed", TotalBytes: 2048})
	s.db.Create(&db.Task{AgentID: "a-win", Type: "screenshot_window", Status: "completed", TotalBytes: 1024})
	s.db.Create(&db.Task{AgentID: "a-other", Type: "screenshot", Status: "completed", TotalBytes: 99})
	shots := s.executeToolSwitchCtx(ctx, "get_screenshot_summary", `{}`)
	if !strings.Contains(shots, "a-win") || strings.Contains(shots, "a-other") {
		t.Fatalf("screenshot summary tenant scope = %s", shots)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_screenshot_summary", `{"agent_id":"a-win","days":7}`); !strings.Contains(out, "a-win") {
		t.Fatalf("screenshot summary by agent = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_screenshot_summary", `{"agent_id":"nope"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("screenshot summary unknown agent = %s", out)
	}

	s.db.Create(&db.Task{AgentID: "a-win", Type: "keylogger_dump", Status: "completed", Result: "user typed password=hunter2 at login"})
	keys := s.executeToolSwitchCtx(ctx, "get_keylog_summary", `{}`)
	if !strings.Contains(keys, "password") {
		t.Fatalf("keylog summary missed the high-value keyword: %s", keys)
	}
	if out := s.executeToolSwitchCtx(ctx, "get_keylog_summary", `{"agent_id":"nope"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("keylog summary unknown agent = %s", out)
	}
}

func TestAIToolAlertsSleepQueueAndUnknown(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolExecAgents(t, s)
	ctx := tenantOperatorCtx()
	s.db.Create(&db.Alert{Type: "beacon", Severity: "high", Title: "new beacon", Message: "agent a-win checked in", Status: "active"})
	s.db.Create(&db.Alert{Type: "beacon", Severity: "low", Title: "old beacon", Status: "resolved"})

	var alerts struct {
		Count  int `json:"count"`
		Alerts []struct {
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "get_alerts", `{}`)), &alerts); err != nil {
		t.Fatalf("decode alerts: %v", err)
	}
	if alerts.Count != 1 || alerts.Alerts[0].Title != "new beacon" {
		t.Fatalf("default (active) alerts = %+v", alerts)
	}
	if err := json.Unmarshal([]byte(s.executeToolSwitchCtx(ctx, "get_alerts", `{"status":"all","limit":50}`)), &alerts); err != nil {
		t.Fatalf("decode all alerts: %v", err)
	}
	if alerts.Count != 2 {
		t.Fatalf("all alerts = %d, want 2", alerts.Count)
	}

	// set_sleep validates before touching the queue.
	if out := s.executeToolSwitchCtx(ctx, "set_sleep", `{"agent_id":"a-win","interval":0}`); !strings.Contains(out, "interval must be") {
		t.Fatalf("bad interval = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "set_sleep", `{"agent_id":"a-win","interval":60,"jitter":200}`); !strings.Contains(out, "jitter must be") {
		t.Fatalf("bad jitter = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "set_sleep", `{"agent_id":"nope","interval":60}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("sleep unknown agent = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "set_sleep", `{"agent_id":"a-win","interval":60,"jitter":10}`); strings.Contains(out, "error") {
		t.Fatalf("valid set_sleep = %s", out)
	}

	if out := s.executeToolSwitchCtx(ctx, "queue_collection", `{"agent_id":"a-win","action":"bogus"}`); !strings.Contains(out, "action must be") {
		t.Fatalf("bad collection action = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "queue_collection", `{"agent_id":"nope","action":"ps"}`); !strings.Contains(out, "agent not found") {
		t.Fatalf("collection unknown agent = %s", out)
	}
	if out := s.executeToolSwitchCtx(ctx, "queue_collection", `{"agent_id":"a-win","action":"ps"}`); strings.Contains(out, "error") {
		t.Fatalf("valid queue_collection = %s", out)
	}

	if out := s.executeToolSwitchCtx(ctx, "definitely_not_a_tool", `{}`); !strings.Contains(out, "unknown tool") {
		t.Fatalf("unknown tool = %s", out)
	}
	// Malformed JSON must not panic the dispatcher: the tolerant pre-parse
	// keeps string keys and each case re-parses what it needs.
	if out := s.executeToolSwitchCtx(ctx, "list_agents", `{"limit":[1,2]}`); out == "" {
		t.Fatal("malformed args produced an empty response")
	}
}
