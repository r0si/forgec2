package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// errAIDisabled is returned by every assist endpoint when the AI subsystem
// is off or has no key. Frontends probe /api/ai/status to hide entries.
var errAIDisabled = errors.New("ai not configured")

const aiOneShotTimeout = 45 * time.Second

// aiSuggestionMaxItems caps the ranked next-step list handed to the UI.
const aiSuggestionMaxItems = 6

// aiPlaybookMaxSteps caps a drafted playbook; the macro runner takes the rest.
const aiPlaybookMaxSteps = 12

// aiAssistReady reports whether one-shot AI helpers may run.
func (s *Server) aiAssistReady() bool {
	if s.cfg == nil {
		return false
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg.AI.Enabled && s.cfg.AI.APIKey != ""
}

// aiOneShot performs a single non-streaming completion (system + user) and
// returns the assistant text. It reuses aiDoRequest so SSRF protection,
// endpoint resolution, claude conversion and retry/backoff all apply.
func (s *Server) aiOneShot(parent context.Context, system, user string, maxTokens int) (string, error) {
	providerConfig, err := s.aiProviderRequestConfigSnapshot()
	if err != nil {
		return "", err
	}
	if !providerConfig.enabled || strings.TrimSpace(providerConfig.apiKey) == "" {
		return "", errAIDisabled
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	model := providerConfig.model
	if model == "" {
		model = aiDefaultModel(providerConfig.provider)
	}
	provider := providerConfig.provider
	providerConfig.model = model

	body := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream:    false,
		MaxTokens: maxTokens,
	}

	payload, ok := marshalJSONSafe(body)
	if !ok {
		return "", fmt.Errorf("marshal request failed")
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, aiOneShotTimeout)
	defer cancel()

	resp, err := s.aiDoRequestWithConfig(ctx, payload, providerConfig)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	// Response shape differs by provider (snapshot taken under RLock above).
	if config.AIProviderUsesClaudeWire(provider) {
		var cr struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &cr); err != nil {
			return "", fmt.Errorf("parse response: %w", err)
		}
		if cr.Error != nil {
			return "", fmt.Errorf("provider error: %s", truncateStr(cr.Error.Message, 200))
		}
		for _, blk := range cr.Content {
			if blk.Type == "text" && blk.Text != "" {
				return blk.Text, nil
			}
		}
		return "", fmt.Errorf("empty completion")
	}

	var or struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &or); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if or.Error != nil {
		return "", fmt.Errorf("provider error: %s", truncateStr(or.Error.Message, 200))
	}
	if len(or.Choices) == 0 {
		return "", fmt.Errorf("empty completion")
	}
	return or.Choices[0].Message.Content, nil
}

// extractJSONBlock pulls a JSON object out of an LLM reply: it prefers a
// fenced ```json block, then the first {...}/[...] span. Returns "" if none.
func extractJSONBlock(text string) string {
	t := strings.TrimSpace(text)
	if idx := strings.Index(t, "```json"); idx >= 0 {
		rest := t[idx+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	if idx := strings.Index(t, "```"); idx >= 0 {
		rest := strings.TrimPrefix(t[idx+3:], "json")
		if endFence := strings.Index(rest, "```"); endFence >= 0 {
			return strings.TrimSpace(rest[:endFence])
		}
	}
	// Bare span scan.
	for _, openClose := range [][2]string{{"{", "}"}, {"[", "]"}} {
		start := strings.Index(t, openClose[0])
		if start < 0 {
			continue
		}
		end := strings.LastIndex(t, openClose[1])
		if end > start {
			return strings.TrimSpace(t[start : end+1])
		}
	}
	return ""
}

// decodeModelJSON unmarshals model output into v, tolerating fences/prose.
func decodeModelJSON(text string, v interface{}) error {
	block := extractJSONBlock(text)
	if block == "" {
		block = text
	}
	if err := json.Unmarshal([]byte(block), v); err != nil {
		// Last resort: strip control chars that sometimes leak into strings.
		cleaned := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' || r == '\r' {
				return -1
			}
			return r
		}, block)
		return json.Unmarshal([]byte(cleaned), v)
	}
	return nil
}

