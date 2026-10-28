package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestClassifyFailure pins the reason codes.
//
// The order of the rules is load-bearing: "access is denied" must not be classified as a generic network error, and a
// deadline must not become "other". The table is the test.
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"context deadline exceeded while running", "timeout"},
		{"The operation timed out", "timeout"},
		{"Access is denied.", "access_denied"},
		{"open /etc/shadow: permission denied", "access_denied"},
		{"Threat detected and quarantined by Defender", "defender"},
		{"Error: the system cannot find the file specified. not found", "not_found"},
		{"dial tcp 10.0.0.5:445: connect: connection refused", "network"},
		{"lookup internal.corp: no such host", "dns"},
		{"x509: certificate signed by unknown authority", "tls"},
		{"unknown command: frobnicate", "unsupported"},
		{"write /tmp/x: no space left on device", "disk"},
		{"task cancelled by operator", "cancelled"},
		{"something nobody has seen before", "other"},
		{"", "other"},
	}
	for _, test := range cases {
		if got := classifyFailure(test.text); got != test.want {
			t.Fatalf("classify(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}

// TestTaskFailureStats covers the report itself.
func TestTaskFailureStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	seed := func(agentID, taskType, errText string, age time.Duration, status string) {
		if err := s.db.Create(&db.Task{
			AgentID: agentID, TenantID: 1, Type: taskType, Command: "x", Status: status, Error: errText,
			CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Three timeouts, one denial, one unmatched, one success that must not be counted.
	seed("a-1", "shell", "context deadline exceeded", time.Hour, "failed")
	seed("a-1", "shell", "operation timed out", 2*time.Hour, "failed")
	seed("a-2", "ps", "task timed out after 30s", 3*time.Hour, "failed")
	seed("a-2", "ps", "Access is denied.", 4*time.Hour, "failed")
	seed("a-1", "lsass", "weird failure", 5*time.Hour, "failed")
	seed("a-1", "shell", "", time.Hour, "completed")
	// Outside the window: must not be counted.
	seed("a-1", "shell", "context deadline exceeded", 30*24*time.Hour, "failed")

	call := func(query string) (int, map[string]interface{}) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/failures"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleTaskFailureStats(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	code, data := call("?since=7d")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["failed"] != float64(5) {
		t.Fatalf("failed = %v, want the five in-window failures", data["failed"])
	}
	buckets, _ := data["buckets"].([]interface{})
	if len(buckets) == 0 {
		t.Fatalf("no buckets: %v", data)
	}
	first, _ := buckets[0].(map[string]interface{})
	if first["key"] != "timeout" || first["count"] != float64(3) {
		t.Fatalf("the timeout bucket should be first and largest: %v", first)
	}
	// The sample travels so an operator can see what an "other" means without opening the task.
	for _, raw := range buckets {
		entry, _ := raw.(map[string]interface{})
		if entry["key"] == "other" && entry["sample"] == "" {
			t.Fatalf("the 'other' bucket carries no example: %v", entry)
		}
	}

	// group_by=engagement answers the after-action question: which exercise accounted for the failures. Agents outside an
	// exercise are bucketed as "none" rather than dropped, because that is itself an answer worth seeing.
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "a-2").Update("engagement_id", engagement.ID).Error; err != nil {
		t.Fatalf("assign agent: %v", err)
	}
	for _, id := range []string{"a-1", "a-2"} {
		if err := s.db.Create(&db.Implant{
			ID: id, TenantID: 1, Hostname: id + "-host", Status: "online", LastSeen: now,
		}).Error; err != nil && !containsFold(err.Error(), "unique") {
			t.Fatalf("seed agent %s: %v", id, err)
		}
	}
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "a-2").Update("engagement_id", engagement.ID).Error; err != nil {
		t.Fatalf("assign agent again: %v", err)
	}
	_, byEngagement := call("?since=7d&group_by=engagement")
	bucketsByEngagement, _ := byEngagement["buckets"].([]interface{})
	seenEngagement, seenNone := false, false
	for _, raw := range bucketsByEngagement {
		entry, _ := raw.(map[string]interface{})
		switch entry["key"] {
		case "DMZ exercise":
			seenEngagement = true
		case "none":
			seenNone = true
		}
	}
	if !seenEngagement || !seenNone {
		t.Fatalf("engagement buckets = %v, want the exercise and the unattributed hosts", bucketsByEngagement)
	}

	// group_by=type groups the same rows differently.
	_, byType := call("?since=7d&group_by=type")
	typeBuckets, _ := byType["buckets"].([]interface{})
	if len(typeBuckets) != 3 {
		t.Fatalf("type buckets = %v, want shell/ps/lsass", typeBuckets)
	}

	// Bad input is refused rather than silently defaulted.
	if code, _ := call("?since=forever"); code != http.StatusBadRequest {
		t.Fatalf("invalid since = %d, want 400", code)
	}
	if code, _ := call("?group_by=phase"); code != http.StatusBadRequest {
		t.Fatalf("invalid group_by = %d, want 400", code)
	}
}
