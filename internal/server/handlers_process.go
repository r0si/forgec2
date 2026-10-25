package server

import (
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func processTreeEnvelope(task db.Task) gin.H {
	kind := "process_tree"
	if task.Type == "ps" {
		kind = "last_ps_snapshot"
	}
	envelope := gin.H{
		"processes": task.Result,
		"source":    task.Type,
		"live":      false,
		"kind":      kind,
		"task_id":   task.ID,
	}
	// UpdatedAt is when the result landed (CreatedAt is when the operator
	// queued the task and can be minutes or hours earlier on a sleeping
	// beacon). Keep CreatedAt as a fallback for old/partial rows.
	collectedAt := task.UpdatedAt
	if collectedAt.IsZero() {
		collectedAt = task.CreatedAt
	}
	if !collectedAt.IsZero() {
		envelope["collected_at"] = collectedAt.UTC().Format(time.RFC3339)
	}
	return envelope
}

// handleGetProcesses queues a process_tree task (falls back to ps if needed).
func (s *Server) handleGetProcesses(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "process_tree"})
	if task == nil {
		return
	}

	respond(c, gin.H{

		"task_id": task.ID,
		"source":  "process_tree",
		"live":    false,
		"message": "Queued process_tree. Wait for the next beacon; this is not a live stream.",
	})
}

// handleCollectProcesses queues a process_tree task and records the operator
// action. Used by POST /api/agents/:id/processes; the legacy GET queue route
// stays read-permission compatible for older clients.
func (s *Server) handleCollectProcesses(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "process_tree"})
	if task == nil {
		return
	}
	s.dispatchTask(c, task, "request_process_tree", "process tree")
}

// handleGetProcessTree returns the last completed process_tree snapshot,
// falling back to a completed ps listing when no tree has been collected yet.
func (s *Server) handleGetProcessTree(c *gin.Context) {
	agentID := c.Param("id")
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}

	// Scan into the model, not a bare struct: Task.AfterFind is what decrypts
	// Result/Error, and it only runs when the destination is a db.Task. A
	// struct scan handed this handler raw FC2ENC: ciphertext, which then failed
	// the envelope parse and answered "no completed process tree" for every
	// agent whose output was encrypted at rest.
	var tasks []db.Task
	err := s.db.Table("tasks").
		Select("id, type, result, created_at, updated_at").
		Where("agent_id = ? AND type IN ? AND status = 'completed' AND result <> ''", agentID, []string{"process_tree", "ps"}).
		Order("CASE WHEN type = 'process_tree' THEN 0 ELSE 1 END, created_at desc").
		Limit(1).
		Find(&tasks).Error

	if err != nil || len(tasks) == 0 || tasks[0].Result == "" {
		respondError(c, http.StatusNotFound, "No completed process tree. Queue process_tree first.")
		return
	}

	respond(c, processTreeEnvelope(tasks[0]))
}
