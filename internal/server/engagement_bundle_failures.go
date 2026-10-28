package server

import (
	"sort"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Failures grouped by shape, for the handover bundle.
//
// The same normaliser the live signature view uses: the point of a handover is that the reviewer can compare it with what the
// console would have told them, and two normalisations would make that comparison a guess.
//
// What the grouping buys: "eleven hosts timed out on the same scan" instead of eleven rows that happen to look similar.
func buildEngagementFailures(tasks []db.Task) gin.H {
	type accumulator struct {
		count   int
		agents  map[string]bool
		reasons map[string]bool
		sample  string
	}
	groups := map[string]*accumulator{}
	failed := 0
	for _, task := range tasks {
		if task.Status != "failed" || strings.TrimSpace(task.Error) == "" {
			continue
		}
		failed++
		signature := normaliseFailureText(task.Error)
		if signature == "" {
			continue
		}
		entry, exists := groups[signature]
		if !exists {
			entry = &accumulator{
				agents:  map[string]bool{},
				reasons: map[string]bool{},
				sample:  truncateString(strings.TrimSpace(task.Error), failureSampleMaxChars),
			}
			groups[signature] = entry
		}
		entry.count++
		if task.AgentID != "" {
			entry.agents[task.AgentID] = true
		}
		entry.reasons[classifyFailure(task.Error)] = true
	}

	type row struct {
		Signature string   `json:"signature"`
		Count     int      `json:"count"`
		Agents    int      `json:"agents_affected"`
		Reasons   []string `json:"reasons"`
		Sample    string   `json:"sample"`
	}
	rows := make([]row, 0, len(groups))
	for signature, entry := range groups {
		reasons := make([]string, 0, len(entry.reasons))
		for reason := range entry.reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		rows = append(rows, row{
			Signature: signature, Count: entry.count, Agents: len(entry.agents),
			Reasons: reasons, Sample: entry.sample,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Signature < rows[j].Signature
	})

	return gin.H{
		"failed_tasks": failed,
		"shapes":       rows,
		"note": "grouped by normalised shape (paths, ids and digits collapsed); " +
			"agents_affected counts distinct hosts",
	}
}
