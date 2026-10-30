package server

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Markdown export of an engagement summary.
//
// The JSON summary answers a machine; a report has to answer a person who was not there, and the usual
// destination is a document rather than an API client. The export reuses the same computation as the JSON
// endpoint (engagementSummaryPayload) so the two can never describe the same exercise differently, and it is
// buffered before anything is written - the same rule the CSV and audit exports follow, because a truncated
// document that still opens is worse than an error.
//
// GET /api/engagements/:id/summary/export?format=markdown|json

// handleEngagementSummaryExport writes the summary as a downloadable document.
func (s *Server) handleEngagementSummaryExport(c *gin.Context) {
	data, engagement, ok := s.buildEngagementSummary(c)
	if !ok {
		return
	}

	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "markdown")))
	stamp := time.Now().UTC().Format("2006-01-02_150405")
	switch format {
	case "json":
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="engagement-%d_%s.json"`, engagement.ID, stamp))
		respond(c, data)
	case "markdown", "md":
		var body bytes.Buffer
		writeEngagementMarkdown(&body, data)
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="engagement-%d_%s.md"`, engagement.ID, stamp))
		c.Data(http.StatusOK, "text/markdown; charset=utf-8", body.Bytes())
	default:
		respondError(c, http.StatusBadRequest, `invalid format (expected "markdown" or "json")`)
	}
}

// writeEngagementMarkdown renders the summary a reader can paste into a report.
func writeEngagementMarkdown(body *bytes.Buffer, data gin.H) {
	engagement, _ := data["engagement"].(gin.H)
	window, _ := data["window"].(gin.H)
	agents, _ := data["agents"].(engagementSummaryAgentCounts)
	tasks, _ := data["tasks"].(db.TaskStats)
	actions, _ := data["actions"].([]engagementActionCount)

	name, _ := engagement["name"].(string)
	fmt.Fprintf(body, "# Engagement: %s\n\n", name)
	if notes, _ := engagement["notes"].(string); notes != "" {
		fmt.Fprintf(body, "%s\n\n", notes)
	}
	fmt.Fprintf(body, "- Window: %v -> %v (%s)\n", window["starts_at"], window["ends_at"], windowState(window))
	if created, _ := engagement["created_by"].(string); created != "" {
		fmt.Fprintf(body, "- Created by: %s\n", created)
	}
	fmt.Fprintf(body, "- Fleet covered: %d host(s), %d online, %d past their deadline\n\n", agents.Total, agents.Online, agents.Expired)

	total := tasks.Pending + tasks.Running + tasks.Completed + tasks.Failed
	fmt.Fprintf(body, "## Tasks inside the window\n\n%d task(s): %d completed, %d failed, %d pending, %d running\n\n",
		total, tasks.Completed, tasks.Failed, tasks.Pending, tasks.Running)

	// Failures grouped by shape: "eleven hosts timed out on the same scan" instead of eleven rows that
	// happen to look similar. Same computation as failures.json in the bundle - one grouping, two renderings.
	if failures, ok := data["failures"].(engagementFailures); ok && failures.FailedTasks > 0 {
		fmt.Fprintf(body, "## What went wrong\n\n")
		fmt.Fprintf(body, "%d task(s) failed inside the window, grouped by cause:\n\n", failures.FailedTasks)
		fmt.Fprintf(body, "| Failures | Hosts | Busiest host | Shape |\n|---|---|---|---|\n")
		shown := 0
		for _, shape := range failures.Shapes {
			if shown >= 5 {
				break
			}
			busiest := shape.TopAgentHost
			if busiest == "" {
				busiest = shape.TopAgent
			}
			fmt.Fprintf(body, "| %d | %d | %s | %s |\n", shape.Count, shape.Agents,
				strings.ReplaceAll(busiest, "|", "\u00a6"),
				strings.ReplaceAll(shape.Signature, "|", "\u00a6"))
			shown++
		}
		if len(failures.Shapes) > 5 {
			fmt.Fprintf(body, "| ... | ... | ... | %d more shapes not shown |\n", len(failures.Shapes)-5)
		}
		fmt.Fprintf(body, "\nFull detail in `failures.json`.\n\n")
	}

	fmt.Fprintf(body, "## Audited actions\n\n")
	if len(actions) == 0 {
		fmt.Fprintf(body, "_No audited actions were recorded inside the window._\n")
		fmt.Fprintf(body, "%s", bundleLimitsSection())
		return
	}
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].Count == actions[j].Count {
			return actions[i].Action < actions[j].Action
		}
		return actions[i].Count > actions[j].Count
	})
	fmt.Fprintf(body, "| Action | Count |\n|---|---|\n")
	for _, action := range actions {
		fmt.Fprintf(body, "| `%s` | %d |\n", action.Action, action.Count)
	}
	fmt.Fprintf(body, "%s", bundleLimitsSection())
}

