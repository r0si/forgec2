package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/gin-gonic/gin"
)

// ── Conversation loop + provider streaming ────────────────────────────────

// converse runs the LLM conversation loop with tool calling, returning SSE events
func (s *Server) converse(model, systemPrompt string, userMessages []chatMessage, ctx context.Context, reqCtx *aiReqCtx, options aiConversationOptions) <-chan sseEvent {
	ch := make(chan sseEvent, AIStreamChanBuf)

	go func() {
		defer close(ch)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
				select {
				case ch <- sseEvent{"error", "AI processing failed unexpectedly"}:
				case <-ctx.Done():
				}
			}
		}()

		send := func(evt sseEvent) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- evt:
				return true
			}
		}

		if !send(aiProgressEvent("analyzing", 1)) {
			return
		}

		messages := make([]chatMessage, 0, len(userMessages)+1)
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
		messages = append(messages, userMessages...)
		tools := s.buildToolsForContext(reqCtx)
		wantReasoning := aiShouldRequestReasoning(options.provider, options.endpoint)

		limits := resolveAIToolLimits(
			options.maxConversationTurns,
			options.maxToolRounds,
			options.maxDuplicateToolCalls,
		)
		toolCallHistory := make(map[string]int) // track tool calls to prevent infinite loops
		consecutiveTools := 0
		emptyContentRounds := 0
		retriedNoTools := false

		for turn := 0; turn < limits.maxConversationTurns; turn++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if turn > 0 && !retriedNoTools {
				if !send(aiProgressEvent("synthesizing", turn+1)) {
					return
				}
			}

			toolsThisRound := tools
			if retriedNoTools {
				toolsThisRound = nil
			}
			body := chatRequest{
				Model:    model,
				Messages: messages,
				Stream:   true,
				Tools:    toolsThisRound,
			}
			if wantReasoning {
				body.Reasoning = &aiReasoningHint{Enabled: true}
			}
			if turn > 0 && !retriedNoTools {
				body.ToolChoice = "auto"
			}

			payload, ok := marshalJSONSafe(body)
			if !ok {
				send(sseEvent{"error", "failed to marshal request"})
				return
			}
			roundCtx, cancelRound := context.WithTimeout(ctx, AIRoundTimeout)
			resp, err := s.aiDoRequestWithConfig(roundCtx, payload, aiProviderRequestConfig{
				provider: options.provider,
				endpoint: options.endpoint,
				apiKey:   options.apiKey,
				model:    model,
			})
			if err != nil {
				cancelRound()
				send(sseEvent{"error", aiFlattenError(err)})
				return
			}

			var toolCalls []toolCall
			var content, reasoning, finishReason string
			var turnUsage aiUsage
			var streamErr error
			// Keep response-body and per-round context cleanup in the same scope
			// as parsing. If a provider parser panics or returns early, the
			// top-level recovery still gets a closed connection and cancelled
			// deadline instead of leaking resources until the parent run ends.
			func() {
				defer resp.Body.Close()
				defer cancelRound()
				if config.AIProviderUsesClaudeWire(options.provider) {
					toolCalls, content, reasoning, finishReason, turnUsage, streamErr = s.parseClaudeStream(resp, ch, roundCtx)
				} else {
					toolCalls, content, reasoning, finishReason, turnUsage, streamErr = s.parseStreamChunks(resp, ch, roundCtx)
				}
			}()
			if streamErr != nil {
				send(sseEvent{"error", aiFlattenError(streamErr)})
				return
			}

			think, visible := splitThinkBlocks(content)
			if think != "" {
				reasoning = joinNonEmpty(reasoning, think, "\n\n")
				content = visible
			}
			if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) == "" && len(toolCalls) == 0 && !retriedNoTools && len(tools) > 0 {
				slog.Info("AI empty reply with tools, retrying without tools", "model", model)
				retriedNoTools = true
				turn--
				continue
			}
			retriedNoTools = false
			if reasoning != "" {
				if !send(sseEvent{"reasoning", reasoning}) {
					return
				}
			}

			// Report this turn's token delta; the browser accumulates it across
			// tool rounds and messages without double-counting prior turns.
			if ub, ok := marshalJSONSafe(turnUsage); ok {
				if !send(sseEvent{"usage", string(ub)}) {
					return
				}
			}

			// Safety: cap content length without splitting UTF-8 text.
			if len(content) > AIResponseTruncLen {
				content, _ = appendAIResponseText("", content)
			}

			if finishReason == "tool_calls" && len(toolCalls) > 0 {
				// Auto-disable tools after 3 empty rounds (no meaningful content, likely model stuck)
				if len(strings.TrimSpace(content)) < 50 {
					emptyContentRounds++
					if emptyContentRounds >= 3 {
						send(sseEvent{"text", "\n\n[Auto-disabled tools after 3 empty rounds — continuing as plain answer]"})
						tools = nil
						retriedNoTools = true
					}
				} else {
					emptyContentRounds = 0
				}
				// Prevent infinite tool loops: semantic dedup with canonical JSON (sorted keys, lower, no ws)
				normalizeKey := func(s string) string {
					var v interface{}
					if err := json.Unmarshal([]byte(s), &v); err == nil {
						if b, err := json.Marshal(canonicalJSON(v)); err == nil {
							return strings.ToLower(strings.Join(strings.Fields(string(b)), ""))
						}
					}
					return strings.ToLower(strings.Join(strings.Fields(s), ""))
				}
				var newCalls []toolCall
				for _, tc := range toolCalls {
					// Ensure tool_call id is present for frontend key stability (latency-first)
					if strings.TrimSpace(tc.ID) == "" {
						tc.ID = fmt.Sprintf("tool-%d-%s", len(toolCallHistory), tc.Function.Name)
					}
					key := tc.Function.Name + ":" + normalizeKey(tc.Function.Arguments)
					// Stricter for read-only list_agents: only 1 duplicate to avoid enumeration loops
					limit := limits.maxDuplicateToolCalls
					if tc.Function.Name == "list_agents" && limit > 1 {
						limit = 1
					}
					if limit > 0 && toolCallHistory[key] >= limit {
						continue
					}
					toolCallHistory[key]++
					newCalls = append(newCalls, tc)
				}
				if len(newCalls) == 0 {
					send(sseEvent{"text", "Duplicate tool calls detected, loop terminated."})
					send(sseEvent{"done", "ok"})
					return
				}
				consecutiveTools++
				if consecutiveTools > limits.maxToolRounds {
					send(sseEvent{"text", content + "\n\n[Max tool calls reached]"})
					send(sseEvent{"done", "ok"})
					return
				}

				assistMsg := chatMessage{Role: "assistant", Content: content, ToolCalls: newCalls}
				messages = append(messages, assistMsg)
				type toolOutcome struct {
					tc     toolCall
					result string
				}
				outcomes := make([]toolOutcome, len(newCalls))
				var wg sync.WaitGroup
				for i, tc := range newCalls {
					wg.Add(1)
					go func(i int, tc toolCall) {
						defer wg.Done()
						defer func() {
							if r := recover(); r != nil {
								outcomes[i].result = `{"error":"tool panicked"}`
							}
						}()
						outcomes[i].tc = tc
						outcomes[i].result = compactToolResultJSON(s.executeToolCtx(reqCtx, tc.Function.Name, tc.Function.Arguments))
					}(i, tc)
				}
				wg.Wait()
				for _, oc := range outcomes {
					tc := oc.tc
					result := oc.result
					if payload, ok := marshalJSONSafe(map[string]string{"id": tc.ID, "name": tc.Function.Name}); ok {
						if !send(sseEvent{"tool_start", string(payload)}) {
							return
						}
					} else if !send(sseEvent{"tool_start", tc.Function.Name}) {
						return
					}
					if !send(sseEvent{"tool", fmt.Sprintf(`{"id":"%s","name":"%s","result":%s}`,
						tc.ID, tc.Function.Name, result)}) {
						return
					}
					messages = append(messages, chatMessage{
						Role: "tool", ToolCallID: tc.ID, Content: result,
					})
				}
				continue
			}

			// No tool calls: transition into the final response.
			if !send(sseEvent{"clear", ""}) {
				return
			}
			if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) != "" {
				// Some thinking models (OpenRouter/DeepSeek-style) put the
				// entire reply in delta.reasoning and leave content empty.
				content = reasoning
			}
			if content != "" && !send(sseEvent{"text", content}) {
				return
			}
			if content == "" && !send(sseEvent{"text", "The model returned an empty reply."}) {
				return
			}
			send(sseEvent{"done", "ok"})
			return
		}
		send(sseEvent{"text", "[Max conversation turns reached]"})
		send(sseEvent{"done", "ok"})
	}()

	return ch
}

