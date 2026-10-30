package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestEngagementSummaryExport covers the Markdown export of a summary.
//
// The JSON endpoint answers a machine; a report has to answer a person who was not there, and the usual
// destination is a document. The export must therefore be complete before anything is written (the rule the
// CSV and audit exports follow) and must carry the facts a reader needs, not the API field names.
func TestEngagementSummaryExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	engagement := db.Engagement{
		TenantID: 1, Name: "DMZ exercise", Notes: "scoped to the DMZ", CreatedBy: "op",
		StartsAt: now.Add(-3 * day), EndsAt: now.Add(-day), ClosedAt: ptrTime(now.Add(-day)),
	}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online",
		LastSeen: now.Add(-s.offlineThreshold() / 2), EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", Type: "shell", Command: "whoami", Status: "completed",
		CreatedAt: now.Add(-2 * day), UpdatedAt: now.Add(-2 * day),
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := s.db.Create(&db.AuditLog{
		Action: "task_create", User: "op", TenantID: 1, Success: true,
		CreatedAt: now.Add(-2 * day), PrevHash: "p", EntryHash: "e",
	}).Error; err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	call := func(query string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/"+itoaUintTest(engagement.ID)+"/summary/export"+query, nil)
		c.Params = gin.Params{{Key: "id", Value: itoaUintTest(engagement.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleEngagementSummaryExport(c)
		return w
	}

	// 1. Markdown is the default and reads as a report.
	w := call("", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("Content-Type = %q", ct)
	}
	body := w.Body.String()
	for _, want := range []string{
		"# Engagement: DMZ exercise",
		"scoped to the DMZ",        // notes, not an API field
		"Fleet covered: 1 host(s)", // the human phrasing
		"1 completed",
		"| `task_create` | 1 |", // the action table
		"closed",                // the window state as a word
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the report is missing %q:\n%s", want, body)
		}
	}
	// A report must not leak API field names as headings.
	if strings.Contains(body, `"starts_at"`) {
		t.Fatalf("the report is JSON pasted into markdown:\n%s", body)
	}

	// 2. The filename carries the id and a timestamp, so two exports in a day do not collide.
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "engagement-"+itoaUintTest(engagement.ID)+"_") || !strings.HasSuffix(cd, ".md\"") {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// 3. The JSON variant is the same payload the endpoint returns.
	if w2 := call("?format=json", 1); w2.Code != http.StatusOK || !strings.Contains(w2.Header().Get("Content-Disposition"), ".json") {
		t.Fatalf("json export = %d (%q)", w2.Code, w2.Header().Get("Content-Disposition"))
	}

	// 4. An unknown format is a 400 rather than a silent fallback.
	if got := call("?format=pdf", 1); got.Code != http.StatusBadRequest {
		t.Fatalf("format=pdf = %d, want 400", got.Code)
	}

	// 5. Another tenant cannot export this engagement.
	if got := call("", 2); got.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant export = %d, want 404", got.Code)
	}
}

// TestEngagementSummaryExportIncludesFailureShapes covers that failed tasks are rendered as a
// grouped narrative in the Markdown export, using the same computation as failures.json in the bundle.
func TestEngagementSummaryExportIncludesFailureShapes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	engagement := db.Engagement{
		TenantID: 1, Name: "failure exercise", Notes: "scoped to failures", CreatedBy: "op",
		StartsAt: now.Add(-3 * day), EndsAt: now.Add(-day), ClosedAt: ptrTime(now.Add(-day)),
	}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	// Two agents, one with many failures, one with one.
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online",
		LastSeen: now.Add(-s.offlineThreshold() / 2), EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent a-1: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "a-2", TenantID: 1, Hostname: "db-02", Status: "online",
		LastSeen: now.Add(-s.offlineThreshold() / 2), EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent a-2: %v", err)
	}
	// a-1 has 3 failures of the same shape; a-2 has 1 of a different shape.
	for i := 0; i < 3; i++ {
		if err := s.db.Create(&db.Task{
			AgentID: "a-1", Type: "shell", Command: "whoami", Status: "failed",
			Error: "connection refused", CreatedAt: now.Add(-2 * day), UpdatedAt: now.Add(-2 * day),
		}).Error; err != nil {
			t.Fatalf("seed task a-1/%d: %v", i, err)
		}
	}
	if err := s.db.Create(&db.Task{
		AgentID: "a-2", Type: "shell", Command: "whoami", Status: "failed",
		Error: "access denied", CreatedAt: now.Add(-2 * day), UpdatedAt: now.Add(-2 * day),
	}).Error; err != nil {
		t.Fatalf("seed task a-2: %v", err)
	}

	call := func(query string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/"+itoaUintTest(engagement.ID)+"/summary/export"+query, nil)
		c.Params = gin.Params{{Key: "id", Value: itoaUintTest(engagement.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleEngagementSummaryExport(c)
		return w
	}

	w := call("", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// The narrative must name the two shapes, the counts, and the hosts.
	for _, want := range []string{
		"## What went wrong",
		"4 task(s) failed inside the window, grouped by cause:",
		"| Failures | Hosts | Busiest host | Shape |",
		"| 3 | 1 | web-01 | connection refused |",
		"| 1 | 1 | db-02 | access denied |",
		"Full detail in `failures.json`.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the report is missing %q:\n%s", want, body)
		}
	}

	// Cross-tenant cannot export this engagement.
	if got := call("", 2); got.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant export = %d, want 404", got.Code)
	}
}
