package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// missingAPICtx is an authenticated tenant-1 admin request.
func missingAPICtx(method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("tenant_id", uint(1))
	c.Params = params
	return c, w
}

func seedMissingAPIAdmin(t *testing.T, s *Server) {
	t.Helper()
	if err := s.db.Create(&db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
}

func TestAPIPackerEndpoints(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	c, w := missingAPICtx(http.MethodGet, "/api/packer/templates", "", nil)
	s.handleAPIPackerTemplates(c)
	if w.Code != http.StatusOK {
		t.Fatalf("templates = %d", w.Code)
	}
	var templates struct {
		Templates []struct {
			Name string `json:"name"`
		} `json:"templates"`
	}
	unmarshalData(t, w.Body.Bytes(), &templates)
	if len(templates.Templates) == 0 {
		t.Fatal("no builtin packer templates advertised")
	}

	c, w = missingAPICtx(http.MethodGet, "/api/packer/info", "", nil)
	s.handleAPIPackerInfo(c)
	var info struct {
		EncodeTypes []string `json:"encode_types"`
		EntryPoints []string `json:"entry_points"`
		OutputTypes []string `json:"output_types"`
		CertOptions []string `json:"cert_options"`
	}
	unmarshalData(t, w.Body.Bytes(), &info)
	if len(info.EncodeTypes) == 0 || len(info.EntryPoints) == 0 || len(info.OutputTypes) == 0 {
		t.Fatalf("packer info incomplete: %+v", info)
	}
	// The endpoint advertises only implemented paths; "dll" is not one of them.
	for _, out := range info.OutputTypes {
		if out == "dll" {
			t.Fatalf("unimplemented output type advertised: %+v", info.OutputTypes)
		}
	}
}

func TestAPIPrivescTimelineChat(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	// Sensitive task commands are encrypted at rest; without a loot key the
	// insert is refused (fail-closed), so the timeline test needs one.
	forgecrypto.InitLootEncryption(strings.Repeat("25", 32))
	seedTenantAgent(t, s, "api-agent", 1)

	// A sensitive command is encrypted at rest and must come back readable
	// through the timeline (this is why the query targets db.Task).
	secret := `net use \\dc01\c$ /user:corp\alice S3cret!`
	if err := s.db.Create(&db.Task{AgentID: "api-agent", Type: "lateral", Command: secret, Status: "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	var rawAtRest string
	if err := s.db.Table("tasks").Select("command").Where("type = ?", "lateral").Scan(&rawAtRest).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawAtRest, "FC2ENC:") {
		t.Fatalf("sensitive command not encrypted at rest: %q", rawAtRest)
	}

	s.db.Create(&db.AuditLog{User: "admin", Action: "login", Details: "signed in", Success: true})
	s.db.Create(&db.Task{AgentID: "api-agent", Type: "privesc_check", Command: "whoami /priv", Status: "completed", Result: "SeDebugPrivilege enabled"})

	c, w := missingAPICtx(http.MethodGet, "/api/timeline", "", nil)
	s.handleAPITimelineData(c)
	if w.Code != http.StatusOK {
		t.Fatalf("timeline = %d %s", w.Code, w.Body.String())
	}
	var timeline struct {
		Events []struct {
			Type    string `json:"type"`
			Action  string `json:"action"`
			AgentID string `json:"agent_id"`
			Success bool   `json:"success"`
		} `json:"events"`
		Total int `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &timeline)
	if timeline.Total != len(timeline.Events) || timeline.Total < 3 {
		t.Fatalf("timeline total = %d events=%d", timeline.Total, len(timeline.Events))
	}
	kinds := map[string]int{}
	sawSecret := false
	for _, e := range timeline.Events {
		kinds[e.Type]++
		if strings.Contains(e.Action, "S3cret!") {
			sawSecret = true
		}
		if strings.Contains(e.Action, "FC2ENC:") {
			t.Fatalf("ciphertext leaked into the timeline: %s", e.Action)
		}
	}
	if kinds["audit"] != 1 || kinds["task"] < 2 {
		t.Fatalf("timeline kinds = %+v", kinds)
	}
	if !sawSecret {
		t.Fatal("decrypted command missing from the timeline")
	}

	// The privesc panel reads its own slice, decrypted the same way.
	c, w = missingAPICtx(http.MethodGet, "/api/privesc", "", nil)
	s.handleAPIPrivesc(c)
	var privesc struct {
		Results []struct {
			Command string `json:"command"`
			Result  string `json:"result"`
			Status  string `json:"status"`
		} `json:"results"`
	}
	unmarshalData(t, w.Body.Bytes(), &privesc)
	if len(privesc.Results) != 1 || privesc.Results[0].Result != "SeDebugPrivilege enabled" {
		t.Fatalf("privesc results = %+v", privesc.Results)
	}

	// Chat: send, history filter, channel aggregation.
	c, w = missingAPICtx(http.MethodPost, "/api/chat/send", `{"message":"hello team","channel":"opsec"}`, nil)
	s.handleAPISendChatMessage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("send chat = %d %s", w.Code, w.Body.String())
	}
	c, w = missingAPICtx(http.MethodPost, "/api/chat/send", `{"message":""}`, nil)
	s.handleAPISendChatMessage(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty message = %d", w.Code)
	}
	s.db.Create(&db.ChatMessage{Username: "admin", Message: "general chatter", Channel: "general"})

	c, w = missingAPICtx(http.MethodGet, "/api/chat/history?channel=opsec", "", nil)
	s.handleAPIChatHistory(c)
	var history struct {
		Messages []struct {
			Message string `json:"message"`
			Channel string `json:"channel"`
		} `json:"messages"`
	}
	unmarshalData(t, w.Body.Bytes(), &history)
	if len(history.Messages) != 1 || history.Messages[0].Channel != "opsec" {
		t.Fatalf("channel filter = %+v", history.Messages)
	}

	c, w = missingAPICtx(http.MethodGet, "/api/chat/channels", "", nil)
	s.handleAPIChatChannels(c)
	var channels struct {
		Channels []struct {
			Channel      string `json:"channel"`
			MessageCount int    `json:"message_count"`
			LastMessage  string `json:"last_message"`
		} `json:"channels"`
	}
	unmarshalData(t, w.Body.Bytes(), &channels)
	if len(channels.Channels) != 2 {
		t.Fatalf("channels = %+v", channels.Channels)
	}
	byChannel := map[string]int{}
	for _, ch := range channels.Channels {
		byChannel[ch.Channel] = ch.MessageCount
	}
	if byChannel["opsec"] != 1 || byChannel["general"] != 1 {
		t.Fatalf("channel counts = %+v", byChannel)
	}
}

func TestAgentChainGraphAndCRUD(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "chain-parent", 1)
	seedTenantAgent(t, s, "chain-child", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "chain-child").Update("parent_agent_id", "chain-parent").Error; err != nil {
		t.Fatal(err)
	}

	for name, fn := range map[string]func(*gin.Context){
		"graph": s.handleAPIChainGraph,
		"list":  s.handleAPIChainList,
		"chain": s.handleAgentChainGet,
	} {
		c, w := missingAPICtx(http.MethodGet, "/api/chain", "", nil)
		fn(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "chain-parent") {
			t.Fatalf("%s did not include the chain: %s", name, w.Body.String())
		}
	}

	// Set a parent through the path param (the body-only read used to 400).
	c, w := missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-parent"}`,
		gin.Params{{Key: "id", Value: "chain-child"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("chain set = %d %s", w.Code, w.Body.String())
	}
	var child db.Implant
	if err := s.db.Where("id = ?", "chain-child").First(&child).Error; err != nil {
		t.Fatal(err)
	}
	if child.ParentAgentID != "chain-parent" {
		t.Fatalf("parent = %q", child.ParentAgentID)
	}

	// Self-parent and cycle are refused.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-child"}`,
		gin.Params{{Key: "id", Value: "chain-child"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("self-parent = %d", w.Code)
	}
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-child"}`,
		gin.Params{{Key: "id", Value: "chain-parent"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cycle") {
		t.Fatalf("cycle = %d %s", w.Code, w.Body.String())
	}

	// Unknown agent and missing path id.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-parent"}`,
		gin.Params{{Key: "id", Value: "ghost"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}

	// Clear removes the parent.
	c, w = missingAPICtx(http.MethodDelete, "/api/agents/x/chain", "",
		gin.Params{{Key: "id", Value: "chain-child"}})
	s.handleAgentChainClear(c)
	if w.Code != http.StatusOK {
		t.Fatalf("chain clear = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("id = ?", "chain-child").First(&child).Error; err != nil {
		t.Fatal(err)
	}
	if child.ParentAgentID != "" {
		t.Fatalf("parent survived clear: %q", child.ParentAgentID)
	}
}

func TestAPISendChatMessageTooLong(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	c, w := missingAPICtx(http.MethodPost, "/api/chat/send",
		`{"message":"`+strings.Repeat("x", MaxChatMessageBytes+1)+`"}`, nil)
	s.handleAPISendChatMessage(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too long") {
		t.Fatalf("oversize message = %d %s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.ChatMessage{}).Count(&count)
	if count != 0 {
		t.Fatalf("oversize message was stored: %d", count)
	}
}
