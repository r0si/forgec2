package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestExtractJSONBlock_Fenced(t *testing.T) {
	in := "Here you go:\n```json\n{\"a\":1}\n```\nthanks"
	if got := extractJSONBlock(in); got != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestExtractJSONBlock_BareSpan(t *testing.T) {
	in := `The filter is {"keywords":["mimikatz"],"since_days":7} as requested.`
	got := extractJSONBlock(in)
	if got == "" || !containsStr(got, `"mimikatz"`) {
		t.Fatalf("unexpected extraction: %q", got)
	}
}

func TestDecodeModelJSON_FencedArray(t *testing.T) {
	var out []struct {
		Action string `json:"action"`
		Risk   string `json:"risk"`
	}
	in := "```json\n[{\"action\":\"Dump LSASS\",\"risk\":\"high\"}]\n```"
	if err := decodeModelJSON(in, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Action != "Dump LSASS" || out[0].Risk != "high" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestInjectDefaultAgent(t *testing.T) {
	// Missing agent_id gets filled from context.
	out := injectDefaultAgent("execute_command", `{"command":"whoami"}`, "agent-9")
	if !containsStr(out, `"agent_id":"agent-9"`) || !containsStr(out, "whoami") {
		t.Fatalf("expected agent_id injection for missing field, got %s", out)
	}
	// Empty agent_id also gets replaced.
	out = injectDefaultAgent("get_agent_tasks", `{"agent_id":""}`, "agent-7")
	if !containsStr(out, `"agent_id":"agent-7"`) {
		t.Fatalf("expected empty agent_id replacement, got %s", out)
	}
	// Explicit agent_id is preserved.
	out = injectDefaultAgent("execute_command", `{"agent_id":"explicit-1","command":"id"}`, "agent-9")
	if !containsStr(out, `"agent_id":"explicit-1"`) || containsStr(out, "agent-9") {
		t.Fatalf("explicit agent_id must not be overwritten, got %s", out)
	}
	// run_macro and execute_command_bulk get agent_ids ARRAY when absent —
	// a bare agent_id string would be ignored by their typed parsers (B2).
	for _, tool := range []string{"run_macro", "execute_command_bulk"} {
		out = injectDefaultAgent(tool, `{"command":"x"}`, "agent-3")
		if !containsStr(out, `"agent_ids":["agent-3"]`) {
			t.Fatalf("%s: expected agent_ids array injection, got %s", tool, out)
		}
	}
	// Non-agent tools untouched.
	if out := injectDefaultAgent("list_agents", `{"foo":1}`, "agent-1"); containsStr(out, "agent-1") {
		t.Fatalf("non-agent tool must stay untouched, got %s", out)
	}
}

// TestExecuteToolSwitchToleratesArrayArgs is the B1 regression guard:
// schema-conformant args carrying arrays/numbers must reach the tool case
// instead of dying in the string pre-parse with "invalid arguments JSON".
func TestExecuteToolSwitchToleratesArrayArgs(t *testing.T) {
	s := &Server{ctx: context.Background()}
	// bulk_task_action with empty task_ids reaches its validation branch
	// ("task_ids required") rather than the pre-parse error.
	out := s.executeToolSwitch("bulk_task_action", `{"task_ids":[],"action":"cancel"}`)
	if containsStr(out, "invalid arguments JSON") {
		t.Fatalf("array-typed args rejected by pre-parse: %s", out)
	}
	if !containsStr(out, "task_ids required") {
		t.Fatalf("expected typed validation to run, got %s", out)
	}
	// Mixed string+number+bool payload also survives pre-parse.
	out = s.executeToolSwitch("create_listener", `{"scheme":"http","host":"0.0.0.0","port":9999,"notes":"x"}`)
	if containsStr(out, "invalid arguments JSON") {
		t.Fatalf("mixed-type args rejected by pre-parse: %s", out)
	}
}

func TestTruncateStrRuneBoundary(t *testing.T) {
	// Multi-byte rune sliced at a byte offset must not produce invalid UTF-8.
	in := "主机名测试中文"
	out := truncateStr(in, 7)
	if !utf8.ValidString(out) {
		t.Fatalf("truncated string is not valid UTF-8: %q", out)
	}
	if !containsStr(out, "...") || len(out) > 7+len("...") {
		t.Fatalf("unexpected truncation result: %q", out)
	}
	// ASCII behavior unchanged.
	if got := truncateStr("abcdefghij", 5); got != "abcde..." {
		t.Fatalf("ascii truncation changed: %q", got)
	}
	// n=0/negative edge.
	if got := truncateStr("abc", 0); got != "..." {
		t.Fatalf("n=0 should yield ellipsis only, got %q", got)
	}
}

func setupAssistTestServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(&db.Implant{}, &db.Task{}, &db.CommandMacro{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := &Server{db: database, ctx: context.Background(), cfg: &config.Config{}}
	r := gin.New()
	r.POST("/api/ai/save-playbook", s.handleAISavePlaybook)
	r.POST("/api/ai/nl-query", s.handleAINLQuery)
	r.GET("/api/ai/status", s.handleAIStatus)
	return s, r
}

func performJSON(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAssistEndpointsDegradedWhenAIDisabled(t *testing.T) {
	_, r := setupAssistTestServer(t)

	w := performJSON(r, http.MethodPost, "/api/ai/nl-query", map[string]string{"question": "who ran mimikatz"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when AI disabled, got %d", w.Code)
	}
}

func TestHandleAIStatus_OffByDefault(t *testing.T) {
	_, r := setupAssistTestServer(t)
	w := performJSON(r, http.MethodGet, "/api/ai/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status should always answer 200, got %d", w.Code)
	}
	var st struct {
		Enabled   bool `json:"enabled"`
		HasAPIKey bool `json:"has_api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.Enabled || st.HasAPIKey {
		t.Fatalf("expected disabled status, got %+v", st)
	}
}

func TestHandleAISavePlaybook_ValidationAndCreate(t *testing.T) {
	s, r := setupAssistTestServer(t)

	// Missing steps -> 400
	w := performJSON(r, http.MethodPost, "/api/ai/save-playbook", map[string]interface{}{"name": "pb"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing steps, got %d", w.Code)
	}

	// Valid save creates macro owned by ai
	w = performJSON(r, http.MethodPost, "/api/ai/save-playbook", map[string]interface{}{
		"name":        "collect_plan",
		"description": "recon sweep",
		"steps": []map[string]string{
			{"command": "tasklist /v", "shell": "cmd.exe"},
			{"command": "Get-NetTCPConnection", "shell": "powershell.exe"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("valid save failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID int `json:"id"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.ID == 0 {
		t.Fatal("macro id missing in response")
	}
	var macro db.CommandMacro
	if err := s.db.First(&macro, resp.ID).Error; err != nil {
		t.Fatalf("macro not persisted: %v", err)
	}
	if macro.CreatedBy != "ai" {
		t.Fatalf("expected CreatedBy=ai, got %q", macro.CreatedBy)
	}
	if !containsStr(macro.Steps, "tasklist") {
		t.Fatalf("steps not stored: %q", macro.Steps)
	}
}

func TestNLQueryStatusesWhitelist(t *testing.T) {
	for _, st := range []string{"pending", "pending_approval", "running", "completed", "failed", "cancelled"} {
		if !nlQueryStatuses[st] {
			t.Fatalf("status %q should be whitelisted", st)
		}
	}
	if nlQueryStatuses["DROP TABLE"] || nlQueryStatuses["completed; --"] {
		t.Fatal("injection strings must not be whitelisted")
	}
}

// TestBuildClaudeRequestCarriesMaxTokens: aiOneShot's per-call token cap must
// survive the OpenAI→Claude conversion instead of being replaced by the
// global constant (B6).
func TestBuildClaudeRequestCarriesMaxTokens(t *testing.T) {
	s := &Server{ctx: context.Background(), cfg: &config.Config{}}
	s.cfg.AI.Provider = "claude"

	payload := `{"model":"claude-3","stream":false,"max_tokens":300,"messages":[{"role":"user","content":"hi"}]}`
	out := string(s.buildClaudeRequest([]byte(payload)))
	if !containsStr(out, `"max_tokens":300`) {
		t.Fatalf("per-call max_tokens lost in claude conversion: %s", out)
	}

	// Without an explicit cap the Claude default applies.
	out = string(s.buildClaudeRequest([]byte(`{"model":"claude-3","stream":true,"messages":[]}`)))
	if !containsStr(out, `"max_tokens":4096`) {
		t.Fatalf("default max_tokens missing: %s", out)
	}
}
