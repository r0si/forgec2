package server

import (
	"fmt"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// auditExportETag identifies the export a request would produce.
//
// Re-fetching a large export is expensive for both sides, and an investigator holding a copy usually
// wants to know "is there anything new" rather than download it again. The tag is derived from the
// newest audit id: every append bumps it, and so does a purge (which writes an audit_purge marker), so
// the tag changes whenever the export's content could have. It also covers the tenant and every
// parameter, because two different selections must never share a tag.
//
// Weak on purpose: two responses sharing a tag are equivalent for the investigator, not necessarily
// byte-identical.
func (s *Server) auditExportETag(c *gin.Context, sinceID uint, limit int, filtered bool, format string) string {
	var newest uint
	// A failure here must not fail the export: an empty max just makes the tag conservative.
	if err := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).
		Select("COALESCE(MAX(id), 0)").Scan(&newest).Error; err != nil {
		newest = 0
	}
	parts := []string{
		fmt.Sprintf("v1-%d", s.currentTenantID(c)),
		fmt.Sprintf("max=%d", newest),
		fmt.Sprintf("since=%d", sinceID),
		fmt.Sprintf("limit=%d", limit),
	}
	if filtered {
		parts = append(parts, "filtered="+strings.Join([]string{
			c.Query("user"), c.Query("action"), c.Query("agent_id"), c.Query("since"), c.Query("until"),
		}, ","))
	}
	parts = append(parts, "fmt="+format)
	return weakETag(strings.Join(parts, "-"))
}
