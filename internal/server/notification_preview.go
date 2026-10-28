package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Notification delivery preview: "if this notification fired now, would anyone receive it".
//
// The failure this replaces is quiet in the worst way: DispatchNotification persists the row and then fans out
// in goroutines, so a route that filters the notification out, a route that is disabled, and a route whose
// target is unusable all look the same from the console - a notification that exists and reached nobody. This
// endpoint applies the SAME rules (enabled, minimum severity, target validity) to a candidate notification and
// reports each route's verdict, without persisting anything or delivering anything.
//
// POST /api/notifications/preview   {"title":"x","message":"y","severity":"warning"}

const notificationPreviewMaxRoutes = 50 // the same cap DispatchNotification applies

// notificationPreviewRequest is the candidate notification.
type notificationPreviewRequest struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Type     string `json:"type"`
}

// handlePreviewNotification evaluates the candidate against every configured route.
func (s *Server) handlePreviewNotification(c *gin.Context) {
	var body notificationPreviewRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"title":"...","message":"...","severity":"info|warning|critical"}`)
		return
	}
	severity := strings.ToLower(strings.TrimSpace(body.Severity))
	if severity == "" {
		severity = "info"
	}
	switch severity {
	case "info", "warning", "critical":
	default:
		respondError(c, http.StatusBadRequest, "invalid severity (expected info, warning or critical)")
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		respondError(c, http.StatusBadRequest, "title is required; it is what a route's message shows")
		return
	}

	var routes []db.NotificationRoute
	if err := s.db.WithContext(s.ctx).Order("name asc").Limit(notificationPreviewMaxRoutes).Find(&routes).Error; err != nil {
		handleQueryError(c, err, "Failed to load notification routes")
		return
	}

	type routeVerdict struct {
		ID           uint   `json:"id"`
		Name         string `json:"name"`
		Channel      string `json:"channel"`
		Enabled      bool   `json:"enabled"`
		MinSeverity  string `json:"min_severity"`
		WouldDeliver bool   `json:"would_deliver"`
		Reason       string `json:"reason"`
	}

	candidateRank := severityRank(severity)
	verdicts := make([]routeVerdict, 0, len(routes))
	deliveries := 0
	for _, route := range routes {
		verdict := routeVerdict{
			ID: route.ID, Name: route.Name, Channel: route.Channel,
			Enabled: route.Enabled, MinSeverity: route.MinSeverity,
		}
		switch {
		case !route.Enabled:
			verdict.Reason = "route is disabled"
		case candidateRank < severityRank(route.MinSeverity):
			verdict.Reason = fmt.Sprintf("severity %q is below the route's minimum %q", severity, route.MinSeverity)
		default:
			// The same target validation the create/update path applies: a route saved before a validation
			// existed, or edited in the database directly, can carry a target the sender will refuse.
			if err := validateNotificationTarget(route); err != nil {
				verdict.Reason = err.Error()
			} else {
				verdict.WouldDeliver = true
				verdict.Reason = "would deliver"
				deliveries++
			}
		}
		verdicts = append(verdicts, verdict)
	}

	if verdicts == nil {
		verdicts = []routeVerdict{}
	}

	s.LogAuditRecord(c, "notification_preview", "notification", "",
		fmt.Sprintf("previewed %q (%s): %d of %d route(s) would deliver", body.Title, severity, deliveries, len(verdicts)), true, nil)

	respond(c, gin.H{
		"severity":        severity,
		"routes":          verdicts,
		"would_deliver":   deliveries,
		"total_routes":    len(verdicts),
		"no_route_at_all": len(verdicts) == 0,
	})
}

// validateNotificationTarget reuses the sender's own validation so a preview cannot promise a delivery the
// sender would refuse.
func validateNotificationTarget(route db.NotificationRoute) error {
	switch route.Channel {
	case "telegram":
		if strings.TrimSpace(route.Secret) == "" {
			return fmt.Errorf("telegram route has no bot token")
		}
		if strings.TrimSpace(route.Target) == "" {
			return fmt.Errorf("telegram route has no chat id")
		}
		return nil
	case "discord", "webhook", "":
		if strings.TrimSpace(route.Target) == "" {
			return fmt.Errorf("route has no target URL")
		}
		if err := validateExternalURL(route.Target); err != nil {
			return fmt.Errorf("invalid target: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown channel %q", route.Channel)
	}
}
