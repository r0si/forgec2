package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Re-running the failures that share one shape.
//
// After fixing a link, the useful action is "re-run the timeouts" - not "re-run every failure on this host", which would
// re-dispatch permission errors that will fail again, and not forty manual clicks. The selector is the same signature the
// failure report and the handover bundle use, so "the row I am looking at" and "the rows this will re-run" are the same set by
// construction.
//
// POST /api/tasks/rerun-by-signature   {"signature":"context deadline exceeded","since":"24h","limit":50}

const (
	rerunBySignatureSampleLimit = 500
	rerunBySignatureMaxDispatch = 100
)

// handleRerunBySignature re-dispatches the failed tasks whose normalised error matches one signature.
func (s *Server) handleRerunBySignature(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var body struct {
		Signature string `json:"signature"`
		Since     string `json:"since"`
		Limit     int    `json:"limit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest,
			`invalid body: expected {"signature":"...","since":"24h","limit":50}`)
		return
	}
	signature := strings.TrimSpace(body.Signature)
	if signature == "" {
		// An empty signature would match every failed task that produced no error text, which is not what a caller means.
		respondError(c, http.StatusBadRequest, "signature is required; it is the row from GET /api/stats/failures/signatures")
		return
	}
	since := strings.TrimSpace(body.Since)
	if since == "" {
		since = "24h"
	}
	window, ok := parseStatsWindow(since)
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid since (expected e.g. 24h or 7d)")
		return
	}
	limit := body.Limit
	if limit <= 0 || limit > rerunBySignatureMaxDispatch {
		limit = rerunBySignatureMaxDispatch
	}

	// Fetch bounded candidates and filter by signature in Go: the normalisation is not expressible in SQL, and a bounded
	// candidate set keeps the request predictable.
	var candidates []db.Task
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Task{}), c).
		Where("status = ?", "failed").
		Where("created_at >= ?", time.Now().Add(-window)).
		Where("type NOT IN ?", unrerunnableTaskTypes()).
		Order("created_at DESC").
		Limit(rerunBySignatureSampleLimit).
		Find(&candidates).Error; err != nil {
		handleQueryError(c, err, "Failed to load failed tasks")
		return
	}

	var queued []uint
	skipped := make([]gin.H, 0, 4)
	matched := 0
	start := time.Now()
	for i := range candidates {
		task := &candidates[i]
		if normaliseFailureText(task.Error) != normaliseFailureText(signature) {
			continue
		}
		matched++
		if len(queued) >= limit {
			skipped = append(skipped, gin.H{"id": task.ID, "type": task.Type, "reason": "limit reached"})
			continue
		}
		newTask, err := s.rerunTaskAsNew(c, task.AgentID, task)
		if err != nil {
			skipped = append(skipped, gin.H{"id": task.ID, "type": task.Type, "reason": sanitizeError(err, "rerun")})
			_ = err
			continue
		}
		// A row created before this request is the idempotency key returning the previous rerun, not new work.
		if newTask.CreatedAt.Before(start) {
			skipped = append(skipped, gin.H{"id": task.ID, "type": task.Type, "reason": "already queued"})
			continue
		}
		queued = append(queued, newTask.ID)
	}

	if len(queued) == 0 && matched == 0 {
		// Not an error, but silence would read as one: say that the shape was not found in the window.
		respond(c, gin.H{"matched": 0, "queued": 0, "skipped": skipped,
			"note": "no failed task in the window has that shape; the signature must match a row from the signature report"})
		return
	}

	s.LogAuditRecord(c, "tasks_rerun_by_signature", "task", "",
		fmt.Sprintf("re-ran %d task(s) matching failure shape %q (matched %d)", len(queued), signature, matched), true, nil)

	respond(c, gin.H{
		"signature": signature,
		"since":     since,
		"matched":   matched,
		"queued":    len(queued),
		"task_ids":  queued,
		"skipped":   skipped,
	})
}
