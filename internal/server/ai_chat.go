package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// ── AI Chat Page ──────────────────────────────────────────────────────────

// aiConfigPublicView returns a redacted view of the AI configuration safe to
// send to any authenticated user. The provider API key is NEVER included
// (S1): only non-secret display fields are exposed.
func (s *Server) aiConfigPublicView() gin.H {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return gin.H{
		"enabled":          s.cfg.AI.Enabled,
		"has_api_key":      s.cfg.AI.APIKey != "",
		"provider":         s.cfg.AI.Provider,
		"model":            s.cfg.AI.Model,
		"endpoint":         s.cfg.AI.Endpoint,
		"system_prompt":    s.cfg.AI.SystemPrompt,
		"allow_execute":    s.cfg.AI.AllowExecute,
		"engagement_notes": s.cfg.AI.EngagementNotes,
	}
}

func (s *Server) aiExecutionEnabled() bool {
	if s.cfg == nil {
		return false
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg.AI.AllowExecute
}

func (s *Server) handleAIPage(c *gin.Context) {
	stats := s.getNavStats(c)
	publicConfig := s.aiConfigPublicView()
	aiEnabled, _ := publicConfig["enabled"].(bool)
	aiHasAPIKey, _ := publicConfig["has_api_key"].(bool)
	data := gin.H{
		"Title":      "ForgeC2 - AI Assistant",
		"ActiveNav":  "ai",
		"IsFullPage": true,
		// Never embed the raw AIConfig: it carries the provider API key. Send a
		// redacted view so the key is never exposed to any authenticated user
		// (including non-admin operators) via the JSON page data (S1).
		"AIConfig":     publicConfig,
		"AIConfigured": aiEnabled && aiHasAPIKey,
		"AIHasAPIKey":  aiHasAPIKey,
	}
	s.addUserToData(c, data)
	for k, v := range stats {
		data[k] = v
	}
	slog.Info("AI page rendered", "enabled", aiEnabled, "provider", publicConfig["provider"])
	s.renderPageOrJSON(c, data)
}

// ── AI Config Save ────────────────────────────────────────────────────────

func (s *Server) handleAIConfig(c *gin.Context) {
	if !s.requireAdmin(c) {
		return
	}
	var req struct {
		Enabled         bool   `json:"enabled"`
		Provider        string `json:"provider"`
		APIKey          string `json:"api_key"`
		Model           string `json:"model"`
		Endpoint        string `json:"endpoint"`
		SystemPrompt    string `json:"system_prompt"`
		EngagementNotes string `json:"engagement_notes"`
		AllowExecute    bool   `json:"allow_execute"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, AIConfigRequestMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			respondError(c, http.StatusRequestEntityTooLarge, "AI configuration request too large")
			return
		}
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Model = strings.TrimSpace(req.Model)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	// Provider acceptance comes from the shared registry so this handler, the
	// profile handler and config.Validate can no longer disagree.
	if !config.IsKnownAIProvider(req.Provider) {
		respondError(c, http.StatusBadRequest, "unsupported AI provider")
		return
	}
	// "custom" names no vendor. Requiring an endpoint is the whole point of
	// the name; defaulting it to some vendor's host would ship the operator's
	// key to a provider they never selected.
	if req.Endpoint == "" && config.AIProviderNeedsExplicitEndpoint(req.Provider) {
		respondError(c, http.StatusBadRequest, "ai endpoint is required when provider is \"custom\"")
		return
	}
	if req.Model == "" {
		req.Model = aiDefaultModel(req.Provider)
	}
	if len(req.APIKey) > 16*1024 || len(req.Model) > 200 || len(req.Endpoint) > 2048 || len(req.SystemPrompt) > 16*1024 {
		respondError(c, http.StatusBadRequest, "AI configuration field too large")
		return
	}
	if strings.ContainsAny(req.APIKey, "\r\n") {
		respondError(c, http.StatusBadRequest, "API key contains invalid characters")
		return
	}
	if len(req.EngagementNotes) > aiMaxNotesLen {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("engagement notes too large (max %d chars)", aiMaxNotesLen))
		return
	}
	if req.Endpoint != "" {
		if err := s.validateAIEndpoint(req.Endpoint); err != nil {
			respondError(c, http.StatusBadRequest, "AI endpoint is invalid or blocked")
			return
		}
	}

	s.configMu.RLock()
	hasExistingAPIKey := strings.TrimSpace(s.cfg.AI.APIKey) != ""
	s.configMu.RUnlock()
	if req.Enabled && req.APIKey == "" && !hasExistingAPIKey {
		respondError(c, http.StatusBadRequest, "API key is required when AI is enabled")
		return
	}

	slog.Info("AI config save request", "enabled", req.Enabled, "provider", req.Provider, "model", req.Model)
	s.configMu.Lock()
	previousAI := s.cfg.AI
	nextAI := previousAI
	nextAI.Enabled = req.Enabled
	nextAI.Provider = req.Provider
	if req.APIKey != "" {
		nextAI.APIKey = req.APIKey
	}
	nextAI.Model = req.Model
	nextAI.Endpoint = req.Endpoint
	nextAI.SystemPrompt = req.SystemPrompt
	nextAI.AllowExecute = req.AllowExecute
	nextAI.EngagementNotes = req.EngagementNotes
	s.cfg.AI = nextAI
	s.configMu.Unlock()
	configPath := s.configPath
	if configPath == "" {
		configPath = "config.yaml"
	}
	if err := s.cfg.Save(configPath); err != nil {
		// A failed disk write must not leave the running server on a config the
		// operator was explicitly told did not save.
		s.configMu.Lock()
		// reflect.DeepEqual, not ==: config.AI holds a []string
		// (ai.allowed_endpoints) and is therefore not comparable with ==.
		// The check still means "config is untouched since I wrote it", so a
		// concurrent reload is not clobbered by the rollback.
		if reflect.DeepEqual(s.cfg.AI, nextAI) {
			s.cfg.AI = previousAI
		}
		s.configMu.Unlock()
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "AI operation"))
		return
	}
	username, _ := c.Get("user")
	slog.Info("AI config saved", "user", username, "enabled", req.Enabled, "provider", req.Provider)
	respond(c, gin.H{"message": "AI config saved"})
}

// aiDefaultModel resolves a provider's default model from the single provider
// registry in internal/config. It used to carry its own switch statement,
// which had no "anthropic" case and therefore answered deepseek-chat for
// Anthropic users.
func aiDefaultModel(provider string) string {
	return config.AIProviderDefaultModel(provider)
}

// ── SSE Chat (streaming) ──────────────────────────────────────────────────

// aiChatSuccessor identifies the durable replacement for the legacy chat
// endpoint, used in the RFC 8594 Link header.
const aiChatSuccessor = "/api/ai/runs"

// markAIChatDeprecated flags POST /ai/chat as deprecated in-band (RFC 8594
// Deprecation + Link) and logs the caller so remaining adoption is measurable.
//
// The endpoint is kept, not removed: external integrations may still call it,
// and deleting a route is a breaking change. The successor, POST /api/ai/runs,
// persists conversations durably, enforces per-user/per-tenant run limits,
// replays events after a reconnect and records token usage; /ai/chat keeps all
// history in the browser and records nothing server-side.
func (s *Server) markAIChatDeprecated(c *gin.Context) {
	c.Header("Deprecation", "true")
	c.Header("Link", "<"+aiChatSuccessor+">; rel=\"successor-version\"")
	c.Header("X-ForgeC2-Deprecated-Endpoint", aiChatSuccessor)
	username, _ := c.Get("user")
	slog.Info("legacy AI chat endpoint used; prefer "+aiChatSuccessor,
		"user", username,
		"ip", c.ClientIP())
}

func (s *Server) handleAIChat(c *gin.Context) {
	s.markAIChatDeprecated(c)
	s.configMu.RLock()
	aiEnabled := s.cfg.AI.Enabled
	aiAPIKey := s.cfg.AI.APIKey
	aiProvider := s.cfg.AI.Provider
	aiModel := s.cfg.AI.Model
	aiEndpoint := s.cfg.AI.Endpoint
	if aiEndpoint == "" {
		aiEndpoint = s.cfg.AIEndpoint()
	}
	aiSystemPrompt := s.cfg.AI.SystemPrompt
	aiEngagementNotes := s.cfg.AI.EngagementNotes
	aiMaxConversationTurns := s.cfg.AI.MaxConversationTurns
	aiMaxToolRounds := s.cfg.AI.MaxToolRounds
	aiMaxDuplicateToolCalls := s.cfg.AI.MaxDuplicateToolCalls
	s.configMu.RUnlock()
	if !aiEnabled || aiAPIKey == "" {
		slog.Warn("AI chat blocked", "enabled", aiEnabled, "provider", aiProvider)
		respondError(c, http.StatusBadRequest, "AI not configured. Set api_key in AI settings.")
		return
	}
	// config.AIEndpoint() returns "" for providers that have no vendor default
	// ("custom" without an explicit endpoint). Fail with a clear message
	// instead of building a request against an empty base URL.
	if aiEndpoint == "" {
		slog.Error("AI endpoint unresolved", "provider", aiProvider)
		respondError(c, http.StatusBadRequest, "AI endpoint is not configured for provider "+aiProvider)
		return
	}

	var req struct {
		Messages []chatMessage `json:"messages"`
		Context  struct {
			Page    string `json:"page"`
			AgentID string `json:"agent_id"`
		} `json:"context"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, AIChatRequestMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			respondError(c, http.StatusRequestEntityTooLarge, "AI chat request too large")
			return
		}
		respondError(c, http.StatusBadRequest, "messages required")
		return
	}
	if len(req.Messages) == 0 {
		respondError(c, http.StatusBadRequest, "messages required")
		return
	}
	if len(req.Messages) > AIChatMaxMessages {
		respondError(c, http.StatusBadRequest, "too many messages")
		return
	}
	for _, message := range req.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			respondError(c, http.StatusBadRequest, "invalid message role")
			return
		}
	}
	principal := aiPrincipal{}
	if s.db != nil {
		var ok bool
		principal, ok = s.currentAIPrincipal(c)
		if !ok {
			respondError(c, http.StatusForbidden, "AI use permission required")
			return
		}
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	clearHTTPWriteDeadline(c.Writer)
	flusher, _ := c.Writer.(http.Flusher)

	model := aiModel
	if model == "" {
		model = aiDefaultModel(aiProvider)
	}

	sysPrompt := effectiveAISystemPrompt(aiSystemPrompt)
	if principal.TenantID == 0 {
		if notes := strings.TrimSpace(aiEngagementNotes); notes != "" {
			sysPrompt += "\n\n## Engagement memory (operator-maintained, persistent)\n" + notes
		}
	}
	if principal.TenantID == 0 {
		if snap := s.buildSituationSnapshot(); snap != "" {
			sysPrompt += "\n\n" + snap
		}
	}

	// Operator context: which page/agent the human is looking at right now.
	// Tools fall back to this agent when the model omits agent_id, so "dump
	// creds on this machine" works without pasting identifiers.
	reqCtx := &aiReqCtx{Principal: principal}
	if ctxAgent := strings.TrimSpace(req.Context.AgentID); ctxAgent != "" {
		if aid := s.resolveAIAgentID(reqCtx, ctxAgent); aid != "" {
			reqCtx.DefaultAgentID = aid
			var agent db.Implant
			if err := s.db.Where("id = ?", aid).First(&agent).Error; err == nil {
				label := agent.Hostname
				if label == "" {
					label = aid
				}
				sysPrompt += fmt.Sprintf("\n\n## Operator context\nOperator is currently viewing agent \"%s\" (id: %s, os: %s, user: %s). When they say \"this machine\", \"this host\" or \"the target\" without naming one, they mean THIS agent.", label, aid, agent.OS, agent.Username)
			} else {
				sysPrompt += "\n\n## Operator context\nOperator is currently viewing agent id " + aid + ". References to \"this machine\" mean that agent."
			}
		}
	}
	if page := strings.TrimSpace(req.Context.Page); page != "" {
		sysPrompt += "\nOperator's current console page: " + truncateStr(page, 120)
	}

	userMessages := trimConversationHistory(req.Messages)

	events := s.converse(model, sysPrompt, userMessages, c.Request.Context(), reqCtx, aiConversationOptions{
		provider:              aiProvider,
		endpoint:              aiEndpoint,
		apiKey:                aiAPIKey,
		maxConversationTurns:  aiMaxConversationTurns,
		maxToolRounds:         aiMaxToolRounds,
		maxDuplicateToolCalls: aiMaxDuplicateToolCalls,
	})
	heartbeat := time.NewTicker(AIStreamHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			// Explicit ping events keep reverse proxies and the browser watchdog
			// alive while a provider is still thinking before its first token.
			s.writeSSE(c, flusher, "ping", strconv.FormatInt(time.Now().Unix(), 10))
		case evt, ok := <-events:
			if !ok {
				return
			}
			s.writeSSE(c, flusher, evt.Type, evt.Data)
		}
	}
}

