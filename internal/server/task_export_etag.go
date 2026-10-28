package server

import (
	"fmt"
	"strings"

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
func (s *Server) taskExportETag(c *gin.Context, limit int) string {
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
	return fmt.Sprintf(`W/"v1-%d-max=%d-upd=%s-limit=%d"`,
		s.currentTenantID(c), newest.ID, sanitizeETagToken(newest.UpdatedAt), limit)
}

// sanitizeETagToken keeps a timestamp usable inside an opaque tag (no quotes, no spaces, no comma -
// a comma would split the tag on the client side).
func sanitizeETagToken(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == ':', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// taskExportNotModified answers 304 when the client already holds this CSV.
func taskExportNotModified(c *gin.Context, etag string) bool {
	if etag == "" {
		return false
	}
	match := strings.TrimSpace(c.GetHeader("If-None-Match"))
	if match == "" {
		return false
	}
	for _, candidate := range strings.Split(match, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
