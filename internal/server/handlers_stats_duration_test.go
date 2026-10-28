package server

import (
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

// TestTaskDurationStats covers the new statistics endpoint: it answers "which task type is getting
// slow" from the task history, which nothing else in the product reported.
func TestTaskDurationStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	base := time.Now().Add(-2 * time.Hour)
	seed := func(agentID, taskType string, tenant uint, ms int, status string) {
		created := base.Add(time.Duration(ms) * time.Millisecond)
		task := db.Task{
			AgentID: agentID, TenantID: tenant, Type: taskType, Command: "x",
			Status: status, CreatedAt: base, UpdatedAt: created,
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed %s/%s: %v", taskType, status, err)
		}
	}

	// shell: 100, 200, 300, 1000 ms -> p50 200, p95 1000, mean 400.
	seed("a-1", "shell", 1, 100, "completed")
	seed("a-1", "shell", 1, 200, "completed")
	seed("a-1", "shell", 1, 300, "completed")
	seed("a-1", "shell", 1, 1000, "failed")
	// screenshot: one sample, so every percentile is that sample.
	seed("a-1", "screenshot", 1, 50, "completed")
	// Ignored: not final yet, and another tenant's rows.
	seed("a-1", "shell", 1, 9999, "running")
	seed("b-2", "shell", 2, 5000, "completed")

	call := func(query string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/duration"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleTaskDurationStats(c)
		return w
	}

	w := call("", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Sampled int `json:"sampled"`
			Types   []struct {
				Type        string  `json:"type"`
				Count       int     `json:"count"`
				P50Ms       float64 `json:"p50_ms"`
				P95Ms       float64 `json:"p95_ms"`
				MeanMs      float64 `json:"mean_ms"`
				MaxMs       float64 `json:"max_ms"`
				Completed   int     `json:"completed"`
				Failed      int     `json:"failed"`
				SuccessRate float64 `json:"success_rate"`
			} `json:"types"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	byType := map[string]struct {
		Count       int
		P50         float64
		P95         float64
		Mean        float64
		Max         float64
		Completed   int
		Failed      int
		SuccessRate float64
	}{}
	for _, row := range resp.Data.Types {
		byType[row.Type] = struct {
			Count       int
			P50         float64
			P95         float64
			Mean        float64
			Max         float64
			Completed   int
			Failed      int
			SuccessRate float64
		}{row.Count, row.P50Ms, row.P95Ms, row.MeanMs, row.MaxMs, row.Completed, row.Failed, row.SuccessRate}
	}

	shell, ok := byType["shell"]
	if !ok {
		t.Fatalf("no shell row: %s", w.Body.String())
	}
	if shell.Count != 4 {
		t.Fatalf("shell count = %d, want 4 (running and other-tenant rows excluded)", shell.Count)
	}
	if shell.P50 != 200 {
		t.Fatalf("shell p50 = %v, want 200", shell.P50)
	}
	if shell.P95 != 1000 {
		t.Fatalf("shell p95 = %v, want 1000", shell.P95)
	}
	if shell.Mean != 400 {
		t.Fatalf("shell mean = %v, want 400", shell.Mean)
	}
	if shell.Max != 1000 {
		t.Fatalf("shell max = %v, want 1000", shell.Max)
	}
	// Outcome counts come from the same rows: 3 completed + 1 failed -> 0.75.
	if shell.Completed != 3 || shell.Failed != 1 {
		t.Fatalf("shell outcomes = %d completed / %d failed, want 3/1", shell.Completed, shell.Failed)
	}
	if shell.SuccessRate != 0.75 {
		t.Fatalf("shell success rate = %v, want 0.75", shell.SuccessRate)
	}
	// Sorted by p95 descending: shell (1000) before screenshot (50).
	if len(resp.Data.Types) < 2 || resp.Data.Types[0].Type != "shell" {
		t.Fatalf("types are not ordered by p95 desc: %+v", resp.Data.Types)
	}

	// A tenant sees only its own rows; tenant 2's 5000 ms sample must not appear.
	if got := call("", 2); got.Code != http.StatusOK {
		t.Fatalf("tenant 2 status = %d", got.Code)
	} else if body := got.Body.String(); !strings.Contains(body, `"count":1`) && strings.Contains(body, `"max_ms":5000`) {
		t.Fatalf("tenant 2 did not get its own row: %s", body)
	}

	// The type filter narrows the answer, and a bad window is a 400.
	if body := call("?type=shell", 1).Body.String(); strings.Contains(body, "screenshot") {
		t.Fatalf("type filter returned other types: %s", body)
	}
	if got := call("?since=forever", 1); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid since = %d, want 400", got.Code)
	}
	if got := call("?since=7d", 1); got.Code != http.StatusOK {
		t.Fatalf("7d window = %d", got.Code)
	}
}

// TestTaskDurationStatsByAgent covers group_by=agent: "which beacon is slow" is a different question
// from "which command is slow", and it needs the hostname so the answer is readable.
func TestTaskDurationStatsByAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	if err := s.db.Create(&db.Implant{ID: "a-1", Hostname: "web-01", TenantID: 1}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := s.db.Create(&db.Implant{ID: "b-2", Hostname: "dc-02", TenantID: 1}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	base := time.Now().Add(-time.Hour)
	seed := func(agentID string, ms int) {
		task := db.Task{
			AgentID: agentID, TenantID: 1, Type: "shell", Command: "x", Status: "completed",
			CreatedAt: base, UpdatedAt: base.Add(time.Duration(ms) * time.Millisecond),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}
	seed("a-1", 100)
	seed("a-1", 300)
	seed("b-2", 2000)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/duration?group_by=agent", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTaskDurationStats(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			GroupBy string `json:"group_by"`
			Agents  []struct {
				Type     string  `json:"type"`
				Hostname string  `json:"hostname"`
				Count    int     `json:"count"`
				P95Ms    float64 `json:"p95_ms"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Data.GroupBy != "agent" {
		t.Fatalf("group_by = %q", resp.Data.GroupBy)
	}
	if len(resp.Data.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (%s)", len(resp.Data.Agents), w.Body.String())
	}
	// Slowest first, and each row carries its hostname.
	if resp.Data.Agents[0].Type != "b-2" || resp.Data.Agents[0].Hostname != "dc-02" {
		t.Fatalf("first agent = %+v, want b-2/dc-02", resp.Data.Agents[0])
	}
	if resp.Data.Agents[0].P95Ms != 2000 {
		t.Fatalf("b-2 p95 = %v, want 2000", resp.Data.Agents[0].P95Ms)
	}
	if resp.Data.Agents[1].Count != 2 || resp.Data.Agents[1].Hostname != "web-01" {
		t.Fatalf("second agent = %+v", resp.Data.Agents[1])
	}

	// An unknown grouping is rejected rather than silently defaulting.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/duration?group_by=tenant", nil)
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTaskDurationStats(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("group_by=tenant = %d, want 400", w2.Code)
	}
}

// TestTaskDurationBuckets covers ?bucket=hour: the per-type rows say WHAT is slow, the buckets say WHEN
// it got slow - the question after someone notices a regression.
func TestTaskDurationBuckets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// Two tasks in one hour (one failed) and one in the previous hour.
	hourStart := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	seed := func(created time.Time, ms int, status string) {
		task := db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "x", Status: status,
			CreatedAt: created, UpdatedAt: created.Add(time.Duration(ms) * time.Millisecond),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed(hourStart, 100, "completed")
	seed(hourStart.Add(10*time.Minute), 300, "failed")
	seed(hourStart.Add(-30*time.Minute), 1000, "completed") // previous hour

	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/duration"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleTaskDurationStats(c)
		return w
	}

	w := call("?since=7d&bucket=hour")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Bucket  string `json:"bucket"`
			Buckets []struct {
				Start       string  `json:"start"`
				Count       int     `json:"count"`
				P95Ms       float64 `json:"p95_ms"`
				MaxMs       float64 `json:"max_ms"`
				Failed      int     `json:"failed"`
				SuccessRate float64 `json:"success_rate"`
			} `json:"buckets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Data.Bucket != "hour" {
		t.Fatalf("bucket = %q", resp.Data.Bucket)
	}
	if len(resp.Data.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2 (%s)", len(resp.Data.Buckets), w.Body.String())
	}
	// Oldest first.
	if resp.Data.Buckets[0].Count != 1 || resp.Data.Buckets[0].MaxMs != 1000 {
		t.Fatalf("first bucket = %+v", resp.Data.Buckets[0])
	}
	// The newer hour has the 100/300 pair, one of them failed.
	newer := resp.Data.Buckets[1]
	if newer.Count != 2 || newer.Failed != 1 || newer.SuccessRate != 0.5 {
		t.Fatalf("newer bucket = %+v, want count 2 / failed 1 / rate 0.5", newer)
	}
	if newer.MaxMs != 300 {
		t.Fatalf("newer bucket max = %v, want 300", newer.MaxMs)
	}

	// Day granularity collapses both into one bucket.
	var dayResp struct {
		Data struct {
			Buckets []struct {
				Count int `json:"count"`
			} `json:"buckets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(call("?since=7d&bucket=day").Body.Bytes(), &dayResp); err != nil {
		t.Fatalf("decode day: %v", err)
	}
	if len(dayResp.Data.Buckets) != 1 || dayResp.Data.Buckets[0].Count != 3 {
		t.Fatalf("day buckets = %+v, want one bucket of 3", dayResp.Data.Buckets)
	}

	// No bucket parameter means no buckets field at all.
	if body := call("?since=7d").Body.String(); strings.Contains(body, `"buckets"`) {
		t.Fatalf("buckets were returned without being asked for: %s", body)
	}

	// An unknown granularity is rejected.
	if got := call("?bucket=week"); got.Code != http.StatusBadRequest {
		t.Fatalf("bucket=week = %d, want 400", got.Code)
	}
}
