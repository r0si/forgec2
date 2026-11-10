package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleExportLoginEventsCSV streams the filtered login log as CSV. The panel's
// browser export used to cover only the visible page; a security handover
// needs every matching sign-in, so this shares the page's query filters and
// resolves countries from the cache (with the same small live-lookup budget as
// the page) instead of hammering a provider for a 50k-row file.
func (s *Server) handleExportLoginEventsCSV(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusServiceUnavailable, "database is not initialized")
		return
	}
	limit := atoiDefault(c.Query("limit"), 50000)
	if limit <= 0 || limit > 200000 {
		limit = 50000
	}

	var rows []db.AuditLog
	if err := s.loginEventQuery(c).Order("created_at DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read the login log")
		return
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="login-log-%s.csv"`, time.Now().Format("20060102-150405")))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Audit-Export-Rows", strconv.Itoa(len(rows)))
	if truncated {
		c.Header("X-Audit-Export-Truncated", "true")
	}

	// UTF-8 BOM so Excel reads Chinese country/city and usernames correctly.
	if _, err := c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"Time", "User", "Action", "IP", "Country", "City", "Result", "Details"})
	ctx := c.Request.Context()
	budget := loginGeoPerRequest
	for i := range rows {
		row := &rows[i]
		country, city := "", ""
		if row.IP != "" {
			if budget > 0 {
				country, city = s.loginCountry(ctx, row.IP)
				budget--
			} else {
				country, city = s.cachedLoginCountry(ctx, row.IP)
			}
		}
		result := "success"
		if !row.Success {
			result = "failed"
		}
		_ = w.Write([]string{
			row.CreatedAt.UTC().Format(time.RFC3339),
			safeCSVCell(row.User),
			safeCSVCell(row.Action),
			safeCSVCell(row.IP),
			safeCSVCell(country),
			safeCSVCell(city),
			result,
			safeCSVCell(row.Details),
		})
	}
	w.Flush()
}
