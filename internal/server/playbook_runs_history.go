package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// The history of one playbook run.
//
// The transitions are already audited (`playbook_run_create`, `playbook_run_transition`), so this endpoint adds no new record - it reads
// the record that exists and puts it where the question is asked. "Who advanced this run, and when" is the question a handover or an
// incident review asks about a checklist, and answering it by searching the global audit page is how the two get separated.

// playbookHistoryLimit bounds the history: a run has a handful of transitions, and an unbounded list would be an archive.
const playbookHistoryLimit = 200

// handlePlaybookRunHistory renders the audited transitions of one run.
func (s *Server) handlePlaybookRunHistory(c *gin.Context) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid run id")
		return
	}
	// Existence first: an empty history for a run that does not exist would read as "nothing happened yet" instead of "no such run".
	if _, _, loadErr := s.loadPlaybookRun(c, uint(id)); loadErr != nil {
		respondError(c, http.StatusNotFound, "no such playbook run")
		return
	}

	resource := strconv.FormatUint(id, 10)
	var rows []db.AuditLog
	if err := s.auditTenantScope(s.db.WithContext(s.ctx).Model(&db.AuditLog{}), c).
		Where("action IN ?", []string{"playbook_run_create", "playbook_run_transition"}).
		// The run id lives in agent_id, not in resource: LogAuditRecord's third argument is the RESOURCE TYPE ("playbook") and the fourth
		// is the per-record identifier, which for a playbook is the run. Filtering on resource alone returned an empty history for a run
		// that plainly had one.
		Where("resource = ?", "playbook").
		Where("agent_id = ?", resource).
		Order("id asc").
		Limit(playbookHistoryLimit).
		Find(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to load the run history")
		return
	}

	type entry struct {
		Action    string `json:"action"`
		User      string `json:"user,omitempty"`
		Details   string `json:"details"`
		Success   bool   `json:"success"`
		At        string `json:"at"`
		EntryHash string `json:"entry_hash,omitempty"`
	}
	out := make([]entry, 0, len(rows))
	for _, row := range rows {
		out = append(out, entry{
			Action: row.Action, User: row.User, Details: row.Details, Success: row.Success,
			At: row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			// The hash travels so the rows can be matched against an exported chain: this is a convenience view, not a second source of
			// truth, and a reviewer comparing it with audit.ndjson needs the link.
			EntryHash: row.EntryHash,
		})
	}

	respond(c, gin.H{
		"run_id":  id,
		"entries": out,
		"count":   len(out),
		"note": "read from the audit log, oldest first; the same rows appear in /api/audit/export, and entry_hash is included so a " +
			"reviewer can match them",
	})
}
