package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Engagement bundle: one download that hands over the whole exercise.
//
// Ending an engagement means delivering it: the summary, the tasks of its hosts, the audit trail of its window and the
// plugin set the work depended on. Collecting that by hand is four downloads and a spreadsheet, and the audit part is
// where the manual step gets skipped - which is exactly the part an after-action reviewer needs. This assembles them
// in memory as a zip.
//
// The parts are produced by the SAME code as their standalone endpoints (buildEngagementSummary, the export field
// catalogue, buildPluginIndex), so a bundle cannot contain a summary that disagrees with the summary endpoint.
//
// GET /api/engagements/:id/bundle

const engagementBundleMaxBytes = 64 << 20

// handleEngagementBundle streams a zip with the engagement's deliverables.
func (s *Server) handleEngagementBundle(c *gin.Context) {
	summary, engagement, ok := s.buildEngagementSummary(c)
	if !ok {
		return // buildEngagementSummary already answered 404 or a 5xx
	}

	now := time.Now().UTC()
	windowEnd := engagement.EndsAt
	if engagement.ClosedAt != nil && engagement.ClosedAt.Before(windowEnd) {
		windowEnd = *engagement.ClosedAt
	}
	if now.Before(windowEnd) {
		windowEnd = now
	}

	// The hosts this engagement covered, and their tasks.
	var agents []db.Implant
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
		Where("engagement_id = ?", engagement.ID).Find(&agents).Error; err != nil {
		handleQueryError(c, err, "Failed to load the engagement agents")
		return
	}
	agentIDs := make([]string, 0, len(agents))
	for _, agent := range agents {
		agentIDs = append(agentIDs, agent.ID)
	}

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	add := func(name string, write func(*zip.Writer) error) bool {
		if err := write(archive); err != nil {
			slog.Error("Failed to build an engagement bundle member", "engagement_id", engagement.ID, "member", name, "err", err)
			return false
		}
		return true
	}
	fail := func() {
		_ = archive.Close()
		respondError(c, http.StatusInternalServerError, "failed to build the engagement bundle")
	}

	// summary.md - the human-facing document.
	var markdown bytes.Buffer
	writeEngagementMarkdown(&markdown, summary)
	if !add("summary.md", func(w *zip.Writer) error {
		f, err := w.Create("summary.md")
		if err != nil {
			return err
		}
		_, err = f.Write(markdown.Bytes())
		return err
	}) {
		fail()
		return
	}

	// The window rows are fetched once: tasks.csv and timeline.md are two views of the same set, and a second
	// query could see a different one.
	var windowTasks []db.Task
	if len(agentIDs) > 0 {
		if err := s.db.WithContext(s.ctx).Preload("Agent", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, hostname")
		}).Select("tasks.id, tasks.agent_id, tasks.type, tasks.command, tasks.result, tasks.error, tasks.status, tasks.created_at").
			Where("agent_id IN ?", agentIDs).
			Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
			Order("created_at asc").Limit(ExportTaskLimit).Find(&windowTasks).Error; err != nil {
			handleQueryError(c, err, "Failed to load the engagement tasks")
			return
		}
	}

	// tasks.csv - the same columns the export offers, in the default order.
	if len(agentIDs) > 0 && !add("tasks.csv", func(w *zip.Writer) error {
		tasks := windowTasks
		f, err := w.Create("tasks.csv")
		if err != nil {
			return err
		}
		writer := csv.NewWriter(f)
		fields := defaultExportFields("csv")
		headers := make([]string, 0, len(fields))
		for _, field := range fields {
			headers = append(headers, field.header)
		}
		writer.Write(headers)
		for _, task := range tasks {
			row := make([]string, 0, len(fields))
			for _, field := range fields {
				row = append(row, field.value(task, task.Agent.Hostname, engagement.Name))
			}
			writer.Write(row)
		}
		writer.Flush()
		return writer.Error()
	}) {
		fail()
		return
	}

	// The audit rows are fetched once as well: audit.ndjson is the verifiable form, timeline.md the readable one,
	// and both must describe the same rows.
	var windowAudit []db.AuditLog
	if err := s.auditTenantScope(s.db.WithContext(s.ctx).Model(&db.AuditLog{}), c).
		Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
		Order("id asc").Limit(200000).Find(&windowAudit).Error; err != nil {
		handleQueryError(c, err, "Failed to load the engagement audit window")
		return
	}

	// timeline.md - the exercise as a chronological narrative, for someone who will not open a spreadsheet.
	if !add("timeline.md", func(w *zip.Writer) error {
		f, err := w.Create("timeline.md")
		if err != nil {
			return err
		}
		_, err = f.Write(buildEngagementTimeline(engagement, windowTasks, windowAudit))
		return err
	}) {
		fail()
		return
	}

	// audit.ndjson - the verification-relevant rows of the window, with every hashed field.
	if !add("audit.ndjson", func(w *zip.Writer) error {
		rows := windowAudit
		f, err := w.Create("audit.ndjson")
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		for i := range rows {
			row := auditChainExportRow{
				ID: rows[i].ID, TenantID: rows[i].TenantID, User: rows[i].User,
				Action: rows[i].Action, Resource: rows[i].Resource, AgentID: rows[i].AgentID,
				IP: rows[i].IP, Success: rows[i].Success, Error: rows[i].Error,
				Details: rows[i].Details, PrevHash: rows[i].PrevHash, EntryHash: rows[i].EntryHash,
				CreatedAt: rows[i].CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			if err := enc.Encode(&row); err != nil {
				return err
			}
		}
		return nil
	}) {
		fail()
		return
	}

	// agents.json and plugins.json - what the exercise covered and what it depended on.
	if !add("agents.json", func(w *zip.Writer) error {
		f, err := w.Create("agents.json")
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(agents)
	}) {
		fail()
		return
	}
	// credentials.json - METADATA ONLY. A handover bundle leaves the console: it gets emailed, put on a share, attached
	// to a report. Shipping passwords and hashes in it would move live secrets out of the system that audits their use,
	// so the file records what was found and where, and says where the secrets live instead.
	if !add("credentials.json", func(w *zip.Writer) error {
		var credentials []db.CredentialEntry
		if len(agentIDs) > 0 {
			if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.CredentialEntry{}), c).
				Where("agent_id IN ?", agentIDs).
				Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
				Order("created_at asc").Limit(5000).Find(&credentials).Error; err != nil {
				return err
			}
		}
		type credentialRecord struct {
			ID        uint   `json:"id"`
			AgentID   string `json:"agent_id"`
			Domain    string `json:"domain,omitempty"`
			Username  string `json:"username,omitempty"`
			Type      string `json:"type,omitempty"`
			Source    string `json:"source,omitempty"`
			Tags      string `json:"tags,omitempty"`
			CreatedAt string `json:"created_at"`
		}
		records := make([]credentialRecord, 0, len(credentials))
		for _, credential := range credentials {
			records = append(records, credentialRecord{
				ID: credential.ID, AgentID: credential.AgentID, Domain: credential.Domain,
				Username: credential.Username, Type: credential.Type, Source: credential.Source,
				Tags: credential.Tags, CreatedAt: credential.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		f, err := w.Create("credentials.json")
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(gin.H{
			"count": len(records),
			"note": "metadata only: passwords and hashes are deliberately excluded from the bundle. " +
				"Retrieve them in the console, where the access is audited.",
			"credentials": records,
		})
	}) {
		fail()
		return
	}
	// iocs.json - the indicators extracted from the engagement hosts, using the same extractor as the IOC export so
	// the bundle and the export cannot disagree about what counts as an indicator.
	if !add("iocs.json", func(w *zip.Writer) error {
		scope := s.db.Model(&db.Implant{}).Select("id").Where("engagement_id = ?", engagement.ID)
		// The extractor takes a day count rather than a window: give it the span of the engagement, rounded up, and
		// say so in the file, because a reader deserves to know this bound is looser than the others.
		days := int(windowEnd.Sub(engagement.StartsAt).Hours()/24) + 1
		if days < 1 {
			days = 1
		}
		if days > 365 {
			days = 365
		}
		indicators, _, err := s.extractIOCs(days, false, scope)
		if err != nil {
			return err
		}
		f, err := w.Create("iocs.json")
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(gin.H{
			"count":       len(indicators),
			"window_days": days,
			"note":        "the extraction bound is a day count spanning the engagement rather than the exact window the other members use",
			"indicators":  indicators,
		})
	}) {
		fail()
		return
	}
	if !add("plugins.json", func(w *zip.Writer) error {
		f, err := w.Create("plugins.json")
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(s.buildPluginIndex(c))
	}) {
		fail()
		return
	}

	if err := archive.Close(); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to close the engagement bundle")
		return
	}
	if buf.Len() > engagementBundleMaxBytes {
		// Refused rather than truncated: a half-delivered bundle is a handover that silently lacks evidence.
		respondError(c, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("the engagement bundle would exceed %d bytes; export its parts separately", engagementBundleMaxBytes))
		return
	}

	s.LogAuditRecord(c, "engagement_bundle", "engagement", fmt.Sprintf("%d", engagement.ID),
		fmt.Sprintf("bundled %d agent(s), %d member(s), %d byte(s)", len(agents), 5, buf.Len()), true, nil)

	name := fmt.Sprintf("engagement-%d_%s.zip", engagement.ID, time.Now().UTC().Format("2006-01-02_150405"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	c.Data(http.StatusOK, "application/zip", buf.Bytes())
}

// buildEngagementTimeline renders the exercise as a chronological narrative.
//
// The bundle is handed to someone who was not there (an after-action reviewer, a customer, a lawyer), and a CSV plus
// an NDJSON dump makes them reconstruct the story themselves. This interleaves the audited actions and the task
// outcomes by time - the two records that answer "what happened, in what order" - and says when a record was capped
// rather than trailing off silently.
func buildEngagementTimeline(engagement db.Engagement, tasks []db.Task, auditRows []db.AuditLog) []byte {
	const maxLines = 500

	type entry struct {
		at     time.Time
		kind   string
		detail string
	}
	entries := make([]entry, 0, len(tasks)+len(auditRows))
	for _, row := range auditRows {
		detail := row.Action
		if row.Resource != "" {
			detail += " " + row.Resource
		}
		if row.AgentID != "" {
			detail += " on " + row.AgentID
		}
		if row.User != "" {
			detail += " by " + row.User
		}
		if !row.Success {
			detail += " (failed)"
		}
		entries = append(entries, entry{at: row.CreatedAt, kind: "audit", detail: detail})
	}
	for _, task := range tasks {
		host := task.AgentID
		if task.Agent.Hostname != "" {
			host = task.Agent.Hostname
		}
		entries = append(entries, entry{
			at:     task.CreatedAt,
			kind:   "task",
			detail: fmt.Sprintf("%s %s on %s -> %s", task.Type, task.Command, host, task.Status),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].at.Equal(entries[j].at) {
			return entries[i].kind < entries[j].kind
		}
		return entries[i].at.Before(entries[j].at)
	})

	var body bytes.Buffer
	fmt.Fprintf(&body, "# Timeline: %s\n\n", engagement.Name)
	fmt.Fprintf(&body, "Window: %s -> %s\n\n", engagement.StartsAt.UTC().Format(time.RFC3339),
		engagement.EndsAt.UTC().Format(time.RFC3339))
	if len(entries) == 0 {
		body.WriteString("_No audited actions or tasks were recorded inside the window._\n")
		return body.Bytes()
	}
	fmt.Fprintf(&body, "| Time (UTC) | Kind | Detail |\n|---|---|---|\n")
	shown := 0
	for _, item := range entries {
		if shown >= maxLines {
			fmt.Fprintf(&body, "\n_%d later entries omitted; the full record is in audit.ndjson and tasks.csv._\n",
				len(entries)-shown)
			break
		}
		fmt.Fprintf(&body, "| %s | %s | %s |\n", item.at.UTC().Format("2006-01-02 15:04:05"), item.kind,
			strings.ReplaceAll(item.detail, "|", "¦"))
		shown++
	}
	return body.Bytes()
}
