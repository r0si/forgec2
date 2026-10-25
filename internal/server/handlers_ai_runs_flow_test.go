package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// aiRunsTestServer is the storage harness plus a tenant-1 admin operator and an
// LLM endpoint that refuses everything, so the handler's happy path returns 202
// while its background run fails fast instead of reaching the network.
func aiRunsTestServer(t *testing.T) *Server {
	t.Helper()
	s := setupAIRunStorageTest(t)
	if err := s.db.Create(&db.User{Username: "alice", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return s
}

func refusingLLMEndpoint(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func seedAIProfile(t *testing.T, s *Server, p db.AIProviderProfile) db.AIProviderProfile {
	t.Helper()
	if p.Name == "" {
		p.Name = "profile"
	}
	if p.TenantID == 0 {
		p.TenantID = 1
	}
	p.Enabled = true
	if p.APIKey == "" {
		p.APIKey = "sk-test"
	}
	if p.Model == "" {
		p.Model = "test-model"
	}
	if err := s.db.Create(&p).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return p
}

func TestResolveAIRunProfileBranches(t *testing.T) {
	s := aiRunsTestServer(t)
	admin := aiPrincipal{UserID: 1, Username: "alice", TenantID: 1, Role: db.RoleAdmin}

	// Nothing configured at all.
	if _, err := s.resolveAIRunProfile(admin, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured resolve = %v", err)
	}

	// A default profile is picked up when the request names none.
	def := seedAIProfile(t, s, db.AIProviderProfile{Name: "default", Provider: "openai", Endpoint: refusingLLMEndpoint(t)})
	if err := s.db.Model(&def).Update("is_default", true).Error; err != nil {
		t.Fatal(err)
	}
	resolved, err := s.resolveAIRunProfile(admin, nil)
	if err != nil {
		t.Fatalf("default resolve: %v", err)
	}
	if resolved.ID == nil || *resolved.ID != def.ID || resolved.Model != "test-model" {
		t.Fatalf("resolved default = %+v", resolved)
	}

	// A provider with a known endpoint may omit it; "custom" may not.
	known := seedAIProfile(t, s, db.AIProviderProfile{Name: "known", Provider: "openai"})
	resolved, err = s.resolveAIRunProfile(admin, &known.ID)
	if err != nil {
		t.Fatalf("known provider resolve: %v", err)
	}
	if resolved.Endpoint == "" || resolved.Endpoint != config.DefaultAIEndpoint("openai") {
		t.Fatalf("known provider endpoint = %q", resolved.Endpoint)
	}
	custom := seedAIProfile(t, s, db.AIProviderProfile{Name: "custom", Provider: "custom"})
	if _, err := s.resolveAIRunProfile(admin, &custom.ID); err == nil || !strings.Contains(err.Error(), "explicit endpoint") {
		t.Fatalf("custom provider without endpoint = %v", err)
	}

	// A profile without a key is refused rather than called anonymously.
	noKey := seedAIProfile(t, s, db.AIProviderProfile{Name: "no-key", Provider: "openai"})
	if err := s.db.Model(&noKey).Update("api_key", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveAIRunProfile(admin, &noKey.ID); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("keyless profile = %v", err)
	}

	// Cross-tenant and disabled profiles are invisible even by id.
	foreign := seedAIProfile(t, s, db.AIProviderProfile{Name: "foreign", Provider: "openai", TenantID: 2})
	if _, err := s.resolveAIRunProfile(admin, &foreign.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-tenant profile = %v", err)
	}
	disabled := seedAIProfile(t, s, db.AIProviderProfile{Name: "off", Provider: "openai"})
	if err := s.db.Model(&disabled).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolveAIRunProfile(admin, &disabled.ID); err == nil {
		t.Fatal("disabled profile was resolved")
	}
}

func TestSessionAllowsLowRisk(t *testing.T) {
	s := aiRunsTestServer(t)
	auto := db.AIChatSession{TenantID: 1, OwnerID: 1, Owner: "alice", WritePolicy: "low_risk_auto"}
	approval := db.AIChatSession{TenantID: 1, OwnerID: 1, Owner: "alice", WritePolicy: "approval"}
	for _, sess := range []*db.AIChatSession{&auto, &approval} {
		if err := s.db.Create(sess).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !sessionAllowsLowRisk(s.db, auto.ID) {
		t.Fatal("low_risk_auto session was refused")
	}
	if sessionAllowsLowRisk(s.db, approval.ID) {
		t.Fatal("approval-policy session allowed low-risk writes")
	}
	if sessionAllowsLowRisk(s.db, 999999) {
		t.Fatal("missing session allowed low-risk writes")
	}
}

func aiRunsCtx(body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/ai/runs", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "alice")
	c.Set("user_role", db.RoleAdmin)
	return c, w
}

func TestHandleAIRunsCreateValidation(t *testing.T) {
	s := aiRunsTestServer(t)
	profile := seedAIProfile(t, s, db.AIProviderProfile{Name: "p", Provider: "openai", Endpoint: refusingLLMEndpoint(t)})
	session := db.AIChatSession{TenantID: 1, OwnerID: 1, Owner: "alice", Title: "chat"}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	foreignSession := db.AIChatSession{TenantID: 2, OwnerID: 2, Owner: "mallory", Title: "theirs"}
	if err := s.db.Create(&foreignSession).Error; err != nil {
		t.Fatal(err)
	}

	// Unauthenticated (no user in context) is a 403 from currentAIPrincipal.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/ai/runs", strings.NewReader("{}"))
	s.handleAIRunsCreate(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("anonymous create = %d", w.Code)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"no session", `{"messages":[{"role":"user","content":"hi"}]}`, http.StatusBadRequest},
		{"no messages", `{"session_id":1}`, http.StatusBadRequest},
		{"bad json", `{`, http.StatusBadRequest},
		{"bad role", `{"session_id":1,"messages":[{"role":"system","content":"x"}]}`, http.StatusBadRequest},
		{"unknown session", `{"session_id":999999,"messages":[{"role":"user","content":"hi"}]}`, http.StatusNotFound},
		{"foreign session", `{"session_id":` + jsonNumber(foreignSession.ID) + `,"messages":[{"role":"user","content":"hi"}]}`, http.StatusNotFound},
		{"agent not visible", `{"session_id":` + jsonNumber(session.ID) + `,"messages":[{"role":"user","content":"hi"}],"context":{"agent_id":"ghost"}}`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := aiRunsCtx(tc.body)
			s.handleAIRunsCreate(c)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}

	// Too many messages is refused before any DB work.
	msgs := make([]string, AIChatMaxMessages+1)
	for i := range msgs {
		msgs[i] = `{"role":"user","content":"hi"}`
	}
	c, w = aiRunsCtx(`{"session_id":` + jsonNumber(session.ID) + `,"messages":[` + strings.Join(msgs, ",") + `]}`)
	s.handleAIRunsCreate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too many messages = %d", w.Code)
	}
	// An over-long idempotency key is rejected.
	c, w = aiRunsCtx(`{"session_id":` + jsonNumber(session.ID) + `,"idempotency_key":"` + strings.Repeat("k", 101) + `","messages":[{"role":"user","content":"hi"}]}`)
	s.handleAIRunsCreate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long idempotency key = %d", w.Code)
	}

	// Happy path: accepted, queued, and the session adopts the profile.
	body := `{"session_id":` + jsonNumber(session.ID) + `,"profile_id":` + jsonNumber(profile.ID) + `,"idempotency_key":"run-1","messages":[{"role":"user","content":"hi"}]}`
	c, w = aiRunsCtx(body)
	s.handleAIRunsCreate(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Success bool         `json:"success"`
		Data    db.AIChatRun `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode accepted: %v (%s)", err, w.Body.String())
	}
	if !created.Success || created.Data.Status != aiRunStatusQueued || created.Data.Owner != "alice" {
		t.Fatalf("accepted run = %+v", created.Data)
	}
	// The idempotency key is not echoed in the response body (it is a client
	// secret); assert it landed on the row instead.
	var stored db.AIChatRun
	if err := s.db.Where("id = ?", created.Data.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.IdempotencyKey != "run-1" || stored.ProfileID == nil || *stored.ProfileID != profile.ID {
		t.Fatalf("stored run = %+v", stored)
	}

	// Replaying the key returns the same run instead of creating a second one.
	c, w = aiRunsCtx(body)
	s.handleAIRunsCreate(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"reused":true`) {
		t.Fatalf("idempotent replay = %d %s", w.Code, w.Body.String())
	}
	var runs int64
	s.db.Model(&db.AIChatRun{}).Where("idempotency_key = ?", "run-1").Count(&runs)
	if runs != 1 {
		t.Fatalf("idempotent replay created %d runs", runs)
	}
}

func TestHandleAIRunsGetAndListAuthorization(t *testing.T) {
	s := aiRunsTestServer(t)
	mine := db.AIChatRun{ID: "run-mine", TenantID: 1, OwnerID: 1, Owner: "alice", SessionID: 1, Status: aiRunStatusQueued}
	theirs := db.AIChatRun{ID: "run-theirs", TenantID: 2, OwnerID: 2, Owner: "mallory", SessionID: 2, Status: aiRunStatusQueued}
	for _, run := range []*db.AIChatRun{&mine, &theirs} {
		if err := s.db.Create(run).Error; err != nil {
			t.Fatal(err)
		}
	}

	c, w := aiRunsCtx("")
	c.Params = gin.Params{{Key: "id", Value: "run-mine"}}
	s.handleAIRunsGet(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "run-mine") {
		t.Fatalf("own run = %d %s", w.Code, w.Body.String())
	}
	c, w = aiRunsCtx("")
	c.Params = gin.Params{{Key: "id", Value: "run-theirs"}}
	s.handleAIRunsGet(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign run = %d", w.Code)
	}

	c, w = aiRunsCtx("")
	s.handleAIRunsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "run-theirs") {
		t.Fatalf("list leaked a foreign run: %s", w.Body.String())
	}

	// active=1 narrows to live runs only.
	c, w = aiRunsCtx("")
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ai/runs?status=active", nil)
	c.Set("user", "alice")
	c.Set("user_role", db.RoleAdmin)
	s.handleAIRunsList(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "run-mine") {
		t.Fatalf("active list = %d %s", w.Code, w.Body.String())
	}
}