// parseStreamChunks reads an OpenAI-compatible SSE stream and accumulates tool
// calls. Provider reasoning (delta.reasoning / reasoning_content / <think>)
// is forwarded as SSE "reasoning" events so the UI can show the thinking
// process without mixing it into the final answer until the stream ends.
func (s *Server) parseStreamChunks(resp *http.Response, ch chan<- sseEvent, ctx context.Context) (toolCalls []toolCall, content, reasoning, finishReason string, usage aiUsage, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reader := io.Reader(resp.Body)
	buf := make([]byte, AIStreamBufSize)
	var leftover string

	type buildingTool struct {
		Index     int
		ID        string
		Name      string
		Arguments strings.Builder
	}
	var buildingTools []*buildingTool
	reasoningStarted := false
	answeringStarted := false
	reasoningTruncated := false
	contentTruncated := false
	lastReasoningSent := 0
	lastContentSent := 0
	sawTerminalEvent := false

	sendChunk := func(evt sseEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- evt:
			return true
		}
	}
	flushBufferedSnapshots := func() {
		if reasoning != "" && len(reasoning) != lastReasoningSent && sendChunk(sseEvent{"reasoning", reasoning}) {
			lastReasoningSent = len(reasoning)
		}
		if content != "" && len(content) != lastContentSent && sendChunk(sseEvent{"text", content}) {
			lastContentSent = len(content)
		}
	}

	for {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return
		default:
		}
		n, readErr := reader.Read(buf)
		// A provider is allowed to close the response without a trailing newline.
		// Process the buffered suffix on EOF instead of silently losing its final
		// token/finish_reason (the UI otherwise appears randomly truncated).
		if n > 0 || (readErr == io.EOF && leftover != "") {
			data := leftover + string(buf[:n])
			if readErr == io.EOF {
				data += "\n"
			}
			lines := strings.Split(data, "\n")
			// Last element may be incomplete
			leftover = lines[len(lines)-1]
			lines = lines[:len(lines)-1]

			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				jsonStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if jsonStr == "" {
					continue
				}
				if jsonStr == "[DONE]" {
					sawTerminalEvent = true
					continue
				}

				var chunk struct {
					Error *struct {
						Message string `json:"message"`
					} `json:"error"`
					Choices []struct {
						Delta struct {
							Content          json.RawMessage `json:"content"`
							Reasoning        json.RawMessage `json:"reasoning"`
							ReasoningContent json.RawMessage `json:"reasoning_content"`
							ReasoningDetails []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							} `json:"reasoning_details"`
							ToolCalls []struct {
								Index    int    `json:"index"`
								ID       string `json:"id"`
								Type     string `json:"type"`
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
					Usage *struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
					continue
				}
				if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
					flushBufferedSnapshots()
					err = errors.New(chunk.Error.Message)
					return
				}
				if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
					usage = aiUsage{
						PromptTokens:     chunk.Usage.PromptTokens,
						CompletionTokens: chunk.Usage.CompletionTokens,
					}
				}
				if len(chunk.Choices) == 0 {
					continue
				}
				delta := chunk.Choices[0].Delta
				fr := chunk.Choices[0].FinishReason
				if fr != "" {
					sawTerminalEvent = true
				}

				if piece := streamReasoningDelta(delta.Reasoning, delta.ReasoningContent, delta.ReasoningDetails); piece != "" && !reasoningTruncated {
					reasoning, reasoningTruncated = appendAIResponseText(reasoning, piece)
					if !reasoningStarted {
						if !sendChunk(aiProgressEvent("reasoning", 0)) {
							err = ctx.Err()
							return
						}
						reasoningStarted = true
					}
					if shouldEmitAIStreamSnapshot(reasoning, lastReasoningSent, piece, reasoningTruncated) {
						if !sendChunk(sseEvent{"reasoning", reasoning}) {
							err = ctx.Err()
							return
						}
						lastReasoningSent = len(reasoning)
					}
				}
				if piece := decodeStreamText(delta.Content); piece != "" && !contentTruncated {
					if !answeringStarted {
						if !sendChunk(aiProgressEvent("answering", 0)) {
							err = ctx.Err()
							return
						}
						answeringStarted = true
					}
					content, contentTruncated = appendAIResponseText(content, piece)
					if shouldEmitAIStreamSnapshot(content, lastContentSent, piece, contentTruncated) {
						if !sendChunk(sseEvent{"text", content}) {
							err = ctx.Err()
							return
						}
						lastContentSent = len(content)
					}
					if contentTruncated {
						return
					}
				}
				// Accumulate tool calls
				for _, tc := range delta.ToolCalls {
					for len(buildingTools) <= tc.Index {
						buildingTools = append(buildingTools, &buildingTool{Index: len(buildingTools)})
					}
					bt := buildingTools[tc.Index]
					if tc.ID != "" {
						bt.ID = tc.ID
					}
					if tc.Function.Name != "" {
						bt.Name = tc.Function.Name
					}
					bt.Arguments.WriteString(tc.Function.Arguments)
				}

				if fr != "" {
					finishReason = fr
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			flushBufferedSnapshots()
			err = readErr
			return
		}
	}
	if !sawTerminalEvent && (content != "" || reasoning != "" || len(buildingTools) > 0) {
		flushBufferedSnapshots()
		err = io.ErrUnexpectedEOF
		return
	}

	// Convert building tools to tool calls
	for _, bt := range buildingTools {
		if bt.Name != "" {
			toolCalls = append(toolCalls, toolCall{
				ID:   bt.ID,
				Type: "function",
				Function: toolCallFunc{
					Name:      bt.Name,
					Arguments: bt.Arguments.String(),
				},
			})
		}
	}
	return
}

