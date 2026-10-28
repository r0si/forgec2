package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestTaskExportTimeBounds covers ?since= / ?until= on the CSV export.
//
// The export could already be limited and filtered by engagement but not by time, which is the first filter an
// operator reaches for ("the window of the exercise", "what ran last night"). Both spellings work - the
// dashboard's `24h` and an RFC3339 instant - and they come from the SAME parser as the audit export, so "since
// 24h" cannot mean two different things depending on which export is open.
func TestTaskExportTimeBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	seed := func(agentID string, age time.Duration) {
		// The export prints the agent HOSTNAME, so the fixture needs an implant row or every Agent cell is
		// empty and the assertions would compare empty strings (which is exactly how this test first failed).
		if err := s.db.Create(&db.Implant{
			ID: agentID, TenantID: 1, Hostname: agentID + "-host", Status: "online", LastSeen: now,
		}).Error; err != nil {
			t.Fatalf("seed agent %s: %v", agentID, err)
		}
		created := now.Add(-age)
		if err := s.db.Create(&db.Task{
			AgentID: agentID, TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
			CreatedAt: created, UpdatedAt: created,
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for i, agentID := range []string{"recent", "hours-ago", "days-ago", "weeks-ago"} {
		ages := []time.Duration{time.Minute, 5 * time.Hour, 3 * 24 * time.Hour, 20 * 24 * time.Hour}
		seed(agentID, ages[i])
	}

	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportTasks(c)
		return w
	}
	agents := func(w *httptest.ResponseRecorder) []string {
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
		if err != nil {
			t.Fatalf("csv: %v", err)
		}
		out := make([]string, 0, len(records))
		for _, row := range records[1:] {
			out = append(out, row[1])
		}
		return out
	}

	// 1. No bounds: everything.
	if got := agents(call("")); len(got) != 4 {
		t.Fatalf("unbounded export = %v, want all four", got)
	}

	// 2. A relative since keeps only the window.
	if got := agents(call("?since=24h")); len(got) != 2 {
		t.Fatalf("since=24h = %v, want the minute-old and five-hour-old tasks", got)
	}

	// 3. An absolute since works too, and is the form an investigator pastes. Four days back includes the
	// three-day-old task as well as the two recent ones - three rows, not two (the fixture ages are 1m, 5h,
	// 3d, 20d).
	cutoff := now.Add(-4 * 24 * time.Hour).Format(time.RFC3339)
	if got := agents(call("?since=" + cutoff)); len(got) != 3 {
		t.Fatalf("since=<rfc3339> = %v, want three", got)
	}

	// 4. until bounds the other side: an open-ended "everything older than 2 days".
	if got := agents(call("?until=48h")); len(got) != 2 {
		t.Fatalf("until=48h = %v, want the three-day and twenty-day tasks", got)
	}

	// 5. Both together select one task.
	if got := agents(call("?since=4d&until=2d")); len(got) != 1 || got[0] != "days-ago-host" {
		t.Fatalf("window = %v, want only the three-day-old task", got)
	}

	// 6. Invalid or contradictory bounds are refused rather than silently ignored.
	for _, query := range []string{
		"?since=forever",
		"?until=whenever",
		"?since=2d&until=4d", // since after until
	} {
		if got := call(query); got.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", query, got.Code)
		}
	}

	// 7. The ETag is not a way around the validation: the same invalid query with a matching tag is still a 400.
	first := call("?since=24h")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Skip("no ETag on this server configuration")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export?since=forever", nil)
	c.Request.Header.Set("If-None-Match", etag)
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportTasks(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid since with a matching ETag = %d, want 400 (%s)", w.Code, fmt.Sprint(w.Header()))
	}
}
