package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Failure signatures across the system.
//
// The task-failure report answers "why do tasks fail" grouped by reason code, and the route counters answer "is this route
// delivering". Neither answers the question that comes next: **what shape of failure dominates right now** - across tasks and
// notifications - which is the difference between four separate 401s and one expired token.
//
// The normalisation is the same one the reason report uses (paths, ids and digits collapsed), so a signature means the same
// thing in both places and in the UI.
//
// GET /api/stats/failures/signatures?since=7d

const failureSignatureTopN = 10

// handleFailureSignatures merges task and notification failure shapes into one answer.
func (s *Server) handleFailureSignatures(c *gin.Context) {
	since := strings.TrimSpace(c.DefaultQuery("since", "7d"))
	window, ok := parseStatsWindow(since)
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid since (expected e.g. 24h or 7d)")
		return
	}
	cutoff := time.Now().Add(-window)

	// Tasks: every failed task in the window, not just the unclassified ones. The grouping is by SHAPE, so a reason code is
	// no longer the axis.
	var rows []struct {
		Type  string
		Error string
	}
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Task{}), c).
		Select("type, error").
		Where("status = ?", "failed").
		Where("updated_at >= ?", cutoff).
		Order("updated_at DESC").
		Limit(failureStatsSampleLimit).
		Scan(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to load failed tasks")
		return
	}

	type taskSignature struct {
		Signature string   `json:"signature"`
		Count     int      `json:"count"`
		Reasons   []string `json:"reasons"`
		Sample    string   `json:"sample"`
	}
	type accumulator struct {
		count   int
		reasons map[string]bool
		sample  string
	}
	bytaskSignature := map[string]*accumulator{}
	for _, row := range rows {
		signature := normaliseFailureText(row.Error)
		if signature == "" {
			continue
		}
		entry, exists := bytaskSignature[signature]
		if !exists {
			entry = &accumulator{reasons: map[string]bool{}, sample: truncateString(strings.TrimSpace(row.Error), failureSampleMaxChars)}
			bytaskSignature[signature] = entry
		}
		entry.count++
		if reason := classifyFailure(row.Error); reason != "" {
			entry.reasons[reason] = true
		}
	}

	taskSignatures := make([]taskSignature, 0, len(bytaskSignature))
	for signature, entry := range bytaskSignature {
		reasons := make([]string, 0, len(entry.reasons))
		for reason := range entry.reasons {
			reasons = append(reasons, reason)
		}
		// Stable order inside a signature, so the same input always renders the same row.
		sort.Strings(reasons)
		taskSignatures = append(taskSignatures, taskSignature{
			Signature: signature, Count: entry.count, Reasons: reasons, Sample: entry.sample,
		})
	}
	sort.SliceStable(taskSignatures, func(i, j int) bool {
		if taskSignatures[i].Count != taskSignatures[j].Count {
			return taskSignatures[i].Count > taskSignatures[j].Count
		}
		return taskSignatures[i].Signature < taskSignatures[j].Signature
	})
	if len(taskSignatures) > failureSignatureTopN {
		taskSignatures = taskSignatures[:failureSignatureTopN]
	}

	// Notifications: the in-memory counters, grouped by the same normalisation. The message is redacted first, because the
	// signature is shown in a table and a webhook path is a credential.
	type notificationSignature struct {
		Signature string   `json:"signature"`
		Routes    []string `json:"routes"`
	}
	byNotification := map[string][]string{}
	for routeID, stat := range notificationRouteStatsSnapshot() {
		if stat.Failed == 0 || stat.LastError == "" {
			continue
		}
		signature := normaliseFailureText(redactDeliveryError(stat.LastError))
		if signature == "" {
			continue
		}
		byNotification[signature] = append(byNotification[signature], strconv.FormatUint(uint64(routeID), 10))
	}
	notificationSignatures := make([]notificationSignature, 0, len(byNotification))
	for signature, routes := range byNotification {
		sort.Strings(routes)
		notificationSignatures = append(notificationSignatures, notificationSignature{Signature: signature, Routes: routes})
	}
	sort.SliceStable(notificationSignatures, func(i, j int) bool {
		if len(notificationSignatures[i].Routes) != len(notificationSignatures[j].Routes) {
			return len(notificationSignatures[i].Routes) > len(notificationSignatures[j].Routes)
		}
		return notificationSignatures[i].Signature < notificationSignatures[j].Signature
	})
	if len(notificationSignatures) > failureSignatureTopN {
		notificationSignatures = notificationSignatures[:failureSignatureTopN]
	}

	respond(c, gin.H{
		"since":                   since,
		"task_failures":           len(rows),
		"task_signatures":         taskSignatures,
		"notification_signatures": notificationSignatures,
		"note": "one shape per row: paths, ids and digits are collapsed, so four 401s read as one problem. " +
			"task_signatures come from the window; notification_signatures come from counters since process start",
	})
}