func aiAssistUnavailable(c *gin.Context) {
	respondError(c, http.StatusServiceUnavailable, "AI not configured. Enable it in AI settings first.")
}

// handleAIStatus exposes the AI toggle state so pages can hide assist UI
// entirely when the subsystem is off (graceful degradation contract).
func (s *Server) handleAIStatus(c *gin.Context) {
	type aiStatus struct {
		Enabled      bool `json:"enabled"`
		HasAPIKey    bool `json:"has_api_key"`
		AllowExecute bool `json:"allow_execute"`
	}
	st := aiStatus{}
	if s.cfg != nil {
		s.configMu.RLock()
		st.Enabled = s.cfg.AI.Enabled
		// The key may live in config or arrive via FORGEC2_AI_API_KEY env.
		st.HasAPIKey = s.cfg.AI.APIKey != "" || os.Getenv("FORGEC2_AI_API_KEY") != ""
		st.AllowExecute = s.cfg.AI.AllowExecute
		s.configMu.RUnlock()
	}
	respond(c, st)
}

// handleAIAnalyzeResult summarizes one task's raw output into structured,
// scannable findings. Read-only; never executes anything.
func (s *Server) handleAIAnalyzeResult(c *gin.Context) {
	if !s.aiAssistReady() {
		aiAssistUnavailable(c)
		return
	}
	var req struct {
		TaskID uint `json:"task_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.TaskID == 0 {
		respondError(c, http.StatusBadRequest, "task_id required")
		return
	}
	var task db.Task
	if err := s.db.First(&task, req.TaskID).Error; err != nil {
		respondError(c, http.StatusNotFound, "task not found")
		return
	}
	// Tenant gate: the task ID is caller-supplied and the result feeds an LLM
	// prompt — without this one tenant analyzes another's raw output.
	if s.resolveVisibleAgentID(c, task.AgentID) == "" {
		respondError(c, http.StatusNotFound, "task not found")
		return
	}
	if strings.TrimSpace(task.Result) == "" && strings.TrimSpace(task.Error) == "" {
		respondError(c, http.StatusBadRequest, "task has no result to analyze yet")
		return
	}

	const maxExcerpt = 12000
	excerpt := redactSensitiveResultForAuxiliary(task.Type, task.Result)
	if len(excerpt) > maxExcerpt {
		excerpt = excerpt[:maxExcerpt] + "\n...[truncated]"
	}
	if task.Error != "" {
		excerpt += "\n\nERROR: " + truncateStr(task.Error, 1000)
	}

	system := `You are a red-team operations analyst inside a C2 console. Analyze the given command output and reply with ONLY a JSON object, no prose, in this exact schema:
{"summary":"2-3 sentence plain summary","highlights":[{"kind":"priv|av|network|path|cred|other","severity":"info|low|medium|high","text":"short finding"}],"next_steps":["imperative suggestion"]}
Highlight rules: privileged processes/SYSTEM/admin groups -> kind=priv; antivirus/EDR (Defender, 360, Huorong, CrowdStrike, SentinelOne...) -> kind=av severity=high; unusual outbound connections/RDP/tunnels -> kind=network; sensitive paths (passwords, keys, DB dumps) -> kind=path; credentials/tokens/hashes -> kind=cred severity=high. Max 8 highlights, max 4 next_steps. Reply in the language of the output data.`

	text, err := s.aiOneShot(c.Request.Context(), system, "Task type: "+task.Type+"\nCommand: "+truncateStr(task.Command, 500)+"\n\nOutput:\n"+excerpt, 1500)
	if err != nil {
		if errors.Is(err, errAIDisabled) {
			aiAssistUnavailable(c)
			return
		}
		slog.Warn("AI analyze-result failed", "err", err)
		respondError(c, http.StatusBadGateway, sanitizeError(err, "AI analysis"))
		return
	}

	// Structured path; degrade gracefully to raw summary when the model
	// refuses to emit clean JSON.
	var parsed struct {
		Summary    string `json:"summary"`
		Highlights []struct {
			Kind     string `json:"kind"`
			Severity string `json:"severity"`
			Text     string `json:"text"`
		} `json:"highlights"`
		NextSteps []string `json:"next_steps"`
	}
	if err := decodeModelJSON(text, &parsed); err != nil || parsed.Summary == "" {
		parsed.Summary = strings.TrimSpace(text)
		parsed.Highlights = nil
		parsed.NextSteps = nil
	}
	out := gin.H{"summary": truncateStr(parsed.Summary, 4000)}
	if len(parsed.Highlights) > 8 {
		parsed.Highlights = parsed.Highlights[:8]
	}
	hs := make([]gin.H, 0, len(parsed.Highlights))
	for _, h := range parsed.Highlights {
		hs = append(hs, gin.H{"kind": h.Kind, "severity": h.Severity, "text": truncateStr(h.Text, 300)})
	}
	out["highlights"] = hs
	ns := make([]string, 0, len(parsed.NextSteps))
	for i, n := range parsed.NextSteps {
		if i >= 4 {
			break
		}
		ns = append(ns, truncateStr(n, 300))
	}
	out["next_steps"] = ns
	respond(c, gin.H{"analysis": out})
}

// buildAttackSurfaceData gathers everything suggest-next-steps needs about one
// agent. Extracted from the get_attack_surface tool so both share one source.
func (s *Server) buildAttackSurfaceData(agent db.Implant) (map[string]interface{}, error) {
	var credCount int64
	if err := s.db.Model(&db.CredentialEntry{}).Where("agent_id = ?", agent.ID).Count(&credCount).Error; err != nil {
		return nil, fmt.Errorf("count nearby credentials: %w", err)
	}
	var recentTasks []db.Task
	if err := s.db.Where("agent_id = ?", agent.ID).Order("created_at desc").Limit(5).Find(&recentTasks).Error; err != nil {
		return nil, fmt.Errorf("load recent tasks: %w", err)
	}
	var recentLateral []db.Task
	if err := s.db.Where("agent_id = ? AND type IN ?", agent.ID, []string{"lateral", "ssh_lateral", "token_steal", "token_make"}).Order("created_at desc").Limit(5).Find(&recentLateral).Error; err != nil {
		return nil, fmt.Errorf("load recent lateral tasks: %w", err)
	}
	return map[string]interface{}{
		"agent": map[string]interface{}{
			"id": agent.ID, "hostname": agent.Hostname, "ip": agent.IP,
			"os": agent.OS, "arch": agent.Arch, "username": agent.Username,
			"domain": agent.Domain, "integrity": agent.Integrity,
			"elevated": agent.Elevated, "pid": agent.PID,
			"status": agent.Status, "version": agent.Version,
		},
		"credentials_nearby": credCount,
		"recent_tasks":       recentTasks,
		"recent_lateral":     recentLateral,
	}, nil
}

// handleAISuggestNextSteps ranks concrete follow-up actions for one agent.
// Purely advisory: execution still goes through normal approval semantics.
func (s *Server) handleAISuggestNextSteps(c *gin.Context) {
	if !s.aiAssistReady() {
		aiAssistUnavailable(c)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.AgentID) == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}
	aid := s.resolveVisibleAgentID(c, strings.TrimSpace(req.AgentID))
	if aid == "" {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	var agent db.Implant
	if err := s.db.Where("id = ?", aid).First(&agent).Error; err != nil {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}

	surface, err := s.buildAttackSurfaceData(agent)
	if err != nil {
		slog.Error("AI suggestion context query failed", "agent_id", aid, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to build agent context")
		return
	}
	covTac, gapTac, _, err := s.mitreCoverageCounts()
	if err != nil {
		slog.Error("AI suggestion MITRE context query failed", "agent_id", aid, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to build MITRE context")
		return
	}
	surface["mitre"] = map[string]int{"covered_tactics": covTac, "gap_tactics": gapTac}

	surfaceJSON, ok := marshalJSONSafe(surface)
	if !ok {
		respondError(c, http.StatusInternalServerError, "failed to encode agent context")
		return
	}

	system := `You are a senior red-team operator advising on next steps for one compromised host. Given the JSON context, reply with ONLY a JSON array (max 6 items), each: {"action":"short imperative title","reason":"why now, grounded in the data","risk":"low|medium|high","command_hint":"concrete command or empty"}.
Order by operational value. Prefer OPSEC-quiet actions. If host is elevated with creds nearby, prioritize credential access/lateral movement; if not elevated, privilege escalation; if stale/offline, re-acquisition. Never include destructive commands. Reply in Chinese when hostnames/domains look Chinese-domain-joined, else English.`

	text, err := s.aiOneShot(c.Request.Context(), system, string(surfaceJSON), 1200)
	if err != nil {
		if errors.Is(err, errAIDisabled) {
			aiAssistUnavailable(c)
			return
		}
		slog.Warn("AI suggest-next-steps failed", "err", err)
		respondError(c, http.StatusBadGateway, sanitizeError(err, "AI suggestions"))
		return
	}

	var suggestions []struct {
		Action      string `json:"action"`
		Reason      string `json:"reason"`
		Risk        string `json:"risk"`
		CommandHint string `json:"command_hint"`
	}
	validRisk := func(r string) string {
		switch r {
		case "low":
			return "low"
		case "high":
			return "high"
		default:
			return "medium"
		}
	}
	if err := decodeModelJSON(text, &suggestions); err != nil {
		respondError(c, http.StatusBadGateway, "model returned unparseable suggestions")
		return
	}
	// Filter before capping: a suggestion with no action is noise, and letting
	// it consume one of the six slots silently shortens the useful list.
	cleaned := make([]gin.H, 0, len(suggestions))
	for _, sg := range suggestions {
		if strings.TrimSpace(sg.Action) == "" {
			continue
		}
		cleaned = append(cleaned, gin.H{
			"action":       truncateStr(sg.Action, 120),
			"reason":       truncateStr(sg.Reason, 400),
			"risk":         validRisk(sg.Risk),
			"command_hint": truncateStr(sg.CommandHint, 300),
		})
		if len(cleaned) == aiSuggestionMaxItems {
			break
		}
	}
	respond(c, gin.H{"agent_id": aid, "hostname": agent.Hostname, "suggestions": cleaned})
}

var nlQueryStatuses = map[string]bool{
	"pending": true, TaskStatusPendingApproval: true, "running": true,
	"completed": true, "failed": true, "cancelled": true,
}

// handleAINLQuery converts a natural-language history question into a
// whitelist-validated filter, then runs a deterministic parameterized query.
// The LLM never writes SQL - it only picks fields, so hallucinations cannot
// reach the database layer.
func (s *Server) handleAINLQuery(c *gin.Context) {
	if !s.aiAssistReady() {
		aiAssistUnavailable(c)
		return
	}
	var req struct {
		Question string `json:"question"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Question) == "" {
		respondError(c, http.StatusBadRequest, "question required")
		return
	}

	system := `Convert the user's question about C2 task history into a search filter. Reply with ONLY JSON: {"keywords":["substring",...],"agent_hostname":"","task_type":"","status":"","since_days":30}.
Rules: keywords = distinctive words likely inside command/result text ([] for none); status only one of pending/pending_approval/running/completed/failed/cancelled else ""; task_type examples shell,screenshot,keylogger_dump,download,upload,lateral else ""; since_days integer 1-365 default 30. Empty string means no filter.`

	text, err := s.aiOneShot(c.Request.Context(), system, strings.TrimSpace(req.Question), 300)
	if err != nil {
		if errors.Is(err, errAIDisabled) {
			aiAssistUnavailable(c)
			return
		}
		slog.Warn("AI nl-query failed", "err", err)
		respondError(c, http.StatusBadGateway, sanitizeError(err, "AI query"))
		return
	}

	var filter struct {
		Keywords      []string `json:"keywords"`
		AgentHostname string   `json:"agent_hostname"`
		TaskType      string   `json:"task_type"`
		Status        string   `json:"status"`
		SinceDays     int      `json:"since_days"`
	}
	if err := decodeModelJSON(text, &filter); err != nil {
		respondError(c, http.StatusBadGateway, "could not interpret question as filter")
		return
	}
	if filter.SinceDays <= 0 || filter.SinceDays > 365 {
		filter.SinceDays = 30
	}
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if filter.Status != "" && !nlQueryStatuses[filter.Status] {
		filter.Status = ""
	}
	filter.TaskType = strings.ToLower(strings.TrimSpace(filter.TaskType))

	q := s.db.Model(&db.Task{}).Where("created_at >= ?", time.Now().AddDate(0, 0, -filter.SinceDays))
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.TaskType != "" {
		q = q.Where("LOWER(type) LIKE ? ESCAPE '\\'", "%"+strings.ToLower(escapeLike(filter.TaskType))+"%")
	}
	if hn := strings.TrimSpace(filter.AgentHostname); hn != "" {
		q = q.Where("agent_id IN (?)", s.db.Model(&db.Implant{}).
			Select("id").Where("LOWER(hostname) LIKE ? ESCAPE '\\'", "%"+strings.ToLower(escapeLike(hn))+"%"))
	}
	kwCount := 0
	for _, kw := range filter.Keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" || kwCount >= 3 {
			continue
		}
		like := "%" + escapeLike(kw) + "%"
		q = q.Where("(command LIKE ? ESCAPE '\\' OR result LIKE ? ESCAPE '\\')", like, like)
		kwCount++
	}
	var tasks []db.Task
	if err := q.Order("created_at desc").Limit(50).Find(&tasks).Error; err != nil {
		slog.Error("AI natural-language query failed", "err", err)
		respondError(c, http.StatusInternalServerError, "task query failed")
		return
	}

	rows := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, gin.H{
			"id": t.ID, "agent_id": t.AgentID, "type": t.Type,
			"command":    truncateStr(t.Command, 160),
			"status":     t.Status,
			"result":     truncateStr(redactSensitiveResultForAuxiliary(t.Type, t.Result), 200),
			"created_at": t.CreatedAt.Format(time.RFC3339),
		})
	}
	respond(c, gin.H{"filter": gin.H{
		"keywords": filter.Keywords, "agent_hostname": filter.AgentHostname,
		"task_type": filter.TaskType, "status": filter.Status,
		"since_days": filter.SinceDays,
	}, "tasks": rows})
}