// bundleLimitsSection says what the package does NOT contain.
//
// A handover that lists only what it includes invites the reader to treat it as the whole record. Several things an operator
// might look for are deliberately absent - and one of them is genuinely unavailable rather than withheld: the notification
// delivery counters are in-memory and reset with the process, so a restart before handover loses them.
func bundleLimitsSection() string {
	var body strings.Builder
	body.WriteString("\n## What this package does not contain\n\n")
	body.WriteString("- **Credential material.** `credentials.json` lists what was captured, without passwords or hashes: a bundle " +
		"leaves the console, and secrets stay in the system that audits their use.\n")
	body.WriteString("- **Notification delivery history.** Route counters live in memory and reset with the process, so they are " +
		"not part of a package; if delivery failures matter to the review, export them before restarting the server.\n")
	body.WriteString("- **Anything outside the window.** Members are bounded by the engagement's start and end; a host that " +
		"checked in before or after it is absent by construction, not by accident.\n")
	// The one absence a reader is most likely to misread as "nothing was planned": structured playbook runs exist, have progress and
	// owners, and are simply NOT scoped to an engagement - so they cannot be selected into this archive without an invented link.
	// Saying so is the difference between "the checklist was empty" and "the checklist is somewhere else".
	body.WriteString("- **Structured playbook runs.** These are tenant-scoped, not engagement-scoped, so they cannot be selected into a " +
		"package without an engagement link that does not exist. They are not empty and not lost: read them with " +
		"`GET /api/playbooks/runs` (and `?attention=1` for the ones waiting on a person) or export them with `?format=csv`.\n")
	return body.String()
}

// windowState words the window the way a reader needs it, rather than as a boolean.
func windowState(window gin.H) string {
	if open, _ := window["open"].(bool); open {
		return "still open"
	}
	return "closed"
}

// buildEngagementSummary computes the summary for one engagement, answering 404 itself when it cannot.
func (s *Server) buildEngagementSummary(c *gin.Context) (gin.H, db.Engagement, bool) {
	var engagement db.Engagement
	var zero db.Engagement
	id := strings.TrimSpace(c.Param("id"))
	lookup := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Engagement{}), c)
	if err := lookup.Where("id = ?", id).First(&engagement).Error; err != nil {
		respondError(c, http.StatusNotFound, "engagement not found")
		return nil, zero, false
	}

	now := time.Now().UTC()
	windowEnd := engagement.EndsAt
	if engagement.ClosedAt != nil && engagement.ClosedAt.Before(windowEnd) {
		windowEnd = *engagement.ClosedAt
	}
	if now.Before(windowEnd) {
		windowEnd = now
	}

	var agents []db.Implant
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
		Where("engagement_id = ?", engagement.ID).
		Select("id, status, last_seen, expires_at, hostname").
		Find(&agents).Error; err != nil {
		handleQueryError(c, err, "Failed to summarise the engagement fleet")
		return nil, zero, false
	}
	counts := engagementSummaryAgentCounts{Total: len(agents)}
	agentIDs := make([]string, 0, len(agents))
	offlineCutoff := now.Add(-s.offlineThreshold())
	for _, agent := range agents {
		agentIDs = append(agentIDs, agent.ID)
		if agent.LastSeen.After(offlineCutoff) {
			counts.Online++
		}
		if agent.ExpiresAt != nil && now.After(*agent.ExpiresAt) {
			counts.Expired++
		}
	}

	tasks := db.TaskStats{}
	if len(agentIDs) > 0 {
		var rows []struct {
			Status string
			Count  int
		}
		if err := s.db.WithContext(s.ctx).Model(&db.Task{}).
			Select("status, COUNT(*) AS count").
			Where("agent_id IN ?", agentIDs).
			Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
			Group("status").
			Scan(&rows).Error; err != nil {
			handleQueryError(c, err, "Failed to summarise engagement tasks")
			return nil, zero, false
		}
		for _, row := range rows {
			switch row.Status {
			case "pending":
				tasks.Pending += row.Count
			case "running":
				tasks.Running += row.Count
			case "completed":
				tasks.Completed += row.Count
			case "failed":
				tasks.Failed += row.Count
			}
		}
	}

	var actionRows []engagementActionCount
	if err := s.db.WithContext(s.ctx).Model(&db.AuditLog{}).
		Select("action, COUNT(*) AS count").
		Where("tenant_id = ? OR tenant_id = 0", s.currentTenantID(c)).
		Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
		Group("action").
		Order("count DESC, action ASC").
		Limit(25).
		Scan(&actionRows).Error; err != nil {
		handleQueryError(c, err, "Failed to summarise engagement actions")
		return nil, zero, false
	}

	// Failed tasks inside the window, grouped by shape for the narrative. The same computation as
	// failures.json in the bundle - one grouping, two renderings.
	hostnames := make(map[string]string, len(agents))
	for _, agent := range agents {
		hostnames[agent.ID] = agent.Hostname
	}
	var failedTasks []db.Task
	if len(agentIDs) > 0 {
		if err := s.db.WithContext(s.ctx).Model(&db.Task{}).
			Where("agent_id IN ?", agentIDs).
			Where("status = ? AND error != ?", "failed", "").
			Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
			Find(&failedTasks).Error; err != nil {
			handleQueryError(c, err, "Failed to summarise engagement failures")
			return nil, zero, false
		}
	}
	failures := buildEngagementFailures(failedTasks, hostnames)

	return engagementSummaryPayload(engagement, now, windowEnd, counts, tasks, actionRows, failures), engagement, true
}
