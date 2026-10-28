package server

import (
	"fmt"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// taskExportETag identifies the CSV a request would stream.
//
// The export is capped at ExportTaskLimit rows, but it is still a download an operator re-runs after
// every batch, and re-fetching it only to discover nothing changed is wasted bandwidth on both sides.
// The tag is derived from BOTH the newest task id and the newest updated_at in the caller's scope: an
// id alone would miss the common case, because a task's result and status change without a new row
// being created - which is exactly when the CSV's most interesting columns change.
//
// Weak on purpose: equal tags mean "equivalent for the reader", not byte-identical.
func (s *Server) taskExportETag(c *gin.Context, limit int, format string) string {
	var newest struct {
		ID        uint
		UpdatedAt string
	}
	// A failure here must not fail the export: an empty tag just means "always send the file".
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Task{}), c).
		Select("COALESCE(MAX(id), 0) AS id, COALESCE(MAX(updated_at), '') AS updated_at").
		Scan(&newest).Error; err != nil {
		return ""
	}
	// Passed through weakETag like every other tag: one renderer, one escaper. Building the header
	// value with a raw format string meant a second sanitiser had to exist for the timestamp.
	// The format is part of the tag: the CSV and the JSON of the same rows are different documents, and a
	// client that revalidated the CSV must not be told 304 when it asks for the JSON.
	return weakETag(fmt.Sprintf("v1-%d-max=%d-upd=%s-limit=%d-fmt=%s",
		s.currentTenantID(c), newest.ID, newest.UpdatedAt, limit, format))
}
