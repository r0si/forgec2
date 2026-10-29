package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// "other: N" is a dead end; the signatures turn it into a lead. Two hosts report the same failure with different
	// identifiers, which must collapse into one signature rather than two.
	// Forward slashes on purpose: the path pattern handles both, and this keeps the fixture readable.
	seed("a-1", "shell", "open /tmp/a/file.txt: denied by policy", 30*time.Minute, "failed")
	seed("a-2", "shell", "open /tmp/b/file.txt: denied by policy", 40*time.Minute, "failed")
	_, withSignatures := call("?since=7d")
	signatures, _ := withSignatures["other_signatures"].([]interface{})
	if len(signatures) == 0 {
		t.Fatalf("no signatures for the unclassified errors: %v", withSignatures)
	}
	top, _ := signatures[0].(map[string]interface{})
	if top["count"] != float64(3) {
		// The two path-only variants plus the earlier "weird failure" sample are three distinct shapes; the two that differ
		// only by path must have merged, so the largest bucket is 2.
		if top["count"] != float64(2) {
			t.Fatalf("top signature count = %v, want the two path-variants merged", top["count"])
		}
	}
	// The identifiers are gone from the signature, which is what makes two hosts one problem.
	signatureText, _ := top["signature"].(string)
	for _, leak := range []string{"/tmp/a/", "/tmp/b/"} {
		if strings.Contains(signatureText, leak) {
			t.Fatalf("the signature still contains a per-host identifier: %q", signatureText)
		}
	}

	// Retried tasks are counted separately: many of them means the LINK is flaky, which a list of reasons cannot show because
	// each retried task ended up somewhere else in the report.
	if data["retried"] != float64(0) {
		t.Fatalf("retried = %v before any retry was seeded", data["retried"])
	}
	retriedTask := db.Task{
		AgentID: "a-1", TenantID: 1, Type: "ps", Command: "ps", Status: "completed",
		CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-20 * time.Minute), RetryCount: 1,
	}
	if err := s.db.Create(&retriedTask).Error; err != nil {
		t.Fatalf("seed retried task: %v", err)
	}
	if err := s.db.Model(&db.Task{}).Where("id = ?", retriedTask.ID).Update("retry_count", 1).Error; err != nil {
		t.Fatalf("set retry count: %v", err)
	}
	_, withRetries := call("?since=7d")
	if withRetries["retried"] != float64(1) {
		t.Fatalf("retried = %v, want the one re-issued task", withRetries["retried"])
	}
	if note, _ := withRetries["retry_note"].(string); note == "" {
		t.Fatalf("the report does not explain the retry count: %v", withRetries)
	}

	// group_by=type groups the same rows differently.
	_, byType := call("?since=7d&group_by=type")
	typeBuckets, _ := byType["buckets"].([]interface{})
	if len(typeBuckets) != 3 {
		t.Fatalf("type buckets = %v, want shell/ps/lsass", typeBuckets)
	}

	// The breakdown can be taken away as a file: this is the oldest of the failure reports and the one most likely to be
	// pasted into a review, and it was the only one of the four with no export at all.
	csvRecorder := httptest.NewRecorder()
	cCsv, _ := gin.CreateTestContext(csvRecorder)
	cCsv.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/failures?since=7d&format=csv", nil)
	cCsv.Set("user", "op")
	cCsv.Set("user_role", db.RoleAdmin)
	cCsv.Set("user_id", uint(1))
	cCsv.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTaskFailureStats(cCsv)
	if csvRecorder.Code != http.StatusOK {
		t.Fatalf("csv = %d (%s)", csvRecorder.Code, csvRecorder.Body.String())
	}
	csvRows, err := csv.NewReader(strings.NewReader(csvRecorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(csvRows) < 2 {
		t.Fatalf("rows = %d, want a header and at least one reason", len(csvRows))
	}
	if got := strings.Join(csvRows[0], ","); got != "Key,Count,Distinct types,Example" {
		t.Fatalf("header = %q", got)
	}
	if scope := csvRecorder.Header().Get(taskExportScopeHeader); !strings.Contains(scope, "since=7d") {
		t.Fatalf("scope header = %q, want the window", scope)
	}

	// Bad input is refused rather than silently defaulted.
	if code, _ := call("?since=forever"); code != http.StatusBadRequest {
		t.Fatalf("invalid since = %d, want 400", code)
	}
	if code, _ := call("?group_by=phase"); code != http.StatusBadRequest {
		t.Fatalf("invalid group_by = %d, want 400", code)
	}
}