// aiMaxNotesLen bounds the persistent engagement memory so a runaway tool
// loop cannot balloon the system prompt.
const aiMaxNotesLen = 8000

// aiMaxContextChars bounds the conversation payload sent to the provider
// (~4 chars/token heuristic). Oldest turns beyond the budget are collapsed.
const aiMaxContextChars = 48000

const aiMaxContextMessageChars = 16000

// trimConversationHistory collapses the oldest turns when the conversation
// grows past the context budget: everything except the system prompt and the
// most recent messages is replaced by a single deterministic summary marker.
// This keeps long engagements usable without an extra summarization API call.
func trimConversationHistory(msgs []chatMessage) []chatMessage {
	normalized := make([]chatMessage, len(msgs))
	for i, message := range msgs {
		if len(message.Content) > aiMaxContextMessageChars {
			message.Content = truncateStr(message.Content, aiMaxContextMessageChars)
		}
		normalized[i] = message
	}
	msgs = normalized
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}
	if total <= aiMaxContextChars {
		return msgs
	}
	// Walk from the newest backwards accumulating until over half the budget;
	// everything older is dropped into a trim notice.
	keepFrom := len(msgs)
	kept := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		kept += len(msgs[i].Content)
		keepFrom = i
		if kept > aiMaxContextChars/2 {
			break
		}
	}
	if keepFrom == 0 {
		return msgs
	}
	trimmed := msgs[keepFrom:]
	// "user" role keeps provider compatibility: the Claude converter folds
	// every system-role message into the top-level prompt, which would
	// clobber the real system prompt.
	notice := chatMessage{
		Role: "user",
		Content: fmt.Sprintf(
			"[System] Earlier conversation trimmed for context budget: %d messages removed. Key facts from those turns are no longer visible — ask the operator if needed.",
			keepFrom),
	}
	out := make([]chatMessage, 0, len(trimmed)+1)
	out = append(out, notice)
	out = append(out, trimmed...)
	return out
}

