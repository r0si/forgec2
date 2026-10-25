package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/opsec"
	"github.com/gin-gonic/gin"
)

// handleOpsecCheck performs OPSEC validation before executing a task.
// POST /api/opsec/check
// Body: { agent_id, task_type, username, hostname, ip, domain, is_da, processes }
func (s *Server) handleOpsecCheck(c *gin.Context) {
	var req struct {
		AgentID   string   `json:"agent_id"`
		TaskType  string   `json:"task_type"`
		Username  string   `json:"username"`
		Hostname  string   `json:"hostname"`
		IP        string   `json:"ip"`
		Domain    string   `json:"domain"`
		IsDA      bool     `json:"is_da"`
		Processes []string `json:"processes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "OPSEC operation"))
		return
	}

	procs := make([]opsec.ProcessInfo, len(req.Processes))
	for i, p := range req.Processes {
		procs[i] = opsec.ProcessInfo{Name: p}
	}

	ctx := &opsec.OpsecContext{
		AgentID:   req.AgentID,
		Username:  req.Username,
		Hostname:  req.Hostname,
		IP:        req.IP,
		Domain:    req.Domain,
		TaskType:  req.TaskType,
		IsDA:      req.IsDA,
		Processes: procs,
	}

	results := opsec.CheckTask(ctx)

	blocked := false
	var messages []string
	for _, r := range results {
		if !r.Allowed {
			blocked = true
		}
		messages = append(messages, r.Message)

		// Persist evaluation history
		if err := s.db.Create(&db.OpsecHistory{
			AgentID:   req.AgentID,
			TaskType:  req.TaskType,
			RuleName:  r.RuleName,
			Allowed:   r.Allowed,
			Message:   r.Message,
			RiskLevel: int(r.RiskLevel),
			Username:  req.Username,
			Hostname:  req.Hostname,
		}).Error; err != nil {
			slog.Error("Failed to record opsec history", "err", err)
		}
	}

	respond(c, gin.H{
		"allowed":  !blocked,
		"blocked":  blocked,
		"results":  results,
		"messages": strings.Join(messages, "; "),
	})
}

// handleCircuitBreakerStatus returns health status for all registered listeners.
// GET /api/circuit-breaker/status
func (s *Server) handleCircuitBreakerStatus(c *gin.Context) {
	cb := GetCircuitBreaker()
	if cb == nil {
		respond(c, gin.H{"status": "disabled"})
		return
	}
	statuses := cb.GetAllStatus()

	type listenerStatus struct {
		ID     string `json:"id"`
		Health string `json:"health"`
	}

	var result []listenerStatus
	for id, health := range statuses {
		label := "unknown"
		switch health {
		case 1:
			label = "healthy"
		case 2:
			label = "unstable"
		case 3:
			label = "burned"
		}
		result = append(result, listenerStatus{ID: id, Health: label})
	}

	respond(c, gin.H{"listeners": result})
}

// handleProfileRotate sends a profile rotation command to an agent.
// POST /api/agents/:id/profile-rotate
func (s *Server) handleProfileRotate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	var req struct {
		BeaconURI    string `json:"beacon_uri"`
		BeaconMethod string `json:"beacon_method"`
		UserAgent    string `json:"user_agent"`
		Encoding     string `json:"encoding"`
		C2URL        string `json:"c2_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "OPSEC operation"))
		return
	}

	// Create a task that will be picked up by the agent on next beacon
	taskData, ok := marshalJSONSafe(req)
	if !ok {
		respondError(c, http.StatusInternalServerError, "failed to marshal request")
		return
	}

	// Route through issueAgentTask: the bare Create used to skip the tenant
	// scope, the agent-existence check and the collaboration-lock gate, and
	// it stamped CreatedBy with the literal "operator" so the audit chain
	// could not answer WHO rotated the profile.
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "profile_rotate", Data: string(taskData)})
	if task == nil {
		return
	}

	// dispatchTask stamps the calling operator as creator and audits the
	// rotation (the old handler recorded no audit entry at all).
	s.dispatchTask(c, task, "profile_rotate", "Profile rotation queued for next beacon")
}

// rotateAgentsOnBurnedListener rotates all agents connected to a burned listener.
func (s *Server) rotateAgentsOnBurnedListener(listenerID string) {
	var agents []db.Implant
	if err := s.db.Where("listener_id = ?", listenerID).Find(&agents).Error; err != nil {
		slog.Error("Failed to query agents for listener rotation", "listener_id", listenerID, "err", err)
		return
	}

	// Find a fallback listener
	var fallbackListeners []db.Listener
	if err := s.db.Where("enabled = ? AND id != ?", true, listenerID).Find(&fallbackListeners).Error; err != nil {
		slog.Error("Failed to find fallback listeners", "err", err)
		return
	}
	if len(fallbackListeners) == 0 {
		slog.Warn("No fallback listener available for agent rotation", "listener_id", listenerID)
		return
	}

	fallback := fallbackListeners[0]
	fallbackURL := fmt.Sprintf("%s://%s:%d", fallback.Scheme, fallback.Host, fallback.Port)
	defaultUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

	for _, agent := range agents {
		rotation := ProfileRotationData{
			BeaconURI:    "/beacon",
			BeaconMethod: "POST",
			UserAgent:    defaultUA,
			Encoding:     "json",
			C2URL:        fallbackURL,
		}
		data, ok := marshalJSONSafe(rotation)
		if !ok {
			slog.Error("Failed to marshal rotation data", "agent_id", agent.ID)
			continue
		}

		// createSystemTaskWithData instead of a bare Create: the system path
		// must apply the same accounting as every other task — tenant
		// inheritance (otherwise tenant-scoped reads 404 on these rows), the
		// pending counter, the task_created plugin hook, and TasksTotal.
		if _, err := s.createSystemTaskWithData(agent.ID, "profile_rotate", "", "", string(data), "pending", "system"); err != nil {
			slog.Error("Failed to create rotation task for agent", "agent_id", agent.ID, "err", err)
		}
	}

	slog.Info("Rotation tasks created for agents on burned listener",
		"listener_id", listenerID,
		"agent_count", len(agents),
		"fallback", fallbackURL)
}

type ProfileRotationData struct {
	BeaconURI    string `json:"beacon_uri"`
	BeaconMethod string `json:"beacon_method"`
	UserAgent    string `json:"user_agent"`
	Encoding     string `json:"encoding"`
	C2URL        string `json:"c2_url"`
}
