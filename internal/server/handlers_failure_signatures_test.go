package server

import (
	"encoding/csv"
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
				Signature     string   `json:"signature"`
				Count         int      `json:"count"`
				Agents        int      `json:"agents_affected"`
				TopAgent      string   `json:"top_agent"`
				TopAgentCount int      `json:"top_agent_failures"`
				Reasons       []string `json:"reasons"`
				Sample        string   `json:"sample"`
			} `json:"task_signatures"`
			Rerun struct {
				Method string            `json:"method"`
				Path   string            `json:"path"`
				Body   map[string]string `json:"body"`
			} `json:"rerun"`
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
	// Distinct hosts travel with the shape: eleven rows from one beacon is a flaky link, eleven from eleven beacons is a bad
	// command, and the count alone cannot tell them apart.
	if top.Agents != 1 {
		t.Fatalf("agents_affected = %d, want the one seeded host", top.Agents)
	}
	// And WHICH host: "one host" only becomes actionable once it is named.
	if top.TopAgent != "a-1" || top.TopAgentCount != 3 {
		t.Fatalf("top agent = %q with %d failure(s), want a-1 with the three", top.TopAgent, top.TopAgentCount)
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

	// 2b. The answer says what to DO with a row: the signature text is the request body, so a client can wire the button
	// from the response without knowing the second endpoint exists.
	if resp.Data.Rerun.Method != "POST" || resp.Data.Rerun.Path != "/api/tasks/rerun-by-signature" {
		t.Fatalf("rerun hint = %+v", resp.Data.Rerun)
	}
	if resp.Data.Rerun.Body["since"] != "7d" {
		t.Fatalf("the hint does not carry the window the rows came from: %+v", resp.Data.Rerun.Body)
	}

	// 2c. The same scope the failure report takes: an exercise with no hosts selects nothing, an unusable value is a 400, and
	// the answer echoes the scope it applied.
	engagement := db.Engagement{TenantID: 1, Name: "DMZ", StartsAt: now.Add(-4 * time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet,
		"/api/stats/failures/signatures?since=7d&engagement="+itoaUint(engagement.ID), nil)
	c3.Set("user", "op")
	c3.Set("user_role", db.RoleAdmin)
	c3.Set("user_id", uint(1))
	c3.Set(middleware.TenantIDContextKey, uint(1))
	s.handleFailureSignatures(c3)
	if w3.Code != http.StatusOK {
		t.Fatalf("scoped signatures = %d (%s)", w3.Code, w3.Body.String())
	}
	var scoped struct {
		Data struct {
			Engagement     string `json:"engagement"`
			TaskFailures   int    `json:"task_failures"`
			TaskSignatures []struct {
				Count int `json:"count"`
			} `json:"task_signatures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &scoped); err != nil {
		t.Fatalf("decode scoped: %v", err)
	}
	if scoped.Data.Engagement != itoaUint(engagement.ID) {
		t.Fatalf("the answer does not echo the scope: %q", scoped.Data.Engagement)
	}
	// No host belongs to that exercise, so nothing is selected - which is different from "no filter".
	if scoped.Data.TaskFailures != 0 || len(scoped.Data.TaskSignatures) != 0 {
		t.Fatalf("a scoped report returned %d failure(s): %v", scoped.Data.TaskFailures, scoped.Data.TaskSignatures)
	}

	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	c4.Request = httptest.NewRequest(http.MethodGet, "/api/stats/failures/signatures?engagement=nope", nil)
	c4.Set("user", "op")
	c4.Set("user_role", db.RoleAdmin)
	c4.Set("user_id", uint(1))
	c4.Set(middleware.TenantIDContextKey, uint(1))
	s.handleFailureSignatures(c4)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("invalid engagement = %d, want 400", w4.Code)
	}

	// 2d. The shapes can be taken away as a file. Every other list here is exportable, and this is the analysis an operator
	// pastes into a review - the task half only, because the notification half is process state rather than a window.
	csvRecorder := httptest.NewRecorder()
	cCsv, _ := gin.CreateTestContext(csvRecorder)
	cCsv.Request = httptest.NewRequest(http.MethodGet, "/api/stats/failures/signatures?since=7d&format=csv", nil)
	cCsv.Set("user", "op")
	cCsv.Set("user_role", db.RoleAdmin)
	cCsv.Set("user_id", uint(1))
	cCsv.Set(middleware.TenantIDContextKey, uint(1))
	s.handleFailureSignatures(cCsv)
	if csvRecorder.Code != http.StatusOK {
		t.Fatalf("csv = %d (%s)", csvRecorder.Code, csvRecorder.Body.String())
	}
	if ct := csvRecorder.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	rows, err := csv.NewReader(strings.NewReader(csvRecorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("rows = %d, want a header and the seeded shape", len(rows))
	}
	if got := strings.Join(rows[0], ","); got != "Signature,Count,Agents,Top Agent,Top Agent Failures,Reasons,Example" {
		t.Fatalf("header = %q", got)
	}
	if rows[1][1] != "3" {
		t.Fatalf("the top shape count = %q, want the three permission failures", rows[1][1])
	}
	// The file says what it covers, like every other export: the window, and not the format (which is not a filter).
	// Both names: the specific one for an existing consumer, and the generic one because this is not a task export.
	if generic := csvRecorder.Header().Get(exportScopeHeader); generic == "" || generic != csvRecorder.Header().Get(taskExportScopeHeader) {
		t.Fatalf("generic scope = %q, specific = %q; they must agree",
			generic, csvRecorder.Header().Get(taskExportScopeHeader))
	}
	scope := csvRecorder.Header().Get(taskExportScopeHeader)
	if !strings.Contains(scope, "since=7d") || strings.Contains(scope, "format") {
		t.Fatalf("scope header = %q, want the window and not the format", scope)
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
