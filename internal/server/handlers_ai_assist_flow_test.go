package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// assistTestServer is the run-storage harness with AI enabled and the SSRF
// guard relaxed for the in-process fake provider (loopback is blocked by
// default on purpose, so this mirrors what a local-model operator configures).
func assistTestServer(t *testing.T) *Server {
	t.Helper()
	s := setupAIRunStorageTest(t)
	s.cfg.AI.Enabled = true
	s.cfg.AI.APIKey = "sk-test"
	s.cfg.AI.Provider = "openai"
	s.cfg.AI.Model = "test-model"
	s.cfg.AI.AllowPrivateEndpoint = true
	return s
}

// fakeProvider serves one canned body and records the last request body so a
// test can assert the wire format.
func fakeProvider(t *testing.T, status int, body string) (*httptest.Server, *string) {
	t.Helper()
	last := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 0, 1024)
		tmp := make([]byte, 512)
		for {
			n, err := r.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		*last = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, last
}

func TestAIOneShotWireFormats(t *testing.T) {
	t.Run("openai wire", func(t *testing.T) {
		s := assistTestServer(t)
		srv, last := fakeProvider(t, http.StatusOK, `{"choices":[{"message":{"content":"hello there"}}]}`)
		s.cfg.AI.Endpoint = srv.URL + "/v1/chat/completions"

		text, err := s.aiOneShot(context.Background(), "system prompt", "user prompt", 0)
		if err != nil {
			t.Fatalf("aiOneShot: %v", err)
		}
		if text != "hello there" {
			t.Fatalf("text = %q", text)
		}
		if !strings.Contains(*last, `"system prompt"`) || !strings.Contains(*last, `"user prompt"`) {
			t.Fatalf("request body did not carry both messages: %s", *last)
		}
		if !strings.Contains(*last, `"max_tokens"`) {
			t.Fatalf("request body missing max_tokens: %s", *last)
		}
	})

	t.Run("claude wire", func(t *testing.T) {
		s := assistTestServer(t)
		s.cfg.AI.Provider = "anthropic"
		srv, _ := fakeProvider(t, http.StatusOK, `{"content":[{"type":"text","text":"from claude"}]}`)
		s.cfg.AI.Endpoint = srv.URL + "/v1/messages"

		text, err := s.aiOneShot(context.Background(), "sys", "usr", 128)
		if err != nil {
			t.Fatalf("aiOneShot: %v", err)
		}
		if text != "from claude" {
			t.Fatalf("text = %q", text)
		}
	})

	cases := []struct {
		name     string
		provider string
		body     string
		wantErr  string
	}{
		{"openai provider error", "openai", `{"error":{"message":"quota exceeded"}}`, "provider error"},
		{"openai empty choices", "openai", `{"choices":[]}`, "empty completion"},
		{"openai malformed", "openai", `not json`, "parse response"},
		{"claude provider error", "anthropic", `{"error":{"message":"bad key"}}`, "provider error"},
		{"claude no text block", "anthropic", `{"content":[{"type":"tool_use"}]}`, "empty completion"},
		{"claude malformed", "anthropic", `{`, "parse response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := assistTestServer(t)
			s.cfg.AI.Provider = tc.provider
			srv, _ := fakeProvider(t, http.StatusOK, tc.body)
			s.cfg.AI.Endpoint = srv.URL + "/v1/chat"
			if _, err := s.aiOneShot(context.Background(), "sys", "usr", 16); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}

	t.Run("disabled", func(t *testing.T) {
		s := assistTestServer(t)
		s.cfg.AI.APIKey = ""
		// Either wording is a correct refusal: the snapshot reports the
		// missing key, and aiOneShot has its own disabled sentinel.
		_, err := s.aiOneShot(context.Background(), "sys", "usr", 16)
		if err == nil || (!strings.Contains(err.Error(), "disabled") && !strings.Contains(err.Error(), "not configured")) {
			t.Fatalf("error = %v, want a disabled/not-configured refusal", err)
		}
	})
}

func TestBuildAttackSurfaceData(t *testing.T) {
	s := assistTestServer(t)
	agent := db.Implant{ID: "surface-agent", TenantID: 1, Hostname: "srv-01", IP: "10.0.0.9",
		OS: "windows", Arch: "amd64", Username: "svc", Domain: "CORP", Integrity: "high",
		Elevated: true, PID: 4242, Status: "online", Version: "2.7.1"}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	s.db.Create(&db.CredentialEntry{AgentID: "surface-agent", TenantID: 1, Username: "alice"})
	s.db.Create(&db.CredentialEntry{AgentID: "surface-agent", TenantID: 1, Username: "bob"})
	s.db.Create(&db.Task{AgentID: "surface-agent", Type: "shell", Command: "whoami", Status: "completed"})
	s.db.Create(&db.Task{AgentID: "surface-agent", Type: "lateral", Command: "psexec", Status: "completed"})
	s.db.Create(&db.Task{AgentID: "surface-agent", Type: "ssh_lateral", Command: "ssh", Status: "completed"})
	// Another agent's rows must not leak into this surface.
	s.db.Create(&db.CredentialEntry{AgentID: "other-agent", TenantID: 1, Username: "mallory"})
	s.db.Create(&db.Task{AgentID: "other-agent", Type: "lateral", Command: "foreign"})

	surface, err := s.buildAttackSurfaceData(agent)
	if err != nil {
		t.Fatalf("buildAttackSurfaceData: %v", err)
	}
	if got := surface["credentials_nearby"]; got != int64(2) {
		t.Fatalf("credentials_nearby = %v", got)
	}
	recent, _ := surface["recent_tasks"].([]db.Task)
	if len(recent) != 3 {
		t.Fatalf("recent_tasks = %d, want 3 for this agent", len(recent))
	}
	lateral, _ := surface["recent_lateral"].([]db.Task)
	if len(lateral) != 2 {
		t.Fatalf("recent_lateral = %d, want the 2 lateral tasks", len(lateral))
	}
	info, _ := surface["agent"].(map[string]interface{})
	if info["hostname"] != "srv-01" || info["integrity"] != "high" || info["pid"] != 4242 {
		t.Fatalf("agent info = %+v", info)
	}
}

func assistCtx(method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/ai/assist", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	// resolveVisibleAgentID reads the tenant from the context keys the auth
	// middleware sets.
	c.Set("user", "alice")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("tenant_id", uint(1))
	return c, w
}

func TestHandleAISuggestNextSteps(t *testing.T) {
	s := assistTestServer(t)
	if err := s.db.Create(&db.User{Username: "alice", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "suggest-agent", TenantID: 1, Hostname: "host-a", Status: "online", Elevated: true}).Error; err != nil {
		t.Fatal(err)
	}

	// The model returns more items than the cap, one without an action, and an
	// unknown risk level; the handler must trim, drop and normalize.
	fenced := "```json\n" + `[
		{"action":"Dump LSASS","reason":"elevated","risk":"high","command_hint":"procdump"},
		{"action":"  ","reason":"empty action","risk":"low"},
		{"action":"Enumerate shares","reason":"quiet","risk":"weird","command_hint":"net view"},
		{"action":"a4","reason":"r"},{"action":"a5","reason":"r"},{"action":"a6","reason":"r"},
		{"action":"a7","reason":"r"},{"action":"a8","reason":"r"}
	]` + "\n```"
	srv, _ := fakeProvider(t, http.StatusOK,
		`{"choices":[{"message":{"content":`+strconvQuote(fenced)+`}}]}`)
	s.cfg.AI.Endpoint = srv.URL + "/v1/chat/completions"

	c, w := assistCtx(http.MethodPost, `{"agent_id":"suggest-agent"}`)
	s.handleAISuggestNextSteps(c)
	if w.Code != http.StatusOK {
		t.Fatalf("suggest = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		AgentID     string `json:"agent_id"`
		Hostname    string `json:"hostname"`
		Suggestions []struct {
			Action string `json:"action"`
			Risk   string `json:"risk"`
		} `json:"suggestions"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.AgentID != "suggest-agent" || out.Hostname != "host-a" {
		t.Fatalf("context = %+v", out)
	}
	if len(out.Suggestions) != 6 {
		t.Fatalf("suggestions = %d, want the cap of 6", len(out.Suggestions))
	}
	risks := map[string]string{}
	for _, sg := range out.Suggestions {
		risks[sg.Action] = sg.Risk
	}
	if risks["Dump LSASS"] != "high" {
		t.Fatalf("high risk not preserved: %+v", risks)
	}
	if risks["Enumerate shares"] != "medium" {
		t.Fatalf("unknown risk not normalized: %+v", risks)
	}

	// Validation and failure paths.
	c, w = assistCtx(http.MethodPost, `{}`)
	s.handleAISuggestNextSteps(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing agent = %d", w.Code)
	}
	c, w = assistCtx(http.MethodPost, `{"agent_id":"ghost"}`)
	s.handleAISuggestNextSteps(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}

	bad, _ := fakeProvider(t, http.StatusOK, `{"choices":[{"message":{"content":"no json here"}}]}`)
	s.cfg.AI.Endpoint = bad.URL + "/v1/chat/completions"
	c, w = assistCtx(http.MethodPost, `{"agent_id":"suggest-agent"}`)
	s.handleAISuggestNextSteps(c)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("unparseable model output = %d %s", w.Code, w.Body.String())
	}

	// AI switched off is a 503, not a 502.
	s.cfg.AI.Enabled = false
	c, w = assistCtx(http.MethodPost, `{"agent_id":"suggest-agent"}`)
	s.handleAISuggestNextSteps(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("AI disabled = %d", w.Code)
	}
}

func TestHandleAINLQuery(t *testing.T) {
	s := assistTestServer(t)
	if err := s.db.Create(&db.User{Username: "alice", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "nl-agent", TenantID: 1, Hostname: "nl-host", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.db.Create(&db.Task{AgentID: "nl-agent", Type: "shell", Command: "net user", Status: "completed"})
	s.db.Create(&db.Task{AgentID: "nl-agent", Type: "shell", Command: "whoami", Status: "failed"})
	s.db.Create(&db.Task{AgentID: "nl-agent", Type: "screenshot", Status: "completed"})

	filter := `{"keywords":["net user"],"agent_hostname":"nl-host","task_type":"shell","status":"completed","since_days":999}`
	srv, last := fakeProvider(t, http.StatusOK,
		`{"choices":[{"message":{"content":`+strconvQuote(filter)+`}}]}`)
	s.cfg.AI.Endpoint = srv.URL + "/v1/chat/completions"

	c, w := assistCtx(http.MethodPost, `{"question":"where did I run net user?"}`)
	s.handleAINLQuery(c)
	if w.Code != http.StatusOK {
		t.Fatalf("nl query = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(*last, "where did I run net user?") {
		t.Fatalf("question not forwarded to the model: %s", *last)
	}
	var out struct {
		Filter struct {
			Keywords  []string `json:"keywords"`
			TaskType  string   `json:"task_type"`
			Status    string   `json:"status"`
			SinceDays int      `json:"since_days"`
		} `json:"filter"`
		Tasks []struct {
			Command string `json:"command"`
			Type    string `json:"type"`
		} `json:"tasks"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	// An out-of-range since_days is clamped, and the filter actually narrows.
	if out.Filter.SinceDays != 30 {
		t.Fatalf("since_days = %d, want clamp to 30", out.Filter.SinceDays)
	}
	if out.Filter.Status != "completed" || out.Filter.TaskType != "shell" {
		t.Fatalf("filter = %+v", out.Filter)
	}
	if len(out.Tasks) != 1 || out.Tasks[0].Command != "net user" {
		t.Fatalf("filtered tasks = %+v", out.Tasks)
	}

	// A status outside the whitelist is dropped rather than passed to SQL.
	bad := `{"keywords":[],"status":"'; DROP TABLE tasks;--","since_days":7}`
	srv2, _ := fakeProvider(t, http.StatusOK,
		`{"choices":[{"message":{"content":`+strconvQuote(bad)+`}}]}`)
	s.cfg.AI.Endpoint = srv2.URL + "/v1/chat/completions"
	c, w = assistCtx(http.MethodPost, `{"question":"everything"}`)
	s.handleAINLQuery(c)
	if w.Code != http.StatusOK {
		t.Fatalf("whitelist query = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "DROP TABLE") {
		t.Fatalf("injected status reached the response: %s", w.Body.String())
	}

	c, w = assistCtx(http.MethodPost, `{}`)
	s.handleAINLQuery(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing question = %d", w.Code)
	}
}

// strconvQuote JSON-quotes a string for embedding in a canned provider body.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
