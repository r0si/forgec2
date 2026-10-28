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

// TestTaskOutputIfRange covers the strong validator, which is what makes a RESUMED download safe.
//
// Ranges alone let a client fetch half a result and then ask for the rest. If the task was re-run in between, the two
// halves belong to different outputs and the spliced file is silently wrong - which is exactly what If-Range exists
// to prevent, and it only works with a strong ETag that changes when the bytes do.
func TestTaskOutputIfRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	body := "0123456789abcdefghij"
	now := time.Now().Truncate(time.Second)
	task := db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "dir", Status: "completed",
		Result: body, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	call := func(rangeHeader, ifRange string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/1/output", nil)
		if rangeHeader != "" {
			c.Request.Header.Set("Range", rangeHeader)
		}
		if ifRange != "" {
			c.Request.Header.Set("If-Range", ifRange)
		}
		c.Params = gin.Params{{Key: "taskId", Value: itoaUint(task.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleDownloadTaskOutput(c)
		return w
	}

	// 1. The tag is strong: quoting without a W/ prefix is what If-Range accepts.
	full := call("", "")
	etag := full.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the output download")
	}
	if strings.HasPrefix(etag, "W/") {
		t.Fatalf("the ETag is weak (%q); If-Range would ignore it", etag)
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("the ETag is not a quoted string: %q", etag)
	}

	// 2. A range WITH the current tag is served partially: the client is up to date.
	current := call("bytes=5-9", etag)
	if current.Code != http.StatusPartialContent || current.Body.String() != "56789" {
		t.Fatalf("range with the current tag = %d %q, want 206 56789", current.Code, current.Body.String())
	}

	// 3. A range with a STALE tag falls back to the whole document: splicing two versions would corrupt the file, so
	// the server refuses to serve a partial response instead.
	stale := call("bytes=5-9", `"0000-0-0"`)
	if stale.Code != http.StatusOK {
		t.Fatalf("range with a stale tag = %d, want 200 with the full body", stale.Code)
	}
	if stale.Body.String() != body {
		t.Fatalf("stale-tag response = %q, want the whole output", stale.Body.String())
	}

	// 4. Re-running the task changes the output and therefore the validator: an old tag can never resume onto the new
	// bytes.
	if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"result": "NEW-RESULT-CONTENT", "updated_at": now.Add(time.Minute),
	}).Error; err != nil {
		t.Fatalf("update task: %v", err)
	}
	after := call("", "")
	newETag := after.Header().Get("ETag")
	if newETag == etag {
		t.Fatalf("the ETag survived a result change: %q", newETag)
	}
	resumed := call("bytes=0-5", etag)
	if resumed.Code != http.StatusOK || resumed.Body.String() != "NEW-RESULT-CONTENT" {
		t.Fatalf("a stale tag resumed onto the new content: %d %q", resumed.Code, resumed.Body.String())
	}
}
