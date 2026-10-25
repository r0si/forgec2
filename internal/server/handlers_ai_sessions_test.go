package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/config"
	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupAISessionTestServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	forgecrypto.InitLootEncryption(strings.Repeat("11", 32))
	gin.SetMode(gin.TestMode)
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&db.User{}, &db.AIChatSession{}, &db.AIChatMessage{}); err != nil {
		t.Fatalf("migrate AI session tables: %v", err)
	}
	if err := database.Create(&db.User{Username: "admin", Role: db.RoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatalf("create AI session test user: %v", err)
	}

	s := &Server{db: database, cfg: &config.Config{}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.GET("/ai/sessions/:id", s.handleAISessionsGet)
	r.POST("/ai/sessions/:id/messages", s.handleAISessionsMessages)
	return s, r
}

func TestAISessionGetReturnsNewestMessagesInChronologicalOrder(t *testing.T) {
	s, r := setupAISessionTestServer(t)
	session := db.AIChatSession{Title: "long session", Owner: "admin", OwnerID: 1}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := make([]db.AIChatMessage, 205)
	for i := range messages {
		messages[i] = db.AIChatMessage{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   fmt.Sprintf("message-%03d", i+1),
		}
	}
	if err := s.db.CreateInBatches(messages, 50).Error; err != nil {
		t.Fatalf("create messages: %v", err)
	}

	w := performJSON(r, http.MethodGet, fmt.Sprintf("/ai/sessions/%d", session.ID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []db.AIChatMessage `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &response)
	if len(response.Data) != aiSessionMessageLimit {
		t.Fatalf("expected %d recent messages, got %d", aiSessionMessageLimit, len(response.Data))
	}
	if response.Data[0].Content != "message-006" || response.Data[len(response.Data)-1].Content != "message-205" {
		t.Fatalf("unexpected restored range: first=%q last=%q", response.Data[0].Content, response.Data[len(response.Data)-1].Content)
	}
}

func TestAISessionMessageValidation(t *testing.T) {
	s, r := setupAISessionTestServer(t)
	session := db.AIChatSession{Title: "validation", Owner: "admin", OwnerID: 1}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	path := fmt.Sprintf("/ai/sessions/%d/messages", session.ID)

	tests := []struct {
		name   string
		body   map[string]string
		status int
	}{
		{name: "invalid role", body: map[string]string{"role": "system", "content": "x"}, status: http.StatusBadRequest},
		{name: "oversized content", body: map[string]string{"role": "assistant", "content": strings.Repeat("x", aiSessionMessageMaxBytes+1)}, status: http.StatusRequestEntityTooLarge},
		{name: "oversized tool name", body: map[string]string{"role": "tool", "content": "x", "tool_name": strings.Repeat("界", aiSessionToolMaxRunes+1)}, status: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := performJSON(r, http.MethodPost, path, tc.body)
			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestAISessionMessageBatchIsAtomicAndOrdered(t *testing.T) {
	s, r := setupAISessionTestServer(t)
	session := db.AIChatSession{Title: "atomic batch", Owner: "admin", OwnerID: 1}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/ai/sessions/%d/messages", session.ID)

	invalid := performJSON(r, http.MethodPost, path, map[string]interface{}{
		"messages": []map[string]string{
			{"role": "user", "content": "must not persist"},
			{"role": "system", "content": "invalid"},
		},
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", invalid.Code, invalid.Body.String())
	}
	var count int64
	if err := s.db.Model(&db.AIChatMessage{}).Where("session_id = ?", session.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid batch partially persisted %d messages", count)
	}

	valid := performJSON(r, http.MethodPost, path, map[string]interface{}{
		"messages": []map[string]string{
			{"role": "user", "content": "question"},
			{"role": "tool", "content": "result", "tool_name": "get_situation"},
			{"role": "assistant", "content": "answer"},
		},
	})
	if valid.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", valid.Code, valid.Body.String())
	}
	var messages []db.AIChatMessage
	if err := s.db.Where("session_id = ?", session.ID).Order("id asc").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Content != "question" || messages[1].ToolName != "get_situation" || messages[2].Content != "answer" {
		t.Fatalf("batch order/content mismatch: %+v", messages)
	}
}

func TestHandleAIConfigPersistsExecutionSetting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	s := &Server{cfg: &config.Config{}, configPath: cfgPath}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/config", s.handleAIConfig)

	w := performJSON(r, http.MethodPost, "/ai/config", map[string]interface{}{
		"enabled":       true,
		"provider":      "deepseek",
		"api_key":       "test-key",
		"model":         "deepseek-chat",
		"allow_execute": true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !s.cfg.AI.AllowExecute {
		t.Fatal("allow_execute was not applied to the live config")
	}
	persisted, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !strings.Contains(string(persisted), "allow_execute: true") {
		t.Fatalf("allow_execute was not persisted: %s", persisted)
	}
}

func TestHandleAIConfigRejectsInvalidValuesBeforeMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	s.cfg.AI.Provider = "openai"
	s.cfg.AI.APIKey = "existing-key"
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/config", s.handleAIConfig)

	w := performJSON(r, http.MethodPost, "/ai/config", map[string]interface{}{
		"enabled":          true,
		"provider":         "deepseek",
		"model":            "deepseek-chat",
		"engagement_notes": strings.Repeat("x", aiMaxNotesLen+1),
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if s.cfg.AI.Provider != "openai" || s.cfg.AI.APIKey != "existing-key" {
		t.Fatalf("invalid request mutated live config: %#v", s.cfg.AI)
	}

	w = performJSON(r, http.MethodPost, "/ai/config", map[string]interface{}{
		"enabled":  true,
		"provider": "unknown-provider",
		"model":    "model",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected unsupported provider to return 400, got %d", w.Code)
	}
}

func TestHandleAIConfigRollsBackWhenPersistenceFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{
		cfg:        &config.Config{},
		configPath: filepath.Join(t.TempDir(), "missing", "config.yaml"),
	}
	s.cfg.AI.Enabled = false
	s.cfg.AI.Provider = "openai"
	s.cfg.AI.APIKey = "existing-key"
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/config", s.handleAIConfig)

	w := performJSON(r, http.MethodPost, "/ai/config", map[string]interface{}{
		"enabled":  true,
		"provider": "deepseek",
		"model":    "deepseek-chat",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if s.cfg.AI.Enabled || s.cfg.AI.Provider != "openai" || s.cfg.AI.APIKey != "existing-key" {
		t.Fatalf("failed persistence was not rolled back: %#v", s.cfg.AI)
	}
}

func TestHandleAIConfigRequiresKeyAndCapsRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/config", s.handleAIConfig)

	w := performJSON(r, http.MethodPost, "/ai/config", map[string]interface{}{
		"enabled": true, "provider": "deepseek", "model": "deepseek-chat",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key to return 400, got %d: %s", w.Code, w.Body.String())
	}

	w = performJSON(r, http.MethodPost, "/ai/config", map[string]interface{}{
		"enabled": false, "provider": "deepseek", "system_prompt": strings.Repeat("x", AIConfigRequestMaxBytes),
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected oversized config to return 413, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAIChatReportsOversizedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}}
	s.cfg.AI.Enabled = true
	s.cfg.AI.APIKey = "test-key"
	s.cfg.AI.Provider = "deepseek"
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/chat", s.handleAIChat)

	w := performJSON(r, http.MethodPost, "/ai/chat", map[string]interface{}{
		"messages": []map[string]string{{
			"role": "user", "content": strings.Repeat("x", AIChatRequestMaxBytes),
		}},
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected oversized chat to return 413, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAIChatAdvertisesSuccessor pins the deprecation contract on the legacy
// endpoint. It is kept for external integrations, so responses must name the
// durable replacement (RFC 8594) and the successor must actually be routed.
func TestAIChatAdvertisesSuccessor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}}
	s.cfg.AI.Enabled = false // short-circuit before any provider call
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/chat", s.handleAIChat)
	r.POST(aiChatSuccessor, func(c *gin.Context) { c.Status(http.StatusAccepted) })

	w := performJSON(r, http.MethodPost, "/ai/chat", map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if got := w.Header().Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation header = %q, want \"true\"", got)
	}
	if got := w.Header().Get("Link"); !strings.Contains(got, aiChatSuccessor) || !strings.Contains(got, "successor-version") {
		t.Errorf("Link header = %q, want a successor-version link to %s", got, aiChatSuccessor)
	}
	// The advertised successor must not be a dead reference.
	w2 := performJSON(r, http.MethodPost, aiChatSuccessor, map[string]interface{}{})
	if w2.Code != http.StatusAccepted {
		t.Errorf("successor %s is not routed (got %d); the Link header would be a lie", aiChatSuccessor, w2.Code)
	}
}

func TestAIChatRejectsUnresolvedEndpoint(t *testing.T) {
	// "custom" with no endpoint used to fall back to api.openai.com and ship
	// the operator's key there. It must now fail closed with a clear message.
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}}
	s.cfg.AI.Enabled = true
	s.cfg.AI.APIKey = "test-key"
	s.cfg.AI.Provider = "custom"
	s.cfg.AI.Endpoint = ""
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Next()
	})
	r.POST("/ai/chat", s.handleAIChat)

	w := performJSON(r, http.MethodPost, "/ai/chat", map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a provider with no endpoint", w.Code)
	}
	if !strings.Contains(w.Body.String(), "endpoint") {
		t.Errorf("body = %s, want it to name the missing endpoint", w.Body.String())
	}
}

func TestTrimConversationHistoryBoundsMessagesAndPreservesUTF8(t *testing.T) {
	messages := make([]chatMessage, 0, 8)
	for i := 0; i < 8; i++ {
		messages = append(messages, chatMessage{Role: "user", Content: strings.Repeat("界", 7000) + fmt.Sprint(i)})
	}
	got := trimConversationHistory(messages)
	if len(got) >= len(messages) {
		t.Fatalf("expected old messages to be trimmed, got %d of %d", len(got), len(messages))
	}
	if !strings.Contains(got[0].Content, "Earlier conversation trimmed") {
		t.Fatalf("missing trim notice: %#v", got[0])
	}
	if !strings.HasSuffix(got[len(got)-1].Content, "7...") && !strings.HasSuffix(got[len(got)-1].Content, "...") {
		t.Fatalf("latest message was not retained: %q", got[len(got)-1].Content)
	}
	for _, message := range got {
		if !utf8.ValidString(message.Content) {
			t.Fatalf("invalid UTF-8 after truncation: %q", message.Content)
		}
		if !strings.Contains(message.Content, "Earlier conversation trimmed") && len(message.Content) > aiMaxContextMessageChars+3 {
			t.Fatalf("message exceeded cap: %d bytes", len(message.Content))
		}
	}
}
