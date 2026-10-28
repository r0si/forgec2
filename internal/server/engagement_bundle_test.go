package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestEngagementBundle covers the handover download: one zip with the summary, the tasks of the engagement's hosts,
// the audit rows of its window, the agents and the plugin set.
//
// The failure it replaces is a manual one - four downloads and a spreadsheet, with the audit part the step that gets
// skipped, which is exactly the part an after-action reviewer needs.
func TestEngagementBundle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{
		TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-3 * time.Hour), EndsAt: now.Add(-time.Hour),
	}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// One task inside the window and one outside it: only the inside one belongs in the handover.
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
		Result: "inside-window", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
		Result: "before-window", CreatedAt: now.Add(-10 * time.Hour), UpdatedAt: now.Add(-10 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed old task: %v", err)
	}
	// One audit row inside the window, one outside.
	if err := s.db.Create(&db.AuditLog{
		Action: "task_create", User: "op", TenantID: 1, Success: true,
		CreatedAt: now.Add(-90 * time.Minute), PrevHash: "p", EntryHash: "e",
	}).Error; err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if err := s.db.Create(&db.AuditLog{
		Action: "login", User: "op", TenantID: 1, Success: true,
		CreatedAt: now.Add(-10 * time.Hour), PrevHash: "p", EntryHash: "e",
	}).Error; err != nil {
		t.Fatalf("seed old audit: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle", nil)
	c.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleEngagementBundle(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/zip") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.zip"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// The archive must parse, and every promised member must be present.
	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("the body is not a zip: %v", err)
	}
	members := map[string][]byte{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		members[file.Name] = content
	}
	for _, name := range []string{"summary.md", "timeline.md", "tasks.csv", "audit.ndjson", "agents.json", "plugins.json"} {
		if _, ok := members[name]; !ok {
			t.Fatalf("the bundle is missing %s (has %v)", name, keysOf(members))
		}
	}

	// summary.md is the same document the summary endpoint renders.
	if !strings.Contains(string(members["summary.md"]), "# Engagement: DMZ exercise") {
		t.Fatalf("summary.md = %q", string(members["summary.md"]))
	}

	// timeline.md is the same two records as a chronological narrative, in one document.
	timeline := string(members["timeline.md"])
	if !strings.Contains(timeline, "# Timeline: DMZ exercise") {
		t.Fatalf("timeline.md = %q", timeline)
	}
	// The task line shows the command and outcome, not the result text: the timeline is a narrative, and the
	// result belongs in tasks.csv where it is not flattened into one cell.
	for _, want := range []string{"| Time (UTC) | Kind | Detail |", "whoami", "task_create"} {
		if !strings.Contains(timeline, want) {
			t.Fatalf("timeline.md is missing %q:\n%s", want, timeline)
		}
	}
	// The out-of-window rows must not appear in the narrative either.
	if strings.Contains(timeline, "before-window") || strings.Contains(timeline, "| login |") {
		t.Fatalf("timeline.md contains out-of-window records:\n%s", timeline)
	}
	// Chronological: the audit row is at -90m and the task at -2h, so the task line comes first.
	taskIndex := strings.Index(timeline, "whoami")
	auditIndex := strings.Index(timeline, "task_create")
	if taskIndex < 0 || auditIndex < 0 || taskIndex > auditIndex {
		t.Fatalf("timeline is not ordered by time (task at %d, audit at %d):\n%s", taskIndex, auditIndex, timeline)
	}

	// tasks.csv holds the in-window task only.
	records, err := csv.NewReader(bytes.NewReader(members["tasks.csv"])).ReadAll()
	if err != nil {
		t.Fatalf("tasks.csv: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("tasks.csv rows = %d, want header + 1 (the in-window task)", len(records))
	}
	if !strings.Contains(strings.Join(records[1], ","), "inside-window") {
		t.Fatalf("tasks.csv row = %v, want the in-window task", records[1])
	}

	// audit.ndjson holds the in-window row with both hashes, so it can be verified offline.
	auditLines := strings.Split(strings.TrimSpace(string(members["audit.ndjson"])), "\n")
	if len(auditLines) != 1 {
		t.Fatalf("audit.ndjson lines = %d, want 1", len(auditLines))
	}
	var auditRow auditChainExportRow
	if err := json.Unmarshal([]byte(auditLines[0]), &auditRow); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if auditRow.Action != "task_create" || auditRow.EntryHash == "" || auditRow.PrevHash == "" {
		t.Fatalf("audit row = %+v, want the in-window action with both hashes", auditRow)
	}

	// plugins.json is the same document the index endpoint returns.
	var plugins []pluginIndexEntry
	if err := json.Unmarshal(members["plugins.json"], &plugins); err != nil {
		t.Fatalf("plugins.json: %v", err)
	}

	// The handover is audited.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_bundle").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("engagement_bundle rows = %d, want 1", audited)
	}

	// Another tenant cannot bundle this engagement.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle", nil)
	c2.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(2))
	s.handleEngagementBundle(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant bundle = %d, want 404", w2.Code)
	}
}

// keysOf lists map keys for a failure message.
func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
