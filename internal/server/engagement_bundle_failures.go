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
	// SignatureKind says which of this product's four "signatures" the rows below carry. Without it, a reader of the archive
	// finds a field named `signature` and has to guess whether it involves cryptography - and this one does not.
	SignatureKind string `json:"signature_kind"`
	// Unfinished is work the exercise had queued when the package was assembled. A handover that reports only what FAILED
	// implies the record is complete; a receiver who does not know there were pending tasks will read an incomplete operation as
	// a finished one - and those tasks are absent from the archive because they never ran.
	Unfinished unfinishedWork `json:"unfinished"`
}

// unfinishedWork counts the tasks that had not finished at handover.
type unfinishedWork struct {
	Pending    int    `json:"pending"`
	Running    int    `json:"running"`
	AgentsHeld int    `json:"agents_holding_work"`
	Note       string `json:"note"`
}

// failureShape is one group of failures that look the same.
type failureShape struct {
	Signature string `json:"signature"`
	Count     int    `json:"count"`
	Agents    int    `json:"agents_affected"`
	// The busiest host, named. The live signature view reports the same three fields, so a reviewer comparing the package with
	// the console is comparing like with like - and an id alone is not actionable, because hosts are listed by name.
	TopAgent      string   `json:"top_agent,omitempty"`
	TopAgentHost  string   `json:"top_agent_host,omitempty"`
	TopAgentCount int      `json:"top_agent_failures,omitempty"`
	Reasons       []string `json:"reasons"`
	Sample        string   `json:"sample"`
}

// buildEngagementFailures groups the window's failed tasks by shape.
func buildEngagementFailures(tasks []db.Task, hostnames map[string]string) engagementFailures {
	type accumulator struct {
		count    int
		agents   map[string]bool
		perAgent map[string]int
		reasons  map[string]bool
		sample   string
	}
	groups := map[string]*accumulator{}
	heldBy := map[string]bool{}
	pending, running := 0, 0
	failed := 0
	for _, task := range tasks {
		// Counted before the failure filter: a task that had not finished is not a failure, and it is exactly what a receiver
		// needs to know the record is incomplete.
		switch task.Status {
		case "pending":
			pending++
			if task.AgentID != "" {
				heldBy[task.AgentID] = true
			}
		case "running":
			running++
			if task.AgentID != "" {
				heldBy[task.AgentID] = true
			}
		}
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
				agents:   map[string]bool{},
				perAgent: map[string]int{},
				reasons:  map[string]bool{},
				sample:   truncateString(strings.TrimSpace(task.Error), failureSampleMaxChars),
			}
			groups[signature] = entry
		}
		entry.count++
		if task.AgentID != "" {
			entry.agents[task.AgentID] = true
			entry.perAgent[task.AgentID]++
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
		// Deterministic: highest count, then lowest id, so two builds of one dataset agree.
		topAgent, topCount := "", 0
		for agentID, count := range entry.perAgent {
			if count > topCount || (count == topCount && agentID < topAgent) {
				topAgent, topCount = agentID, count
			}
		}
		shapes = append(shapes, failureShape{
			Signature: signature, Count: entry.count, Agents: len(entry.agents),
			TopAgent: topAgent, TopAgentHost: hostnames[topAgent], TopAgentCount: topCount,
			Reasons: reasons, Sample: entry.sample,
		})
	}
	sort.SliceStable(shapes, func(i, j int) bool {
		if shapes[i].Count != shapes[j].Count {
			return shapes[i].Count > shapes[j].Count
		}
		return shapes[i].Signature < shapes[j].Signature
	})

	unfinished := unfinishedWork{Pending: pending, Running: running, AgentsHeld: len(heldBy)}
	if pending+running == 0 {
		unfinished.Note = "every task in the window had finished when this package was assembled"
	} else {
		unfinished.Note = "these tasks had not run when the package was assembled: the archive cannot contain their " +
			"results, so an absent result is unfinished work rather than a clean one"
	}
	return engagementFailures{
		SignatureKind: failureSignatureKind,
		Unfinished:    unfinished,
		FailedTasks:   failed,
		Shapes:        shapes,
		Note: "grouped by normalised shape (paths, ids and digits collapsed); " +
			"agents_affected counts distinct hosts",
	}
}

// hostnamesForTasks builds the id->hostname map the failure shapes need.
//
// Taken from the preloaded Agent relation rather than a second query: the bundle already loads it for tasks.csv, and a second
// lookup could see a different fleet than the rows being described.
func hostnamesForTasks(tasks []db.Task) map[string]string {
	hostnames := make(map[string]string, len(tasks))
	for _, task := range tasks {
		if task.Agent.Hostname != "" { // db.Task.Agent is a value, not a pointer: an unloaded one is simply empty
			hostnames[task.AgentID] = task.Agent.Hostname
		}
	}
	return hostnames
}