// agentIDsOf returns the distinct, non-empty agent IDs of the given tasks in
// first-seen order. Dedup matters: a pending-approval page routinely holds
// several tasks for one agent, and a per-row lookup would repeat the query.
func agentIDsOf(tasks []db.Task) []string {
	seen := make(map[string]struct{}, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t.AgentID == "" {
			continue
		}
		if _, dup := seen[t.AgentID]; dup {
			continue
		}
		seen[t.AgentID] = struct{}{}
		ids = append(ids, t.AgentID)
	}
	return ids
}

// agentHostnamesByID maps agent ID -> hostname for the requested agents, in a
// single query. A missing or unreadable row simply has no entry, which is what
// the previous per-row lookup produced (hostname stayed "" on error).
//
// Tenant scope is deliberately not applied: the previous per-row SELECT had
// none, and narrowing it here would silently blank hostnames for rows whose
// task and agent tenants disagree. Tenant isolation of this endpoint is a
// separate open question.
func (s *Server) agentHostnamesByID(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	var rows []db.Implant
	if err := s.db.Model(&db.Implant{}).Select("id", "hostname").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		slog.Warn("agent hostname lookup failed", "agents", len(ids), "err", err)
		return out
	}
	for _, r := range rows {
		out[r.ID] = r.Hostname
	}
	return out
}