type sseEvent struct {
	Type string
	Data string
}

func aiProgressEvent(stage string, turn int) sseEvent {
	payload, ok := marshalJSONSafe(map[string]interface{}{"stage": stage, "turn": turn})
	if !ok {
		return sseEvent{"progress", fmt.Sprintf(`{"stage":%q}`, stage)}
	}
	return sseEvent{"progress", string(payload)}
}

// aiFlattenError formats an AI request error into a single-line, user-visible
// message. aiDoRequest already wraps useful diagnostics (HTTP status, upstream
// body) in the error, so rather than the generic "AI request failed" we surface
// that detail to the operator's chat view. The API key is never part of these
// errors; any echoed upstream body is trimmed to a read-safe length.
func aiFlattenError(err error) string {
	if err == nil {
		return "AI request failed"
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "AI request failed"
	}
	if len(msg) > AIErrorBodyTruncLen {
		msg = msg[:AIErrorBodyTruncLen] + "..."
	}
	return "AI request failed: " + msg
}

// aiBuildChatURL resolves the final chat endpoint from a configured base URL.
// Providers that use the OpenAI-compatible /chat/completions shape (deepseek,
// openai, qianwen, zhipu, longcat, custom) get the suffix appended; but an
// operator may supply the full chat-completions URL (e.g. OpenRouter examples
// give https://.../api/v1/chat/completions) — appending again would 404, so we
// use it as-is. Claude uses /messages instead.
func aiBuildChatURL(baseURL, provider string) string {
	if config.AIProviderUsesClaudeWire(provider) {
		if strings.HasSuffix(baseURL, "/messages") {
			return baseURL
		}
		return baseURL + "/messages"
	}
	if strings.HasSuffix(baseURL, "/chat/completions") {
		return baseURL
	}
	return baseURL + "/chat/completions"
}

