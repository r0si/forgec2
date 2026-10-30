package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Build failures grouped by WHY they failed.
//
// A build log row already carries the failure text, so this report classifies the history rather than asking anyone to remember a code:
// "six builds failed with toolchain_missing" is an environment problem, "six with compile_error" is a code problem, and those send an
// operator to different places. The classifier is the same one `error_hint` uses, so the report and a single failed build cannot describe
// the same failure differently.

// buildFailureWindowDefault bounds the report when the caller does not say: a day answers "is the build environment working today".
const buildFailureWindowDefault = 24 * time.Hour

// buildFailureWindowMax keeps the scan bounded, since this reads the build history rather than an aggregate table.
const buildFailureWindowMax = 30 * 24 * time.Hour

// handleBuildFailureStats reports the failure causes in a window.
func (s *Server) handleBuildFailureStats(c *gin.Context) {
	window := buildFailureWindowDefault
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		if parsed, ok := parseBuildWindow(raw); ok {
			window = parsed
		} else {
			respondError(c, http.StatusBadRequest, "since must be a duration such as 24h, or a number of hours")
			return
		}
	}
	if window > buildFailureWindowMax {
		window = buildFailureWindowMax
	}
	since := time.Now().Add(-window)

	var rows []db.BuildLog
	if err := s.db.WithContext(s.ctx).
		Where("status = ?", "failed").
		Where("created_at >= ?", since).
		Order("created_at desc").
		Limit(2000).
		Find(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to load build failures")
		return
	}

	type causeGroup struct {
		// Code is the closed-set hint code; Advice is the sentence that says what to do about it.
		Code      string   `json:"code"`
		Advice    string   `json:"advice"`
		Count     int      `json:"count"`
		Platforms []string `json:"platforms"`
		// Example is one failure's text, bounded: a report should show a sample, not a log archive.
		Example string `json:"example,omitempty"`
	}
	groups := map[string]*causeGroup{}
	platformsSeen := map[string]map[string]bool{}
	for _, row := range rows {
		hint := buildFailureHint(errors.New(row.Error))
		group, exists := groups[hint.Code]
		if !exists {
			group = &causeGroup{Code: hint.Code, Advice: hint.Text}
			groups[hint.Code] = group
			platformsSeen[hint.Code] = map[string]bool{}
		}
		group.Count++
		if row.Platform != "" {
			platformsSeen[hint.Code][row.Platform] = true
		}
		if group.Example == "" && row.Error != "" {
			group.Example = truncateBuildFailure(row.Error)
			if len(group.Example) > 300 {
				group.Example = group.Example[:300] + "…"
			}
		}
	}

	out := make([]causeGroup, 0, len(groups))
	for code, group := range groups {
		for platform := range platformsSeen[code] {
			group.Platforms = append(group.Platforms, platform)
		}
		sort.Strings(group.Platforms)
		out = append(out, *group)
	}
	// Most frequent first, then by code so two runs of the report agree.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Code < out[j].Code
	})

	// The one-line reading a caller should not have to derive.
	summary := "no failed builds in the window"
	if len(out) > 0 {
		summary = strconv.Itoa(len(rows)) + " failed build(s); most common cause: " + out[0].Code
		if out[0].Advice != "" {
			summary += " - " + out[0].Advice
		}
	}

	// ?format=csv takes the table away: this is the report an operator pastes into a ticket when the toolchain is broken on a build host,
	// and every other stats report here exports. One row per (cause, platform) keeps the platform column meaningful - a single row with a
	// comma-joined list would need parsing again at the other end.
	if strings.EqualFold(strings.TrimSpace(c.Query("format")), "csv") {
		var buf strings.Builder
		writer := csv.NewWriter(&buf)
		if err := writer.Write([]string{"Cause", "Advice", "Count", "Platforms", "Example"}); err != nil {
			handleQueryError(c, err, "Failed to encode the build failures")
			return
		}
		for _, group := range out {
			platforms := strings.Join(group.Platforms, " ")
			for _, platform := range group.Platforms {
				if err := writer.Write([]string{
					group.Code, group.Advice, strconv.Itoa(group.Count), platform, group.Example,
				}); err != nil {
					handleQueryError(c, err, "Failed to encode the build failures")
					return
				}
			}
			if platforms == "" {
				// A cause with no platform recorded still deserves a row, or the totals silently stop adding up.
				if err := writer.Write([]string{
					group.Code, group.Advice, strconv.Itoa(group.Count), "", group.Example,
				}); err != nil {
					handleQueryError(c, err, "Failed to encode the build failures")
					return
				}
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			handleQueryError(c, err, "Failed to encode the build failures")
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="forgec2_build-failures_%s.csv"`,
			time.Now().Format("2006-01-02_150405")))
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(buf.String()))
		return
	}

	respond(c, gin.H{
		"since":        since.UTC().Format("2006-01-02T15:04:05Z"),
		"window_hours": int(window.Hours()),
		"total_failed": len(rows),
		"causes":       out,
		"summary":      summary,
		"note":         "classified with the same code that error_hint reports for one build, so the report and a single failure agree",
	})
}

// parseBuildWindow accepts "24h" or a bare number of hours.
//
// Both spellings because an operator typing a query string uses whichever they saw first, and rejecting one of them is a papercut that
// teaches nothing.
func parseBuildWindow(raw string) (time.Duration, bool) {
	if hours, err := strconv.Atoi(raw); err == nil {
		if hours <= 0 {
			return 0, false
		}
		return time.Duration(hours) * time.Hour, true
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}