func (s *Server) handleAIPendingTasks(c *gin.Context) {
	var tasks []db.Task
	if err := s.tenantScope(s.db.Where("status = ? AND created_by = ?", "pending_approval", "ai"), c).Order("created_at desc").Limit(20).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}
	type pendingTask struct {
		ID        uint   `json:"id"`
		AgentID   string `json:"agent_id"`
		Hostname  string `json:"hostname"`
		Command   string `json:"command"`
		Type      string `json:"type"`
		CreatedAt string `json:"created_at"`
	}
	// Hostnames for up to 20 rows, fetched in one query. The loop used to run
	// a SELECT per task — and repeated the same SELECT for tasks sharing an
	// agent — so a full page cost 21 round trips to render one list.
	hostnames := s.agentHostnamesByID(agentIDsOf(tasks))
	var out []pendingTask
	for _, t := range tasks {
		out = append(out, pendingTask{
			ID: t.ID, AgentID: t.AgentID, Hostname: hostnames[t.AgentID],
			Command: truncateStr(t.Command, 200), Type: t.Type,
			CreatedAt: t.CreatedAt.Format(time.RFC3339),
		})
	}
	respond(c, gin.H{"tasks": out})
}

type aiToolLimits struct {
	maxConversationTurns  int
	maxToolRounds         int
	maxDuplicateToolCalls int
}

