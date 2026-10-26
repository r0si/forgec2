package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// seedToolSweepData populates one row of every kind the read-only tools read,
// plus a tenant-2 sentinel that no tool may leak.
func seedToolSweepData(t *testing.T, s *Server) {
	t.Helper()
	seedToolExecAgents(t, s)
	if err := s.db.Create(&db.Listener{Name: "listener-1", TenantID: 1, Scheme: "http", Host: "10.0.0.1", Port: 8080, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.CredentialEntry{AgentID: "a-win", TenantID: 1, Username: "alice", Password: "s3cret", Type: "cleartext", Source: "lsass"}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: "a-win", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed", Result: "alice", CreatedAt: time.Now()}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Alert{TenantID: 1, Type: "cpu_high", Title: "CPU high", Source: "system", SourceName: "WIN-DC01", Status: "active", Severity: "warning"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.CommandMacro{Name: "recon", Description: "recon macro", Steps: `[{"type":"shell","command":"whoami"}]`, TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "sentinel-other", TenantID: 2, Hostname: "other-tenant", Status: "online", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
}

// TestAIToolReadOnlySweep calls every read-only tool once and requires a JSON
// result that never contains another tenant's data. It is a smoke sweep: the
// point is that each branch runs and stays tenant-scoped, not that every field
// is asserted.
func TestAIToolReadOnlySweep(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolSweepData(t, s)
	ctx := tenantOperatorCtx()

	cases := []struct {
		tool string
		args string
	}{
		{"list_agents", `{}`},
		{"get_agent_detail", `{"agent_id":"a-win"}`},
		{"get_agent_tasks", `{"agent_id":"a-win"}`},
		{"list_listeners", `{}`},
		{"list_credentials", `{}`},
		{"get_online_operators", `{}`},
		{"search_tasks", `{"query":"whoami"}`},
		{"get_timeline", `{}`},
		{"query_ioc", `{"query":"10.0.0.5"}`},
		{"list_macros", `{}`},
		{"get_attack_surface", `{}`},
		{"get_task_detail", `{"task_id":1}`},
		{"query_bloodhound", `{"query":"admin"}`},
		{"get_coverage_gaps", `{}`},
		{"list_pending_tasks", `{}`},
		{"get_screenshot_summary", `{}`},
		{"get_keylog_summary", `{}`},
		{"get_situation", `{}`},
		{"get_alerts", `{}`},
		{"generate_report", `{"type":"summary"}`},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			out := s.executeToolSwitchCtx(ctx, tc.tool, tc.args)
			if strings.TrimSpace(out) == "" {
				t.Fatalf("%s returned an empty result", tc.tool)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("%s returned invalid JSON: %.200s", tc.tool, out)
			}
			if strings.Contains(out, "other-tenant") || strings.Contains(out, "sentinel-other") {
				t.Fatalf("%s leaked another tenant's agent: %.300s", tc.tool, out)
			}
		})
	}
}

// TestAIToolSweepUnknownAndMalformedArgs pins the error envelope for a tool that
// does not exist and for arguments that cannot be parsed.
func TestAIToolSweepUnknownAndMalformedArgs(t *testing.T) {
	s := setupToolExecServer(t)
	seedToolSweepData(t, s)
	ctx := tenantOperatorCtx()

	// An unknown tool must report an error.
	out := s.executeToolSwitchCtx(ctx, "no_such_tool", `{}`)
	if !json.Valid([]byte(out)) {
		t.Fatalf("unknown tool returned invalid JSON: %.200s", out)
	}
	if !strings.Contains(out, "error") && !strings.Contains(out, "message") {
		t.Fatalf("unknown tool returned %.200s, want an error payload", out)
	}

	// Malformed arguments are tolerated: the tool runs with empty arguments
	// instead of failing the turn. Pinned so a future change is deliberate.
	out = s.executeToolSwitchCtx(ctx, "list_agents", `{`)
	if !json.Valid([]byte(out)) {
		t.Fatalf("malformed args returned invalid JSON: %.200s", out)
	}
	if strings.Contains(out, "other-tenant") {
		t.Fatal("malformed args leaked another tenant")
	}
}
