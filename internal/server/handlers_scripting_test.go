package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

type scriptExecuteResponse struct {
	Success bool `json:"success"`
	Result  struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	} `json:"result"`
}

func postScriptExecute(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, "g1-scripter", 1)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/scripts/execute", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleAPIExecuteScript(c)
	return w
}

// TestScriptExecuteScopesAgentsGlobal proves the optional agent_id narrows the
// `agents` global to a single host, and exposes that host as `agent_id`.
func TestScriptExecuteScopesAgentsGlobal(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "g1-scope-target", 1)
	seedTenantAgent(t, s, "g1-scope-other", 1)

	w := postScriptExecute(t, s, `{"agent_id":"g1-scope-target","code":"agent_id + \":\" + agents.length"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp scriptExecuteResponse
	unmarshalData(t, w.Body.Bytes(), &resp)
	if !resp.Result.Success {
		t.Fatalf("execution failed: %+v", resp)
	}
	if resp.Result.Output != "g1-scope-target:1" {
		t.Fatalf("output=%q, want the scoped single-agent context", resp.Result.Output)
	}
}

// TestScriptExecuteCrossTenantAgent404 proves a target agent from another
// tenant is rejected before any script runs.
func TestScriptExecuteCrossTenantAgent404(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "g1-x-foreign", 2)

	w := postScriptExecute(t, s, `{"agent_id":"g1-x-foreign","code":"1"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", w.Code, w.Body.String())
	}
}

// TestScriptExecuteFailureIsAudited proves a failing run records its error, so
// the run history can show why it failed.
func TestScriptExecuteFailureIsAudited(t *testing.T) {
	s := mustTenantServer(t)

	w := postScriptExecute(t, s, `{"code":"throw new Error('boom')"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var entry db.AuditLog
	if err := s.db.Where("action = ?", "execute_script").Order("id DESC").First(&entry).Error; err != nil {
		t.Fatalf("load audit entry: %v", err)
	}
	if entry.Success {
		t.Fatalf("audit entry marked successful: %+v", entry)
	}
	if entry.Error == "" {
		t.Fatalf("audit entry carries no error text: %+v", entry)
	}
	if entry.AgentID != "" {
		t.Fatalf("agent_id=%q, want empty for a fleet-wide run", entry.AgentID)
	}
}

// TestScriptRunDetailsRoundTrip locks the audit-details encoding that the run
// history parses back.
func TestScriptRunDetailsRoundTrip(t *testing.T) {
	cases := []struct {
		label, agentID string
	}{
		{"inline", ""},
		{"recon", "agent-1"},
		{"multi\nline   label", "agent-2"},
		{"", ""},
	}
	for _, tc := range cases {
		gotLabel, gotAgent := parseScriptRunDetails(scriptRunDetails(tc.label, tc.agentID))
		wantLabel := tc.label
		if wantLabel == "" {
			wantLabel = "inline"
		}
		wantLabel = strings.Join(strings.Fields(wantLabel), " ")
		if gotLabel != wantLabel || gotAgent != tc.agentID {
			t.Fatalf("round trip (%q,%q) = (%q,%q)", tc.label, tc.agentID, gotLabel, gotAgent)
		}
	}

	if label, agent := parseScriptRunDetails("unrelated audit text"); label != "" || agent != "" {
		t.Fatalf("non-script details parsed as %q/%q, want empty", label, agent)
	}
}

// TestScriptsHistoryServesAuditRuns proves the history endpoint is fed by the
// audit log (execution never writes a task row) and stays tenant-scoped.
func TestScriptsHistoryServesAuditRuns(t *testing.T) {
	s := mustTenantServer(t)

	rows := []db.AuditLog{
		{User: "alice", Action: "execute_script", Resource: "scripting", AgentID: "g1-h-1", TenantID: 1, Success: true, Details: scriptRunDetails("recon", "g1-h-1")},
		{User: "alice", Action: "execute_script", Resource: "scripting", TenantID: 1, Success: false, Error: "syntax error", Details: scriptRunDetails("inline", "")},
		{User: "mallory", Action: "execute_script", Resource: "scripting", TenantID: 2, Success: true, Details: scriptRunDetails("foreign", "x")},
		{User: "legacy", Action: "execute_script", Resource: "scripting", TenantID: 0, Success: true, Details: scriptRunDetails("legacy-run", "")},
		{User: "alice", Action: "login", Resource: "auth", TenantID: 1, Success: true},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "g1-scripter", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/scripts/history", nil)
	s.handleAPIScriptsHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}

	var resp struct {
		History []struct {
			ID         uint   `json:"id"`
			ScriptName string `json:"script_name"`
			AgentID    string `json:"agent_id"`
			User       string `json:"user"`
			Status     string `json:"status"`
			Error      string `json:"error"`
		} `json:"history"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)

	if len(resp.History) != 3 {
		t.Fatalf("history has %d entries, want 3 (own tenant + legacy, no foreign)", len(resp.History))
	}
	byLabel := map[string]string{}
	for _, h := range resp.History {
		byLabel[h.ScriptName] = h.Status
		if h.ScriptName == "foreign" {
			t.Fatalf("cross-tenant audit row leaked into history: %+v", h)
		}
	}
	if byLabel["recon"] != "success" {
		t.Fatalf("recon run status=%q, want success", byLabel["recon"])
	}
	if byLabel["inline"] != "failed" {
		t.Fatalf("inline run status=%q, want failed", byLabel["inline"])
	}
	if byLabel["legacy-run"] != "success" {
		t.Fatalf("legacy run status=%q, want success", byLabel["legacy-run"])
	}
}
