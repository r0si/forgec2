package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Bulk rerun of failed tasks.
//
// handleRerunTask covers one task at a time. The situation that needs the bulk form is the one that
// produces the failures: a beacon goes offline (or the listener dies for an hour) and everything
// queued against it fails at once, so recovering means re-running dozens of tasks individually.
//
// The endpoint is deliberately conservative: it refuses to fan out unbounded, it never retries a task
// whose type is control/monitoring (the same list the single rerun uses), and it reports what it
// skipped and why rather than silently returning a smaller number than the caller expected.

const (
	bulkRerunDefaultLimit = 10
	bulkRerunMaxLimit     = 25
)

// noRerunTaskTypes are task types a rerun must not clone: they control a live session rather than do
// work, so replaying them would fight whatever the operator is doing now.
var noRerunTaskTypes = map[string]bool{
	"kill_agent": true, "screen_stream_start": true, "screen_stream_stop": true,
}

// handleRerunFailedTasks re-queues the failed tasks of one agent, newest first.
//
// POST /api/agents/:id/tasks/rerun-failed?since=24h&limit=10[&type=shell]
func (s *Server) handleRerunFailedTasks(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}

	window, ok := parseStatsWindow(c.DefaultQuery("since", "24h"))
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid since (expected e.g. 1h, 24h, 7d, 30d)")
		return
	}
	limit := bulkRerunDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			respondError(c, http.StatusBadRequest, "invalid limit")
			return
		}
		if parsed > bulkRerunMaxLimit {
			parsed = bulkRerunMaxLimit
		}
		limit = parsed
	}

	query := s.tenantScope(s.db.Model(&db.Task{}), c).
		Where("agent_id = ?", agentID).
		Where("status = ?", "failed").
		Where("created_at >= ?", time.Now().Add(-window))
	if filterType := c.Query("type"); filterType != "" {
		query = query.Where("type = ?", filterType)
	}
	// Exclude unrerunnable types in the query, not after the limit: otherwise a burst of control-type
	// failures fills the candidate window and the eligible tasks are never reached.
	query = query.Where("type NOT IN ?", unrerunnableTaskTypes())

	var originals []db.Task
	// One extra row tells us whether the answer was cut short, which the response reports.
	if err := query.Order("created_at desc").Limit(limit + 1).Find(&originals).Error; err != nil {
		handleQueryError(c, err, "Failed to load failed tasks for rerun")
		return
	}
	truncated := len(originals) > limit
	if truncated {
		originals = originals[:limit]
	}

	type skip struct {
		ID     uint   `json:"id"`
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	var queued []uint
	var skipped []skip
	start := time.Now()

	for i := range originals {
		original := &originals[i]
		if noRerunTaskTypes[original.Type] {
			skipped = append(skipped, skip{ID: original.ID, Type: original.Type, Reason: "task type cannot be rerun"})
			continue
		}
		newTask, rerunErr := s.rerunTaskAsNew(c, agentID, original)
		if rerunErr != nil {
			skipped = append(skipped, skip{ID: original.ID, Type: original.Type, Reason: sanitizeError(rerunErr, "rerun")})
			continue
		}
		// A row created before this request is the previous rerun being returned by its idempotency
		// key, not new work: report it as already queued instead of counting it twice.
		if newTask.CreatedAt.Before(start) {
			skipped = append(skipped, skip{ID: original.ID, Type: original.Type, Reason: "already queued"})
			continue
		}
		queued = append(queued, newTask.ID)
	}

	details := fmt.Sprintf("bulk rerun of %d failed task(s) [ids=%s]", len(queued), joinUintIDs(queued))
	s.LogAuditRecord(c, "task_bulk_rerun", "agent", agentID, details, true, nil)

	// Report the control-type failures that were excluded rather than leaving the caller to wonder why
	// fewer tasks were rerun than failed.
	excluded := int64(0)
	if c.Query("type") == "" {
		_ = s.tenantScope(s.db.Model(&db.Task{}), c).
			Where("agent_id = ?", agentID).
			Where("status = ?", "failed").
			Where("created_at >= ?", time.Now().Add(-window)).
			Where("type IN ?", unrerunnableTaskTypes()).
			Count(&excluded).Error
	}

	resp := gin.H{
		"queued":    len(queued),
		"task_ids":  queued,
		"skipped":   skipped,
		"excluded":  excluded,
		"truncated": truncated,
		"since":     c.DefaultQuery("since", "24h"),
	}
	if len(skipped) == 0 {
		resp["skipped"] = []skip{}
	}
	respond(c, resp)
}

// rerunTaskAsNew clones one task, returning the new row or the reason it could not be cloned.
//
// Shared by the single-task rerun and the bulk one so the two cannot drift on the no-rerun list, the
// idempotency carry-over or the caller plumbing.
func (s *Server) rerunTaskAsNew(c *gin.Context, agentID string, original *db.Task) (*db.Task, error) {
	opts := callerOpts(c)
	// Carrying the original key over means rerunning a live keyed task returns the live row instead
	// of stacking a duplicate. An UNKEYED original gets a deterministic key derived from its id, so
	// the same protection covers reruns: without it, a double-clicked "rerun failures" button queued
	// every failure twice (the originals stay failed either way).
	key := strings.TrimSpace(original.IdempotencyKey)
	if key == "" {
		key = fmt.Sprintf("rerun:%d", original.ID)
	}
	opts = append(opts, WithIdempotencyKey(key))
	newTask, err := s.createTask(agentID, original.Type, original.Command, original.Shell, original.Path, original.Data, original.Offset, original.Size, opts...)
	if err != nil {
		// Return the error itself, not a rendered string: the single-task rerun maps it through
		// respondTaskError so a rate-limited rerun still answers 429. Collapsing it to text here is how
		// that mapping was lost once already.
		return nil, err
	}
	return newTask, nil
}

// joinUintIDs renders ids for an audit detail without depending on a slice formatter.
func joinUintIDs(ids []uint) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	return strings.Join(parts, ",")
}

// unrerunnableTaskTypes lists the control types a rerun must not clone, for use in a query.
func unrerunnableTaskTypes() []string {
	types := make([]string, 0, len(noRerunTaskTypes))
	for taskType := range noRerunTaskTypes {
		types = append(types, taskType)
	}
	return types
}