// aiStreamClient is used for provider chat streams. The short httpClient
// Timeout (30s) covers the entire request including body read, so a normal
// streamed reply (or a tool round) would get canceled mid-token and leave
// the UI sitting on a half-open SSE connection.
func (s *Server) aiStreamClient() *http.Client {
	base := s.httpClientLong
	if base == nil {
		base = s.httpClient
	}
	// aiSSRFClient (not ssrfSafeClient) so redirects are validated against the
	// AI allowlist: an allowlisted local endpoint may redirect within the
	// allowlist but still cannot bounce the API key to an arbitrary internal
	// address.
	c := s.aiSSRFClient(base)
	c.Timeout = 0
	if t, ok := c.Transport.(*http.Transport); ok && t != nil {
		cloned := t.Clone()
		cloned.ResponseHeaderTimeout = 45 * time.Second
		c.Transport = cloned
	}
	return c
}

type aiProviderRequestConfig struct {
	enabled  bool
	provider string
	endpoint string
	apiKey   string
	model    string
}

func (s *Server) aiProviderRequestConfigSnapshot() (aiProviderRequestConfig, error) {
	if s.cfg == nil {
		return aiProviderRequestConfig{}, errors.New("AI configuration unavailable")
	}
	s.configMu.RLock()
	snapshot := aiProviderRequestConfig{
		enabled:  s.cfg.AI.Enabled,
		provider: s.cfg.AI.Provider,
		endpoint: s.cfg.AIEndpoint(),
		apiKey:   s.cfg.AI.APIKey,
		model:    s.cfg.AI.Model,
	}
	s.configMu.RUnlock()
	// An empty endpoint means the configured provider names no vendor and no
	// endpoint was supplied (only "custom" can reach this state). The one-shot
	// assist endpoints read this snapshot, so it is the path that used to send
	// the key to whatever host a fallback branch picked.
	if snapshot.enabled && strings.TrimSpace(snapshot.apiKey) != "" && snapshot.endpoint == "" {
		return aiProviderRequestConfig{}, fmt.Errorf(
			"AI provider %q has no endpoint configured; set ai.endpoint", snapshot.provider)
	}
	return snapshot, nil
}

