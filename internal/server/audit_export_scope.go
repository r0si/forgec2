package server

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// auditExportScopeHeader names the header that carries an audit export's filters, published by the capabilities document.
const auditExportScopeHeader = "X-Audit-Export-Scope"

// The filters an audit export applied, as one header.
//
// `X-Audit-Export-Filtered` already says that a selection was made - and the 199 warning says a filtered file is not a
// contiguous chain, which is the important caveat. What neither says is **which** selection produced this file, so an
// investigator holding `forgec2-audit-chain_filtered_....ndjson` cannot reproduce it. The scope header names every filter that
// shaped the rows, in the same format as the task export's.
//
// Format: `action=login;since=2026-10-01T00:00:00Z;user=admin` - semicolon-separated, sorted by key, or `all`. `since_id` is
// included because it changes which rows appear just as much as a time bound.
func auditExportScope(c *gin.Context) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	for _, name := range []string{"action", "user", "agent_id", "since", "until", "since_id"} {
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
