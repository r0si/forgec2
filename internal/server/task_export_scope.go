package server

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// The filters a task export applied, as one header.
//
// The export already announces that it was truncated, because a capped file that looks complete is a lie of omission. A
// FILTERED file has the same defect one layer out: `tasks.csv` for one exercise looks exactly like `tasks.csv` for the whole
// tenant, and the person who receives it has no way to tell. The header names every filter that shaped the rows, so the file
// carries its own scope.
//
// Format: `engagement=7;status=failed;agent=web-01;since=2026-10-01T00:00:00Z` - semicolon-separated, sorted by key, and
// `all` when nothing was filtered.
func taskExportScope(c *gin.Context) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 5)
	for _, name := range []string{"engagement", "agent", "status", "type", "since", "until"} {
		if value := strings.TrimSpace(c.Query(name)); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