func resolveAIToolLimits(maxConversationTurns, maxToolRounds, maxDuplicateToolCalls int) aiToolLimits {
	// 0 in config means "unlimited", but an unbounded tool loop never
	// closes the SSE stream and the composer stays stuck on loading.
	// Apply a hard safety ceiling so the conversation always terminates.
	if maxConversationTurns <= 0 {
		maxConversationTurns = AISafetyMaxTurns
	}
	if maxToolRounds <= 0 {
		maxToolRounds = AISafetyMaxToolRounds
	}
	if maxDuplicateToolCalls <= 0 {
		maxDuplicateToolCalls = AISafetyMaxDuplicateTools
	}
	return aiToolLimits{
		maxConversationTurns:  maxConversationTurns,
		maxToolRounds:         maxToolRounds,
		maxDuplicateToolCalls: maxDuplicateToolCalls,
	}
}

// clearHTTPWriteDeadline drops the server WriteTimeout for a long-lived SSE
// response. gin.Context.Writer wraps the net/http ResponseWriter, so we unwrap
// a few layers until SetWriteDeadline sticks.
func clearHTTPWriteDeadline(w http.ResponseWriter) {
	for i := 0; i < 6 && w != nil; i++ {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err == nil {
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// aiUsage carries provider token accounting for one conversation.
type aiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// aiReqCtx carries per-request context through the tool loop: the agent the
// operator is currently viewing, used as fallback target when a tool call
// omits agent_id ("run mimikatz on this box" while looking at that box).
type aiReqCtx struct {
	DefaultAgentID         string
	Principal              aiPrincipal
	SessionID              uint
	RunID                  string
	AllowLowRiskWrites     bool
	ApprovedIntent         bool
	KnowledgeCollectionIDs []uint
	DisableTools           bool
}

type aiConversationOptions struct {
	provider              string
	endpoint              string
	apiKey                string
	maxConversationTurns  int
	maxToolRounds         int
	maxDuplicateToolCalls int
}
