package server

import (
	"sort"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
)

// Failures grouped by shape, for the handover bundle.
//
// The same normaliser the live signature view uses: the point of a handover is that the reviewer can compare it with what the
// console would have told them, and two normalisations would make that comparison a guess.
//
// What the grouping buys: "eleven hosts timed out on the same scan" instead of eleven rows that happen to look similar.
//
// The result is a NAMED type rather than a map on purpose: `failures.json` and the narrative heading are two renderings of
// one computation, and reading it back out of a `gin.H` with an anonymous struct assertion fails silently when the tags do
// not match exactly - which is how a summary table ends up with a header and no rows.
type engagementFailures struct {
	FailedTasks int            `json:"failed_tasks"`
	Shapes      []failureShape `json:"shapes"`
	Note        string         `json:"note"`
}

// failureShape is one group of failures that look the same.
type failureShape struct {
	Signature string   `json:"signature"`
	Count     int      `json:"count"`
	Agents    int      `json:"agents_affected"`
	Reasons   []string `json:"reasons"`
	Sample    string   `json:"sample"`
}

// buildEngagementFailures groups the window's failed tasks by shape.
func buildEngagementFailures(tasks []db.Task) engagementFailures {
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

	shapes := make([]failureShape, 0, len(groups))
	for signature, entry := range groups {
		reasons := make([]string, 0, len(entry.reasons))
		for reason := range entry.reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		shapes = append(shapes, failureShape{
			Signature: signature, Count: entry.count, Agents: len(entry.agents),
			Reasons: reasons, Sample: entry.sample,
		})
	}
	sort.SliceStable(shapes, func(i, j int) bool {
		if shapes[i].Count != shapes[j].Count {
			return shapes[i].Count > shapes[j].Count
		}
		return shapes[i].Signature < shapes[j].Signature
	})

	return engagementFailures{
		FailedTasks: failed,
		Shapes:      shapes,
		Note: "grouped by normalised shape (paths, ids and digits collapsed); " +
			"agents_affected counts distinct hosts",
	}
}