// handleAIGeneratePlaybook drafts a macro from a natural-language goal,
// optionally personalized for one agent's profile. Draft only - nothing runs.
func (s *Server) handleAIGeneratePlaybook(c *gin.Context) {
	if !s.aiAssistReady() {
		aiAssistUnavailable(c)
		return
	}
	var req struct {
		Goal    string `json:"goal"`
		AgentID string `json:"agent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Goal) == "" {
		respondError(c, http.StatusBadRequest, "goal required")
		return
	}

	contextLine := ""
	if aid := s.resolveVisibleAgentID(c, strings.TrimSpace(req.AgentID)); aid != "" {
		var agent db.Implant
		if err := s.db.Where("id = ?", aid).First(&agent).Error; err == nil {
			contextLine = fmt.Sprintf("\nTarget profile: os=%s arch=%s user=%s domain=%s integrity=%s elevated=%t",
				agent.OS, agent.Arch, agent.Username, agent.Domain, agent.Integrity, agent.Elevated)
		}
	}

	system := `You are a red-team playbook designer for Windows/Linux implants managed by ForgeC2. Given a goal (and optional target profile), draft a step-by-step macro. Reply with ONLY JSON: {"name":"snake_case_name","description":"one line","steps":[{"command":"shell command","shell":"cmd.exe|powershell.exe|/bin/sh","rationale":"why this step"}]}.
Rules: 2-8 steps, each step self-contained and quiet (prefer built-in tools over downloads). Steps run sequentially on ONE agent. No destructive or persistence-without-consent steps. Use powershell.exe shell for PowerShell cmdlets, /bin/sh on non-Windows targets.`

	text, err := s.aiOneShot(c.Request.Context(), system, "Goal: "+strings.TrimSpace(req.Goal)+contextLine, 1400)
	if err != nil {
		if errors.Is(err, errAIDisabled) {
			aiAssistUnavailable(c)
			return
		}
		slog.Warn("AI generate-playbook failed", "err", err)
		respondError(c, http.StatusBadGateway, sanitizeError(err, "AI playbook"))
		return
	}

	var pb struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Steps       []struct {
			Command   string `json:"command"`
			Shell     string `json:"shell"`
			Rationale string `json:"rationale"`
		} `json:"steps"`
	}
	if err := decodeModelJSON(text, &pb); err != nil || len(pb.Steps) == 0 {
		respondError(c, http.StatusBadGateway, "model returned unparseable playbook")
		return
	}
	if pb.Name == "" {
		pb.Name = "ai_playbook_" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	pb.Name = strings.Trim(pb.Name, "_ ")
	// Filter before capping, for the same reason as the suggestion list: a
	// step with no command is noise, and letting it consume one of the twelve
	// slots silently shortens a usable playbook.
	steps := make([]gin.H, 0, len(pb.Steps))
	for _, st := range pb.Steps {
		cmd := strings.TrimSpace(st.Command)
		if cmd == "" {
			continue
		}
		shell := st.Shell
		if shell == "" {
			shell = "cmd.exe"
		}
		steps = append(steps, gin.H{
			"command":   truncateStr(cmd, 800),
			"shell":     shell,
			"rationale": truncateStr(st.Rationale, 300),
		})
		if len(steps) == aiPlaybookMaxSteps {
			break
		}
	}
	if len(steps) == 0 {
		respondError(c, http.StatusBadGateway, "playbook had no usable steps")
		return
	}
	respond(c, gin.H{
		"name":        truncateStr(pb.Name, 80),
		"description": truncateStr(pb.Description, 300),
		"steps":       steps,
	})
}

// handleAISavePlaybook persists a drafted playbook as a CommandMacro owned by
// "ai". Running it later uses the ordinary macro runner + approval semantics.
func (s *Server) handleAISavePlaybook(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Steps       []struct {
			Command string `json:"command"`
			Shell   string `json:"shell"`
		} `json:"steps"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "name required")
		return
	}
	if len(req.Steps) == 0 {
		respondError(c, http.StatusBadRequest, "at least one step required")
		return
	}
	if len(req.Steps) > 12 {
		req.Steps = req.Steps[:12]
	}
	type macroStep struct {
		Command string `json:"command"`
		Shell   string `json:"shell"`
	}
	steps := make([]macroStep, 0, len(req.Steps))
	for _, st := range req.Steps {
		cmd := strings.TrimSpace(st.Command)
		if cmd == "" {
			continue
		}
		shell := st.Shell
		if shell == "" {
			shell = "cmd.exe"
		}
		steps = append(steps, macroStep{Command: truncateStr(cmd, 800), Shell: shell})
	}
	if len(steps) == 0 {
		respondError(c, http.StatusBadRequest, "no usable steps")
		return
	}
	stepsJSON, ok := marshalJSONSafe(steps)
	if !ok {
		respondError(c, http.StatusInternalServerError, "failed to encode playbook steps")
		return
	}

	// Unique-name safety: append a numeric suffix on collision instead of failing.
	name := req.Name
	var count int64
	if err := s.db.Model(&db.CommandMacro{}).Where("name = ?", name).Count(&count).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to check playbook name")
		return
	}
	if count > 0 {
		name = fmt.Sprintf("%s_%d", name, time.Now().Unix()%100000)
	}

	macro := db.CommandMacro{
		Name:        truncateStr(name, 100),
		Description: truncateStr(req.Description, 300),
		Steps:       string(stepsJSON),
		CreatedBy:   "ai",
	}
	if err := s.db.Create(&macro).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Playbook save"))
		return
	}
	respond(c, gin.H{"id": macro.ID, "name": macro.Name, "steps": len(steps)})
}

