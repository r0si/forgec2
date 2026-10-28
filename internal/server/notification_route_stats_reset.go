package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Resetting delivery counters.
//
// The counters are an operational indicator, which means they must be clearable: a route that failed for a week and was
// then fixed keeps a red "failing" state forever otherwise, and an indicator that can only get worse is one operators
// learn to ignore. Clearing is audited, because "the failures disappeared" is exactly the kind of change someone will
// want to explain later.
//
// POST /api/notification-routes/stats/reset          (all routes)
// POST /api/notification-routes/stats/reset?id=7     (one route)

// handleResetNotificationRouteStats clears the in-memory delivery counters.
func (s *Server) handleResetNotificationRouteStats(c *gin.Context) {
	rawID := c.Query("id")
	if rawID == "" {
		before := notificationRouteStatsSnapshot()
		resetNotificationStats()
		s.LogAuditRecord(c, "notification_stats_reset", "notification", "",
			fmt.Sprintf("cleared delivery counters for %d route(s)", len(before)), true, nil)
		respond(c, gin.H{"cleared": len(before), "scope": "all"})
		return
	}

	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid route id")
		return
	}
	// The route must exist: otherwise a typo silently "clears" nothing and the operator believes the badge was reset.
	var route db.NotificationRoute
	if err := s.db.WithContext(s.ctx).First(&route, uint(id)).Error; err != nil {
		respondError(c, http.StatusNotFound, "route not found")
		return
	}

	notificationStatsMu.Lock()
	_, had := notificationStats[uint(id)]
	delete(notificationStats, uint(id))
	notificationStatsMu.Unlock()

	s.LogAuditRecord(c, "notification_stats_reset", "notification", fmt.Sprintf("%d", uint(id)),
		"cleared delivery counters for route "+route.Name, true, nil)
	respond(c, gin.H{"cleared": boolToInt(had), "scope": "route", "route_id": uint(id)})
}

// boolToInt renders a boolean as 0 or 1 for a count field.
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