func (s *Server) aiDoRequest(ctx context.Context, payload []byte) (*http.Response, error) {
	snapshot, err := s.aiProviderRequestConfigSnapshot()
	if err != nil {
		return nil, err
	}
	return s.aiDoRequestWithConfig(ctx, payload, snapshot)
}

func (s *Server) aiDoRequestWithConfig(ctx context.Context, payload []byte, snapshot aiProviderRequestConfig) (*http.Response, error) {
	// Keep one coherent provider snapshot for the full retry sequence. Config
	// reloads may otherwise change the endpoint, provider, parser, or credential
	// while a request is in flight.
	baseURL := strings.TrimRight(snapshot.endpoint, "/")
	provider := snapshot.provider
	apiKey := snapshot.apiKey
	model := snapshot.model
	hostAndPath := baseURL
	hostAndPath = strings.TrimPrefix(hostAndPath, "https://")
	hostAndPath = strings.TrimPrefix(hostAndPath, "http://")
	if !strings.Contains(hostAndPath, "/") {
		baseURL += "/v1"
	}

	// SSRF guard: resolve the endpoint and reject any loopback/private/
	// link-local/metadata address (incl. DNS-rebinding), matching the outbound
	// fetch protection used elsewhere (S4). Also validates the URL is
	// well-formed. Operators running a local model can permit a specific
	// host:port via ai.allowed_endpoints; nothing else is relaxed.
	if err := s.validateAIEndpoint(baseURL); err != nil {
		return nil, fmt.Errorf("AI endpoint blocked: %w", err)
	}

	var urlStr string
	if config.AIProviderUsesClaudeWire(provider) {
		urlStr = aiBuildChatURL(baseURL, provider)
		payload = s.buildClaudeRequest(payload)
	} else {
		urlStr = aiBuildChatURL(baseURL, "")
	}

	slog.Info("AI API request", "url", urlStr, "model", model, "provider", provider)

	backoff := []time.Duration{time.Second, 3 * time.Second, 7 * time.Second}
	var lastErr error
	// ssrfSafeClient re-validates every redirect hop so a public endpoint
	// cannot pivot into internal targets while carrying the API key (S4).
	aiClient := s.aiStreamClient()
	for attempt := 0; attempt <= aiRetryMax; attempt++ {
		// A request body is consumed by Client.Do. Rebuild the request for each
		// attempt so retries carry the same payload instead of an empty body.
		httpReq, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if config.AIProviderUsesClaudeWire(provider) {
			httpReq.Header.Set("x-api-key", apiKey)
			httpReq.Header.Set("anthropic-version", "2023-06-01")
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		if provider == "deepseek" {
			httpReq.Header.Set("Accept", "application/json")
		}

		resp, err := aiClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			if attempt < aiRetryMax {
				slog.Warn("AI API request failed, retrying", "attempt", attempt+1, "error", err)
				if err := waitAIBackoff(ctx, backoff[attempt]); err != nil {
					return nil, fmt.Errorf("request cancelled: %w", err)
				}
				continue
			}
			return nil, lastErr
		}
		if resp.StatusCode != 200 {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				slog.Warn("Failed to read AI error response body", "status", resp.StatusCode, "read_error", readErr)
			}
			bodyStr := string(body)
			if len(bodyStr) > AIErrorBodyTruncLen {
				bodyStr = bodyStr[:AIErrorBodyTruncLen] + "..."
			}
			if attempt < aiRetryMax && (resp.StatusCode == 429 || resp.StatusCode >= 500) {
				slog.Warn("AI API retryable error", "status", resp.StatusCode, "attempt", attempt+1)
				if err := waitAIBackoff(ctx, backoff[attempt]); err != nil {
					return nil, fmt.Errorf("request cancelled: %w", err)
				}
				continue
			}
			slog.Error("AI API error", "status", resp.StatusCode, "url", redactURLForLog(urlStr), "body", bodyStr)
			return nil, fmt.Errorf("API %d from %s: %s", resp.StatusCode, urlStr, bodyStr)
		}
		return resp, nil
	}
	return nil, lastErr
}

func waitAIBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Server) writeSSE(c *gin.Context, flusher http.Flusher, event string, data string) {
	fmt.Fprintf(c.Writer, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(c.Writer, "data: %s\n", line)
	}
	fmt.Fprintf(c.Writer, "\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func appendAIResponseText(current, piece string) (string, bool) {
	next := current + piece
	if len(next) <= AIResponseTruncLen {
		return next, false
	}
	cut := AIResponseTruncLen
	for cut > 0 && !utf8.RuneStart(next[cut]) {
		cut--
	}
	return next[:cut] + "\n\n[Response truncated]", true
}

func shouldEmitAIStreamSnapshot(current string, lastSent int, piece string, truncated bool) bool {
	return lastSent == 0 || truncated || len(current)-lastSent >= AIStreamEmitMinBytes || strings.Contains(piece, "\n")
}

// ── Claude provider ───────────────────────────────────────────────────────

// buildClaudeRequest converts an OpenAI-format JSON payload to Anthropic Claude format
func (s *Server) buildClaudeRequest(openAIPayload []byte) []byte {
	var req struct {
		Model     string        `json:"model"`
		Messages  []chatMessage `json:"messages"`
		Stream    bool          `json:"stream"`
		Tools     []toolDef     `json:"tools,omitempty"`
		MaxTokens int           `json:"max_tokens,omitempty"`
	}
	if err := json.Unmarshal(openAIPayload, &req); err != nil {
		slog.Warn("Failed to parse OpenAI payload for Claude conversion", "error", err)
		return openAIPayload
	}

	// Build Claude-format messages (system is top-level, not a message)
	var claudeMessages []map[string]interface{}
	var systemPrompt string
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			continue
		}
		claudeMsg := map[string]interface{}{"role": msg.Role}
		if msg.Content != "" {
			claudeMsg["content"] = msg.Content
		}
		// Convert tool calls to Claude format
		if len(msg.ToolCalls) > 0 {
			var claudeToolUses []map[string]interface{}
			for _, tc := range msg.ToolCalls {
				claudeToolUses = append(claudeToolUses, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": parseJSONMap(tc.Function.Arguments),
				})
			}
			claudeMsg["content"] = claudeToolUses
		}
		// Tool results
		if msg.Role == "tool" {
			claudeMsg["content"] = []map[string]interface{}{{
				"type":        "tool_result",
				"tool_use_id": msg.ToolCallID,
				"content":     msg.Content,
			}}
		}
		claudeMessages = append(claudeMessages, claudeMsg)
	}

	// Claude tools format
	var claudeTools []map[string]interface{}
	for _, t := range req.Tools {
		claudeTools = append(claudeTools, map[string]interface{}{
			"name":         t.Function.Name,
			"description":  t.Function.Description,
			"input_schema": t.Function.Parameters,
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ClaudeMaxTokens
	}
	claudeReq := map[string]interface{}{
		"model":      req.Model,
		"messages":   claudeMessages,
		"max_tokens": maxTokens,
		"stream":     req.Stream,
	}
	if systemPrompt != "" {
		claudeReq["system"] = systemPrompt
	}
	if len(claudeTools) > 0 {
		claudeReq["tools"] = claudeTools
	}

	b, ok := marshalJSONSafe(claudeReq)
	if !ok {
		return nil
	}
	return b
}

func (s *Server) parseClaudeStream(resp *http.Response, ch chan<- sseEvent, ctx context.Context) (toolCalls []toolCall, content, reasoning, finishReason string, usage aiUsage, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reader := io.Reader(resp.Body)
	buf := make([]byte, AIStreamBufSize)
	var leftover string

	type buildingClaudeTool struct {
		ID        string
		Name      string
		Arguments strings.Builder
	}
	var buildingTools []*buildingClaudeTool
	answeringStarted := false
	reasoningStarted := false
	reasoningTruncated := false
	contentTruncated := false
	lastReasoningSent := 0
	lastContentSent := 0
	sawTerminalEvent := false

	sendChunk := func(evt sseEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- evt:
			return true
		}
	}
	flushBufferedSnapshots := func() {
		if reasoning != "" && len(reasoning) != lastReasoningSent && sendChunk(sseEvent{"reasoning", reasoning}) {
			lastReasoningSent = len(reasoning)
		}
		if content != "" && len(content) != lastContentSent && sendChunk(sseEvent{"text", content}) {
			lastContentSent = len(content)
		}
	}

	for {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return
		default:
		}
		n, readErr := reader.Read(buf)
		if n > 0 || (readErr == io.EOF && leftover != "") {
			data := leftover + string(buf[:n])
			if readErr == io.EOF {
				data += "\n"
			}
			lines := strings.Split(data, "\n")
			leftover = lines[len(lines)-1]
			lines = lines[:len(lines)-1]

			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || !strings.HasPrefix(line, "data:") {
					continue
				}
				jsonStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if jsonStr == "" {
					continue
				}
				if jsonStr == "[DONE]" {
					sawTerminalEvent = true
					continue
				}

				var event struct {
					Type  string `json:"type"`
					Index int    `json:"index"`
					Delta struct {
						Type        string `json:"type"`
						Text        string `json:"text"`
						Thinking    string `json:"thinking"`
						PartialJSON string `json:"partial_json"`
						StopReason  string `json:"stop_reason"`
					} `json:"delta"`
					ContentBlock struct {
						Type string `json:"type"`
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"content_block"`
					Message *struct {
						Usage *struct {
							InputTokens int `json:"input_tokens"`
						} `json:"usage"`
					} `json:"message"`
					Usage *struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
					Error *struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(jsonStr), &event); err != nil {
					continue
				}
				if event.Message != nil && event.Message.Usage != nil && event.Message.Usage.InputTokens > 0 {
					usage.PromptTokens = event.Message.Usage.InputTokens
				}
				if event.Usage != nil && event.Usage.OutputTokens > 0 {
					usage.CompletionTokens = event.Usage.OutputTokens
				}

				switch event.Type {
				case "error":
					flushBufferedSnapshots()
					message := "Claude stream error"
					if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
						message = event.Error.Message
					}
					err = errors.New(message)
					return
				case "message_stop":
					sawTerminalEvent = true
				case "content_block_delta":
					if (event.Delta.Type == "thinking_delta" || event.Delta.Type == "reasoning_delta") && event.Delta.Thinking+event.Delta.Text != "" && !reasoningTruncated {
						piece := event.Delta.Thinking
						if piece == "" {
							piece = event.Delta.Text
						}
						reasoning, reasoningTruncated = appendAIResponseText(reasoning, piece)
						if !reasoningStarted {
							if !sendChunk(aiProgressEvent("reasoning", 0)) {
								err = ctx.Err()
								return
							}
							reasoningStarted = true
						}
						if shouldEmitAIStreamSnapshot(reasoning, lastReasoningSent, piece, reasoningTruncated) {
							if !sendChunk(sseEvent{"reasoning", reasoning}) {
								err = ctx.Err()
								return
							}
							lastReasoningSent = len(reasoning)
						}
					} else if event.Delta.Type == "text_delta" && event.Delta.Text != "" && !contentTruncated {
						if !answeringStarted {
							if !sendChunk(aiProgressEvent("answering", 0)) {
								err = ctx.Err()
								return
							}
							answeringStarted = true
						}
						content, contentTruncated = appendAIResponseText(content, event.Delta.Text)
						if shouldEmitAIStreamSnapshot(content, lastContentSent, event.Delta.Text, contentTruncated) {
							if !sendChunk(sseEvent{"text", content}) {
								err = ctx.Err()
								return
							}
							lastContentSent = len(content)
						}
						if contentTruncated {
							return
						}
					} else if event.Delta.Type == "input_json_delta" && event.Delta.PartialJSON != "" {
						for len(buildingTools) <= event.Index {
							buildingTools = append(buildingTools, &buildingClaudeTool{})
						}
						buildingTools[event.Index].Arguments.WriteString(event.Delta.PartialJSON)
					}
				case "content_block_start":
					if event.ContentBlock.Type == "tool_use" {
						for len(buildingTools) <= event.Index {
							buildingTools = append(buildingTools, &buildingClaudeTool{})
						}
						buildingTools[event.Index].ID = event.ContentBlock.ID
						buildingTools[event.Index].Name = event.ContentBlock.Name
					}
				case "message_delta":
					if event.Delta.StopReason != "" {
						finishReason = event.Delta.StopReason
						sawTerminalEvent = true
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			flushBufferedSnapshots()
			err = readErr
			return
		}
	}
	if !sawTerminalEvent && (content != "" || reasoning != "" || len(buildingTools) > 0) {
		flushBufferedSnapshots()
		err = io.ErrUnexpectedEOF
		return
	}

	for _, bt := range buildingTools {
		if bt.Name != "" {
			toolCalls = append(toolCalls, toolCall{
				ID:   bt.ID,
				Type: "function",
				Function: toolCallFunc{
					Name:      bt.Name,
					Arguments: bt.Arguments.String(),
				},
			})
		}
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	} else if finishReason == "" {
		finishReason = "stop"
	}
	return
}
