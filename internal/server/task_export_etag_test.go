package server

import (
	"net/http"
	"net/http/httptest"
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
