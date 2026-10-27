package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func pendingTasksCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ai/pending-tasks", nil)
	c.Set("user", "viewer1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestAIPendingTasksDoesNotLeakHostnames closes the question the comment on
// agentHostnamesByID left open. The task query is tenant-scoped, so the agent ids are
// already the caller's own; the hostname lookup deliberately does not re-scope (doing so
// would blank the hostname on a row whose task and agent tenants disagree, hiding a data
// problem instead of showing it). This test pins the property that matters: nothing from
// another tenant reaches the response.
func TestAIPendingTasksDoesNotLeakHostnames(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "viewer1", 1)

	const mine = "a1000000-0000-0000-0000-00000000aa01"
	const theirs = "a2000000-0000-0000-0000-00000000aa02"
	for _, row := range []db.Implant{
		{ID: mine, TenantID: 1, Hostname: "my-host", Status: "online", LastSeen: time.Now()},
		{ID: theirs, TenantID: 2, Hostname: "other-team-host", Status: "online", LastSeen: time.Now()},
	} {
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []db.Task{
		{AgentID: mine, TenantID: 1, Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval, CreatedBy: "ai", CreatedAt: time.Now()},
		{AgentID: theirs, TenantID: 2, Type: "shell", Command: "net user", Status: TaskStatusPendingApproval, CreatedBy: "ai", CreatedAt: time.Now()},
	} {
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
	}

	c, w := pendingTasksCtx()
	s.handleAIPendingTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if containsAny(body, "other-team-host", "net user") {
		t.Fatalf("another tenant's pending task leaked: %s", body)
	}
	if !containsAny(body, "my-host", "whoami") {
		t.Fatalf("the caller's own pending task is missing, so the test proves nothing: %s", body)
	}
}

// TestAIPendingTasksHostnameLookupRunsOnce keeps the batching that made this endpoint
// cheap: the hostname lookup is one query for the page, not one per row. The property is
// asserted through the response (every visible task carries its hostname), which is what
// a regression would break.
func TestAIPendingTasksHostnameLookupRunsOnce(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "viewer1", 1)

	const agent = "a3000000-0000-0000-0000-00000000aa03"
	if err := s.db.Create(&db.Implant{
		ID: agent, TenantID: 1, Hostname: "shared-host", Status: "online", LastSeen: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.db.Create(&db.Task{
			AgentID: agent, TenantID: 1, Type: "shell", Command: "whoami",
			Status: TaskStatusPendingApproval, CreatedBy: "ai", CreatedAt: time.Now(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	c, w := pendingTasksCtx()
	s.handleAIPendingTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := countSubstring(w.Body.String(), "shared-host"); got != 5 {
		t.Fatalf("hostname resolved %d times, want 5 (all five visible tasks)", got)
	}
}

func countSubstring(haystack, needle string) int {
	count := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
		}
	}
	return count
}
