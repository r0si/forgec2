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

// TestTaskOutputSupportsRange covers the resumable download: a large task result (a directory listing, a
// wevtutil dump) should be fetchable in pieces, and a client that already has the first half should not have to
// re-download it.
//
// The endpoint used to write the bytes directly, which means every request was all-or-nothing.
func TestTaskOutputSupportsRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	body := "0123456789abcdefghij"
	task := db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "dir", Status: "completed",
		Result: body, CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Minute),
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	call := func(rangeHeader, method string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/api/tasks/1/output", nil)
		if rangeHeader != "" {
			c.Request.Header.Set("Range", rangeHeader)
		}
		c.Params = gin.Params{{Key: "taskId", Value: itoaUint(task.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleDownloadTaskOutput(c)
		return w
	}

	// 1. A full request advertises that ranges are available and returns the whole body.
	full := call("", http.MethodGet, 1)
	if full.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", full.Code, full.Body.String())
	}
	if got := full.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := full.Body.String(); got != body {
		t.Fatalf("body = %q, want the whole result", got)
	}
	if ct := full.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q", ct)
	}

	// 2. A range returns 206 with Content-Range and exactly those bytes.
	partial := call("bytes=5-9", http.MethodGet, 1)
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206 (%s)", partial.Code, partial.Body.String())
	}
	if got := partial.Body.String(); got != "56789" {
		t.Fatalf("range body = %q, want 56789", got)
	}
	wantRange := "bytes 5-9/" + itoaUint(uint(len(body)))
	if got := partial.Header().Get("Content-Range"); got != wantRange {
		t.Fatalf("Content-Range = %q, want %q", got, wantRange)
	}

	// 3. An open-ended range works too: that is what a resuming client sends.
	tail := call("bytes=10-", http.MethodGet, 1)
	if tail.Code != http.StatusPartialContent || tail.Body.String() != "abcdefghij" {
		t.Fatalf("open range = %d %q", tail.Code, tail.Body.String())
	}

	// 4. A range past the end is refused with 416 rather than silently returning everything.
	if got := call("bytes=9999-10000", http.MethodGet, 1); got.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range = %d, want 416", got.Code)
	}

	// 5. HEAD reports the size without a body, which a client uses to plan the download.
	head := call("", http.MethodHead, 1)
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", head.Code)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d byte(s)", head.Body.Len())
	}
	if got := head.Header().Get("Content-Length"); got != itoaUint(uint(len(body))) {
		t.Fatalf("HEAD Content-Length = %q, want %d", got, len(body))
	}
}