// handleAIRunReview produces a post-run debrief for a completed AI run:
// what the assistant did, what worked, gaps, and concrete next steps.
// It reads the durable run event stream (no re-execution) and runs one
// non-streaming completion over a capped transcript. Accepts either run_id
// or session_id (latest completed/failed run in that session).
func (s *Server) handleAIRunReview(c *gin.Context) {
	if !s.aiAssistReady() {
		aiAssistUnavailable(c)
		return
	}
	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	var req struct {
		RunID     string `json:"run_id"`
		SessionID uint   `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.RunID == "" && req.SessionID == 0) {
		respondError(c, http.StatusBadRequest, "run_id or session_id required")
		return
	}
	runQuery := s.db.Where("owner_id = ? AND owner = ?", principal.UserID, principal.Username)
	if principal.TenantID != 0 {
		runQuery = runQuery.Where("tenant_id = ?", principal.TenantID)
	}
	var run db.AIChatRun
	if req.RunID != "" {
		if err := runQuery.Where("id = ?", req.RunID).First(&run).Error; err != nil {
			respondError(c, http.StatusNotFound, "run not found")
			return
		}
	} else {
		if err := runQuery.Where("session_id = ? AND status IN ?", req.SessionID,
			[]string{aiRunStatusCompleted, aiRunStatusFailed}).
			Order("completed_at DESC").First(&run).Error; err != nil {
			respondError(c, http.StatusNotFound, "no completed run in session")
			return
		}
	}
	if run.Status != aiRunStatusCompleted && run.Status != aiRunStatusFailed {
		respondError(c, http.StatusBadRequest, "run is not finished yet")
		return
	}

	var events []db.AIChatRunEvent
	if err := s.db.Where("run_id = ?", run.ID).Order("sequence").Limit(60).Find(&events).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}
	if len(events) == 0 {
		respondError(c, http.StatusBadRequest, "run has no events to review")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Run status: %s\n", run.Status)
	for _, ev := range events {
		payload := truncateStr(ev.Payload, 1500)
		switch ev.Type {
		case "text", "reasoning", "tool_call", "tool_result", "tool_error":
			fmt.Fprintf(&sb, "[%s] %s\n", ev.Type, payload)
		}
		if sb.Len() > 14000 {
			break
		}
	}
	transcript := sb.String()
	if len(transcript) > 15000 {
		transcript = transcript[:15000] + "\n...[truncated]"
	}

	system := `You are a red-team operations analyst inside a C2 console. Review the finished AI assistant run below and reply with ONLY a JSON object, no prose, in this exact schema:
{"summary":"2-3 sentence plain summary of what was done","what_worked":["concrete completed item"],"gaps":["what failed or is still unknown"],"next_steps":["imperative follow-up suggestion"]}
Max 5 items per list. Reply in the language of the transcript.`
	text, err := s.aiOneShot(c.Request.Context(), system, "Original goal: "+truncateStr(run.Input, 500)+"\n\nTranscript:\n"+transcript, 1500)
	if err != nil {
		if errors.Is(err, errAIDisabled) {
			aiAssistUnavailable(c)
			return
		}
		slog.Warn("AI run-review failed", "err", err)
		respondError(c, http.StatusBadGateway, sanitizeError(err, "AI review"))
		return
	}
	var parsed struct {
		Summary    string   `json:"summary"`
		WhatWorked []string `json:"what_worked"`
		Gaps       []string `json:"gaps"`
		NextSteps  []string `json:"next_steps"`
	}
	if err := decodeModelJSON(text, &parsed); err != nil || parsed.Summary == "" {
		parsed.Summary = strings.TrimSpace(text)
	}
	cap5 := func(in []string) []string {
		out := make([]string, 0, len(in))
		for i, v := range in {
			if i >= 5 {
				break
			}
			out = append(out, truncateStr(v, 300))
		}
		return out
	}
	respond(c, gin.H{"review": gin.H{
		"run_id":      run.ID,
		"run_status":  run.Status,
		"summary":     truncateStr(parsed.Summary, 4000),
		"what_worked": cap5(parsed.WhatWorked),
		"gaps":        cap5(parsed.Gaps),
		"next_steps":  cap5(parsed.NextSteps),
	}})
}
