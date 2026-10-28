package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Why tasks fail.
//
// "Twelve tasks failed this week" is not actionable; "nine of them timed out and two were denied by Defender" is. The error
// text is already stored on every failed task and the duration statistics already group by type and agent, but nobody could
// ask "what are the reasons" - so the only way to answer it was to read task rows one at a time.
//
// The classification is deliberately coarse and transparent: a short list of reason codes, each matched by a few
// substrings, with everything unmatched collected as `other` and shown with an example. It is a starting point for an
// investigation, not a verdict.
//
// GET /api/stats/tasks/failures?since=7d&group_by=reason|type|agent

const (
	failureStatsSampleLimit = 5000
	failureSampleMaxChars   = 160
)

// failureReason is one classification rule.
type failureReason struct {
	Code     string
	Patterns []string
}

// failureReasons is ordered: the first match wins, so the most specific explanations come first. "access is denied" must be
// checked before a generic "denied", and a timeout before a generic network error.
var failureReasons = []failureReason{
	{"timeout", []string{"timeout", "timed out", "deadline exceeded", "context deadline"}},
	{"access_denied", []string{"access is denied", "access denied", "permission denied", "privilege", "not permitted", "unauthorized", "401", "403"}},
	{"defender", []string{"defender", "antivirus", "amsi", "quarantine", "threat detected"}},
	{"not_found", []string{"not found", "no such file", "does not exist", "cannot find", "could not find"}},
	{"network", []string{"connection refused", "connection reset", "no route to host", "unreachable", "broken pipe", "network is down"}},
	{"dns", []string{"no such host", "name resolution", "dns"}},
	{"tls", []string{"tls", "certificate", "x509", "ssl"}},
	{"unsupported", []string{"unsupported", "not supported", "unimplemented", "unknown command", "invalid task type"}},
	{"disk", []string{"no space", "disk full", "write error", "read-only file system"}},
	{"cancelled", []string{"cancelled", "canceled", "aborted", "killed"}},
}

// classifyFailure maps an error text to a reason code.
func classifyFailure(text string) string {
	lowered := strings.ToLower(text)
	for _, reason := range failureReasons {
		for _, pattern := range reason.Patterns {
			if strings.Contains(lowered, pattern) {
				return reason.Code
			}
		}
	}
	return "other"
}

// handleTaskFailureStats classifies failed tasks in a window.
func (s *Server) handleTaskFailureStats(c *gin.Context) {
	since := strings.TrimSpace(c.DefaultQuery("since", "7d"))
	window, ok := parseStatsWindow(since)
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid since (expected e.g. 24h or 7d)")
		return
	}
	groupBy := strings.ToLower(strings.TrimSpace(c.DefaultQuery("group_by", "reason")))
	switch groupBy {
	case "reason", "type", "agent":
	default:
		respondError(c, http.StatusBadRequest, `invalid group_by (expected "reason", "type" or "agent")`)
		return
	}

	cutoff := time.Now().Add(-window)
	// Projection rather than Find: the classifier needs four columns, and pulling whole result blobs would make this
	// endpoint expensive on the very datasets it is meant to explain.
	var rows []struct {
		Type    string
		AgentID string
		Error   string
	}
	query := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Task{}), c).
		Select("type, agent_id, error").
		Where("status = ?", "failed").
		Where("updated_at >= ?", cutoff).
		Order("updated_at DESC").
		Limit(failureStatsSampleLimit)
	if err := query.Scan(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to load failed tasks")
		return
	}

	type bucket struct {
		Key    string `json:"key"`
		Count  int    `json:"count"`
		Sample string `json:"sample,omitempty"`
		// Tasks is the number of distinct task types in the bucket: a reason spread over many types is systemic, one
		// concentrated in a single type is that type's problem.
		Types int `json:"distinct_types"`
	}
	byKey := map[string]*bucket{}
	typeSeen := map[string]map[string]bool{}
	for _, row := range rows {
		key := ""
		switch groupBy {
		case "reason":
			key = classifyFailure(row.Error)
		case "type":
			key = row.Type
		case "agent":
			key = row.AgentID
		}
		if key == "" {
			key = "unknown"
		}
		entry, exists := byKey[key]
		if !exists {
			entry = &bucket{Key: key}
			byKey[key] = entry
			typeSeen[key] = map[string]bool{}
		}
		entry.Count++
		if entry.Sample == "" && strings.TrimSpace(row.Error) != "" {
			entry.Sample = truncateString(strings.TrimSpace(row.Error), failureSampleMaxChars)
		}
		if row.Type != "" {
			typeSeen[key][row.Type] = true
		}
	}

	buckets := make([]bucket, 0, len(byKey))
	for key, entry := range byKey {
		entry.Types = len(typeSeen[key])
		buckets = append(buckets, *entry)
	}
	// Most common first, then by name so the order is stable when counts tie.
	sort.SliceStable(buckets, func(i, j int) bool {
		if buckets[i].Count != buckets[j].Count {
			return buckets[i].Count > buckets[j].Count
		}
		return buckets[i].Key < buckets[j].Key
	})

	respond(c, gin.H{
		"since":     since,
		"group_by":  groupBy,
		"failed":    len(rows),
		"truncated": len(rows) >= failureStatsSampleLimit,
		"buckets":   buckets,
		"note": "reason codes are a starting point: first match wins, and unmatched errors are collected as 'other' " +
			"with an example",
	})
}
