package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

	// tasks.csv - the same columns the export offers, in the default order.
	if len(agentIDs) > 0 && !add("tasks.csv", func(w *zip.Writer) error {
		var tasks []db.Task
		if err := s.db.WithContext(s.ctx).Preload("Agent", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, hostname")
		}).Select("tasks.id, tasks.agent_id, tasks.type, tasks.command, tasks.result, tasks.error, tasks.status, tasks.created_at").
			Where("agent_id IN ?", agentIDs).
			Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
			Order("created_at asc").Limit(ExportTaskLimit).Find(&tasks).Error; err != nil {
			return err
		}
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

	// audit.ndjson - the verification-relevant rows of the window, with every hashed field.
	if !add("audit.ndjson", func(w *zip.Writer) error {
		var rows []db.AuditLog
		if err := s.auditTenantScope(s.db.WithContext(s.ctx).Model(&db.AuditLog{}), c).
			Where("created_at >= ? AND created_at <= ?", engagement.StartsAt, windowEnd).
			Order("id asc").Limit(200000).Find(&rows).Error; err != nil {
			return err
		}
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
