package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestDownloadTaskOutput covers the plain-text output endpoint: a file is what an operator wants for a
// large result, and the file has to keep the failure with the output rather than silently omitting it.
func TestDownloadTaskOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	seed := func(agentID string, tenant uint, result, errText, status string) db.Task {
		task := db.Task{
			AgentID: agentID, TenantID: tenant, Type: "shell", Command: "dir",
			Status: status, Result: result, Error: errText, CreatedAt: time.Now(),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		return task
	}

	ok := seed("a-1", 1, "Volume in drive C has no label.\n", "", "completed")
	failed := seed("a-1", 1, "", "access denied", "failed")
	empty := seed("a-1", 1, "", "", "pending")
	foreign := seed("b-2", 2, "secret output", "", "completed")

	call := func(taskID uint, agentParam string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		target := "/api/tasks/" + strconv.FormatUint(uint64(taskID), 10) + "/output"
		c.Request = httptest.NewRequest(http.MethodGet, target, nil)
		c.Params = gin.Params{{Key: "taskId", Value: strconv.FormatUint(uint64(taskID), 10)}}
		if agentParam != "" {
			c.Params = append(c.Params, gin.Param{Key: "id", Value: agentParam})
		}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleDownloadTaskOutput(c)
		return w
	}

	// A completed task downloads its output with the status and finality in headers.
	w := call(ok.ID, "", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content type = %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "Volume in drive C") {
		t.Fatalf("body = %q", body)
	}
	if w.Header().Get("X-Task-Status") != "completed" || w.Header().Get("X-Task-Final") != "true" {
		t.Fatalf("headers = %v", w.Header())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "task-"+strconv.FormatUint(uint64(ok.ID), 10)+"-shell.txt") {
		t.Fatalf("filename = %q", cd)
	}

	// A failed task still downloads, and the error travels with the (empty) output.
	w2 := call(failed.ID, "", 1)
	if w2.Code != http.StatusOK {
		t.Fatalf("failed task status = %d", w2.Code)
	}
	body := w2.Body.String()
	if !strings.Contains(body, "--- error ---") || !strings.Contains(body, "access denied") {
		t.Fatalf("the failure was dropped from the download: %q", body)
	}
	if w2.Header().Get("X-Task-Final") != "false" {
		t.Fatalf("a failed task was reported final: %q", w2.Header().Get("X-Task-Final"))
	}

	// Nothing to download is a 404 with a reason, not an empty file.
	if got := call(empty.ID, "", 1); got.Code != http.StatusNotFound {
		t.Fatalf("empty output = %d, want 404", got.Code)
	}

	// Another tenant's output is not reachable, and neither is the wrong agent's.
	if got := call(foreign.ID, "", 1); got.Code != http.StatusNotFound {
		t.Fatalf("foreign task output = %d, want 404", got.Code)
	}
	if got := call(ok.ID, "other-agent", 1); got.Code != http.StatusNotFound {
		t.Fatalf("wrong agent = %d, want 404", got.Code)
	}

	// A non-numeric id is rejected before any query.
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/abc/output", nil)
	c3.Params = gin.Params{{Key: "taskId", Value: "abc"}}
	c3.Set("user", "op")
	c3.Set("user_role", db.RoleAdmin)
	c3.Set("user_id", uint(1))
	c3.Set(middleware.TenantIDContextKey, uint(1))
	s.handleDownloadTaskOutput(c3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id = %d, want 400", w3.Code)
	}
}
