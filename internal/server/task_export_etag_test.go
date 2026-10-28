package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestTaskExportETag covers revalidation of the task CSV export.
//
// The tag has to change on the case that matters most: a task finishing. Its result and status change
// without a new row being created, so an id-only tag would keep answering 304 while the CSV's most
// interesting columns had changed.
func TestTaskExportETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now()
	seed := func(agentID string, tenant uint, status string, age time.Duration) db.Task {
		task := db.Task{
			AgentID: agentID, TenantID: tenant, Type: "shell", Command: "whoami",
			Status: status, Result: "out", CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		return task
	}
	first := seed("a-1", 1, "pending", 2*time.Minute)

	call := func(ifNoneMatch string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export", nil)
		if ifNoneMatch != "" {
			c.Request.Header.Set("If-None-Match", ifNoneMatch)
		}
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleExportTasks(c)
		return w
	}

	// 1. A download carries a tag and revalidation instructions.
	response := call("", 1)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the task export")
	}
	if cc := response.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if body := response.Body.String(); body == "" {
		t.Fatal("the first export was empty")
	}

	// 2. The same tag is a bodiless 304.
	repeat := call(etag, 1)
	if repeat.Code != http.StatusNotModified {
		t.Fatalf("repeat = %d, want 304", repeat.Code)
	}
	if body := repeat.Body.String(); body != "" {
		t.Fatalf("a 304 carried a body: %q", body)
	}

	// 3. A task finishing changes the CSV, so it must change the tag even though no row was added.
	if err := s.db.Model(&db.Task{}).Where("id = ?", first.ID).Updates(map[string]interface{}{
		"status": "completed", "result": "done",
	}).Error; err != nil {
		t.Fatalf("complete the task: %v", err)
	}
	afterUpdate := call(etag, 1)
	if afterUpdate.Code != http.StatusOK {
		t.Fatalf("after a task completed = %d, want 200 (the tag must change)", afterUpdate.Code)
	}
	if afterUpdate.Header().Get("ETag") == etag {
		t.Fatal("the ETag did not change when an existing task completed")
	}

	// 4. A new row changes it too.
	seed("a-1", 1, "pending", time.Minute)
	current := afterUpdate.Header().Get("ETag")
	if got := call(current, 1); got.Code != http.StatusOK {
		t.Fatalf("after a new task = %d, want 200", got.Code)
	}

	// 5. Tenant 2 has its own tag, and tenant 1's tag does not satisfy it.
	t2 := call("", 2)
	if t2.Header().Get("ETag") == current {
		t.Fatalf("two tenants share a tag: %q", t2.Header().Get("ETag"))
	}
	if got := call(current, 2); got.Code != http.StatusOK {
		t.Fatalf("tenant 1's tag satisfied tenant 2 (%d)", got.Code)
	}
}

// TestTaskExportIsSentWhole proves the export is complete before anything is written: the response
// carries a Content-Length that matches its body, and the CSV parses with the header row plus one row
// per exported task. Streaming could not promise either - a failed encode was appended to a 200.
func TestTaskExportIsSentWhole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now()
	for i, status := range []string{"completed", "failed", "pending"} {
		if err := s.db.Create(&db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami",
			Status: status, Result: "res,with comma", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export", nil)
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportTasks(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	length := w.Header().Get("Content-Length")
	if length == "" {
		t.Fatal("no Content-Length: the export is still being streamed")
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("Content-Type = %q", got)
	}

	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("the body is not valid CSV: %v", err)
	}
	// Header + three tasks; the comma inside a result must be quoted, not split.
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4 (%v)", len(records), records)
	}
	if records[0][0] != "Time" || records[0][6] != "Status" || records[0][7] != "Task ID" || records[0][8] != "Engagement" {
		t.Fatalf("header = %v", records[0])
	}
	for _, row := range records[1:] {
		if len(row) != 9 {
			t.Fatalf("row has %d fields, want 9: %v", len(row), row)
		}
		// The engagement cell is empty for an agent that belongs to no exercise, which is the honest value.
		if row[8] != "" {
			t.Fatalf("engagement = %q for an unlinked agent, want empty", row[8])
		}
		// The id column exists so a row can be matched back to the task it came from.
		if _, err := strconv.ParseUint(row[7], 10, 64); err != nil {
			t.Fatalf("Task ID = %q, not a number", row[7])
		}
	}
	// Three tasks and the default cap: nothing was left behind, and the export says so.
	if got := w.Header().Get("X-Task-Export-Truncated"); got != "false" {
		t.Fatalf("X-Task-Export-Truncated = %q, want false", got)
	}
	// The filename carries a time, so two exports in one day do not overwrite each other.
	if cd := w.Header().Get("Content-Disposition"); !regexp.MustCompile(`forgec2_tasks_[0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{6}\.csv`).MatchString(cd) {
		t.Fatalf("Content-Disposition = %q, want a timestamped filename", cd)
	}
}

// TestTaskExportReportsTruncation covers the silent-truncation case: with a cap that cuts the answer,
// the response has to say so. A partial CSV is indistinguishable from a complete one otherwise.
func TestTaskExportReportsTruncation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.db.Create(&db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami",
			Status: "completed", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export?limit=1", nil)
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportTasks(c)

	if got := w.Header().Get("X-Task-Export-Truncated"); got != "true" {
		t.Fatalf("X-Task-Export-Truncated = %q, want true (limit=1 of 3 tasks)", got)
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want header + 1 row", len(records))
	}
}
