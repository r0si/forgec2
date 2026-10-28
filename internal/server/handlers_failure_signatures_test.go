package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestFailureSignatures covers the merged view: what SHAPE of failure dominates, across tasks and notifications.
//
// The point is the difference between four separate 401s and one expired token. Four failures that differ only by identifiers
// must read as one row, and a route failure must use the same normalisation so a signature means the same thing in both halves.
func TestFailureSignatures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetObservabilityWarnings()
	t.Cleanup(func() {
		resetNotificationStats()
		resetObservabilityWarnings()
	})

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	seed := func(taskType, errText string, age time.Duration) {
		if err := s.db.Create(&db.Task{
			AgentID: "a-1", TenantID: 1, Type: taskType, Command: "x", Status: "failed", Error: errText,
			CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Three failures of the same shape, differing only by a path; one of a different shape.
	seed("files_ls", "open /tmp/a/x.txt: permission denied", time.Hour)
	seed("files_ls", "open /tmp/b/y.txt: permission denied", 2*time.Hour)
	seed("ls", "open /var/c/z.txt: permission denied", 3*time.Hour)
	seed("ps", "context deadline exceeded", 4*time.Hour)

	// Two routes failing for the same reason, differing by webhook id.
	recordNotificationDelivery(11, errors.New(`Post "https://discord.com/api/webhooks/111/aaa": 401 Unauthorized`))
	recordNotificationDelivery(12, errors.New(`Post "https://discord.com/api/webhooks/222/bbb": 401 Unauthorized`))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/failures/signatures?since=7d", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleFailureSignatures(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			TaskFailures   int `json:"task_failures"`
			TaskSignatures []struct {
				Signature string   `json:"signature"`
				Count     int      `json:"count"`
				Reasons   []string `json:"reasons"`
				Sample    string   `json:"sample"`
			} `json:"task_signatures"`
			NotificationSignatures []struct {
				Signature string   `json:"signature"`
				Routes    []string `json:"routes"`
			} `json:"notification_signatures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 1. Three permission failures collapse into one row, which is the largest.
	if resp.Data.TaskFailures != 4 {
		t.Fatalf("task_failures = %d, want the four in-window failures", resp.Data.TaskFailures)
	}
	if len(resp.Data.TaskSignatures) != 2 {
		t.Fatalf("signatures = %v, want two shapes", resp.Data.TaskSignatures)
	}
	top := resp.Data.TaskSignatures[0]
	if top.Count != 3 || !strings.Contains(top.Signature, "permission denied") {
		t.Fatalf("top signature = %+v, want the three permission failures", top)
	}
	// The identifiers are gone, or the row would not be a shape.
	for _, leak := range []string{"/tmp/a/", "/tmp/b/", "/var/c/"} {
		if strings.Contains(top.Signature, leak) {
			t.Fatalf("the signature still carries %q: %q", leak, top.Signature)
		}
	}
	// The reason codes travel with the shape, so a reader can see it is an access problem.
	found := false
	for _, reason := range top.Reasons {
		if reason == "access_denied" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the top signature carries no reason code: %v", top.Reasons)
	}
	if top.Sample == "" {
		t.Fatal("the top signature carries no example")
	}

	// 2. The two notification failures share one signature and name both routes.
	if len(resp.Data.NotificationSignatures) != 1 {
		t.Fatalf("notification signatures = %v, want one", resp.Data.NotificationSignatures)
	}
	notification := resp.Data.NotificationSignatures[0]
	if len(notification.Routes) != 2 {
		t.Fatalf("the shapes were not merged across routes: %+v", notification)
	}
	for _, leak := range []string{"111", "222", "aaa", "bbb"} {
		if strings.Contains(notification.Signature, leak) {
			t.Fatalf("the notification signature carries %q: %q", leak, notification.Signature)
		}
	}

	// 3. A bad window is refused rather than defaulted.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/stats/failures/signatures?since=forever", nil)
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handleFailureSignatures(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("invalid since = %d, want 400", w2.Code)
	}
}
