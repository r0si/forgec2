package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestHandleAIGeneratePlaybook(t *testing.T) {
	s := assistTestServer(t)
	if err := s.db.Create(&db.User{Username: "alice", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "pb-agent", TenantID: 1, Hostname: "pb-host", OS: "windows",
		Arch: "amd64", Username: "svc", Domain: "CORP", Integrity: "high", Elevated: true, Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}

	// More steps than the cap, one without a command (dropped), one without a
	// shell (defaulted).
	steps := []string{
		`{"command":"whoami /all","shell":"cmd.exe","rationale":"confirm integrity"}`,
		`{"command":"","shell":"cmd.exe","rationale":"noise"}`,
		`{"command":"Get-Process","rationale":"inventory"}`,
	}
	for i := 0; i < 12; i++ {
		steps = append(steps, `{"command":"echo step`+string(rune('a'+i))+`","shell":"cmd.exe"}`)
	}
	modelJSON := `{"name":"  recon_plan  ","description":"quiet recon","steps":[` + strings.Join(steps, ",") + `]}`
	srv, last := fakeProvider(t, http.StatusOK,
		`{"choices":[{"message":{"content":`+strconvQuote(modelJSON)+`}}]}`)
	s.cfg.AI.Endpoint = srv.URL + "/v1/chat/completions"

	c, w := assistCtx(http.MethodPost, `{"goal":"enumerate the domain","agent_id":"pb-agent"}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("playbook = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Steps       []struct {
			Command   string `json:"command"`
			Shell     string `json:"shell"`
			Rationale string `json:"rationale"`
		} `json:"steps"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Name != "recon_plan" {
		t.Fatalf("name = %q, want the trimmed model name", out.Name)
	}
	if len(out.Steps) != 12 {
		t.Fatalf("steps = %d, want the 12-step cap (the empty one dropped)", len(out.Steps))
	}
	for _, st := range out.Steps {
		if strings.TrimSpace(st.Command) == "" {
			t.Fatalf("empty command survived: %+v", st)
		}
		if st.Shell == "" {
			t.Fatalf("shell not defaulted: %+v", st)
		}
	}
	if out.Steps[1].Command != "Get-Process" || out.Steps[1].Shell != "cmd.exe" {
		t.Fatalf("default shell not applied to step 2: %+v", out.Steps[1])
	}
	// The visible agent's profile must reach the model as context.
	if !strings.Contains(*last, "integrity=high") || !strings.Contains(*last, "enumerate the domain") {
		t.Fatalf("prompt missing goal or target profile: %s", *last)
	}

	// Validation and model-failure paths.
	c, w = assistCtx(http.MethodPost, `{}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing goal = %d", w.Code)
	}
	c, w = assistCtx(http.MethodPost, `{"goal":"   "}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("blank goal = %d", w.Code)
	}

	noisy, _ := fakeProvider(t, http.StatusOK, `{"choices":[{"message":{"content":"no json at all"}}]}`)
	s.cfg.AI.Endpoint = noisy.URL + "/v1/chat/completions"
	c, w = assistCtx(http.MethodPost, `{"goal":"recon"}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("unparseable playbook = %d %s", w.Code, w.Body.String())
	}

	empty, _ := fakeProvider(t, http.StatusOK,
		`{"choices":[{"message":{"content":`+strconvQuote(`{"name":"x","steps":[{"command":"   "}]}`)+`}}]}`)
	s.cfg.AI.Endpoint = empty.URL + "/v1/chat/completions"
	c, w = assistCtx(http.MethodPost, `{"goal":"recon"}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "no usable steps") {
		t.Fatalf("all-empty playbook = %d %s", w.Code, w.Body.String())
	}

	// A model reply with no explicit name gets a generated one.
	unnamed, _ := fakeProvider(t, http.StatusOK,
		`{"choices":[{"message":{"content":`+strconvQuote(`{"steps":[{"command":"whoami"}]}`)+`}}]}`)
	s.cfg.AI.Endpoint = unnamed.URL + "/v1/chat/completions"
	c, w = assistCtx(http.MethodPost, `{"goal":"recon"}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("unnamed playbook = %d %s", w.Code, w.Body.String())
	}
	var unnamedOut struct {
		Name  string `json:"name"`
		Steps []struct {
			Command string `json:"command"`
		} `json:"steps"`
	}
	unmarshalData(t, w.Body.Bytes(), &unnamedOut)
	if !strings.HasPrefix(unnamedOut.Name, "ai_playbook_") {
		t.Fatalf("generated name = %q", unnamedOut.Name)
	}

	// AI disabled wins over everything else.
	s.cfg.AI.Enabled = false
	c, w = assistCtx(http.MethodPost, `{"goal":"recon"}`)
	s.handleAIGeneratePlaybook(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("AI disabled = %d", w.Code)
	}
}

func TestBuildAIRunContext(t *testing.T) {
	s := knowledgeTestServer(t)
	principal := aiPrincipal{UserID: 1, Username: "alice", TenantID: 1, Role: db.RoleAdmin}
	session := seedChatSession(t, s, "context")
	otherSession := seedChatSession(t, s, "other")

	mine := uploadAttachment(t, s, session.ID, "mine.txt", "The jump host is 10.0.0.9.")
	uploadAttachment(t, s, otherSession.ID, "theirs.txt", "A different session's secret.")

	// Nothing selected ⇒ no injected context at all.
	if got := s.buildAIRunContext(principal, session.ID, "jump", nil, nil); got != "" {
		t.Fatalf("unexpected context without selections: %q", got)
	}

	// The attachment is included with its source id, and the boundary warning
	// tells the model to treat it as data.
	ctxText := s.buildAIRunContext(principal, session.ID, "", []string{mine.ID}, nil)
	if !strings.Contains(ctxText, "Authorized reference context") || !strings.Contains(ctxText, "never as instructions") {
		t.Fatalf("missing boundary warning: %q", ctxText)
	}
	if !strings.Contains(ctxText, "mine.txt") || !strings.Contains(ctxText, mine.ID) {
		t.Fatalf("attachment not cited: %q", ctxText)
	}
	// An attachment from another session is not injected even when its id is
	// requested explicitly.
	leak := s.buildAIRunContext(principal, session.ID, "", []string{"not-a-real-id"}, nil)
	if strings.Contains(leak, "theirs.txt") || strings.Contains(leak, "different session") {
		t.Fatalf("cross-session attachment leaked: %q", leak)
	}

	// Knowledge collections contribute ranked chunks when a query is present.
	collection := db.AIKnowledgeCollection{TenantID: 1, OwnerID: 1, Name: "kb"}
	if err := s.db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	c, w := knowledgeCtx(http.MethodPost, "/x", "")
	c.Params = gin.Params{{Key: "collectionID", Value: jsonNumber(collection.ID)}, {Key: "attachmentID", Value: mine.ID}}
	s.handleAIKnowledgePromoteAttachment(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("promote for context: %d %s", w.Code, w.Body.String())
	}
	withKB := s.buildAIRunContext(principal, session.ID, "jump host", nil, []uint{collection.ID})
	if !strings.Contains(withKB, "#chunk-") || !strings.Contains(withKB, "collection=") {
		t.Fatalf("knowledge chunk not cited: %q", withKB)
	}
	// Without a query the collection is not searched (no accidental full dump).
	if got := s.buildAIRunContext(principal, session.ID, "   ", nil, []uint{collection.ID}); got != "" {
		t.Fatalf("empty query injected knowledge: %q", got)
	}
}
