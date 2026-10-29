package server

import (
	"encoding/json"
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

// TestTaskQueueStats covers the per-beacon backlog view.
//
// The question it answers is not "how many tasks failed" but "which host is not consuming work", and that is a QUEUE question. A
// stalled queue means the beacon is not polling - a different problem from a slow command, needing a different response - so the
// stall test compares the age of the OLDEST pending task against the beacon's own sleep plus a grace period, not against a count.
func TestTaskQueueStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	// One beacon that sleeps 10s and has a fresh queue; one that sleeps 10s but has a task pending for an hour.
	for _, agent := range []db.Implant{
		{ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, CurrentInterval: 10},
		{ID: "a-2", TenantID: 1, Hostname: "db-02", Status: "online", LastSeen: now, CurrentInterval: 10},
	} {
		agent := agent
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	seed := func(agentID, status string, age time.Duration) {
		task := db.Task{
			AgentID: agentID, TenantID: 1, Type: "ps", Command: "ps", Status: status,
			CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}
	seed("a-1", "pending", 5*time.Second)
	seed("a-1", "running", 3*time.Second)
	seed("a-2", "pending", time.Hour)
	// A second pending row for a-2, so the two beacons carry the SAME queued count and the age has to break the tie.
	seed("a-2", "pending", 10*time.Second)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/queue", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTaskQueueStats(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Queued         int `json:"queued"`
			StalledAgents  int `json:"stalled_agents"`
			StallGraceSecs int `json:"stall_grace_seconds"`
			Agents         []struct {
				AgentID       string `json:"agent_id"`
				Hostname      string `json:"hostname"`
				Pending       int    `json:"pending"`
				Running       int    `json:"running"`
				OldestPending int64  `json:"oldest_pending_seconds"`
				Stalled       bool   `json:"stalled"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 1. Both beacons are represented, with their counts.
	if len(resp.Data.Agents) != 2 {
		t.Fatalf("agents = %+v, want both beacons", resp.Data.Agents)
	}
	if resp.Data.Queued != 4 {
		t.Fatalf("queued = %d, want the four pending/running tasks", resp.Data.Queued)
	}
	if resp.Data.StallGraceSecs != 120 {
		t.Fatalf("stall grace = %d, want the documented two minutes", resp.Data.StallGraceSecs)
	}

	byAgent := map[string]bool{}
	for _, agent := range resp.Data.Agents {
		byAgent[agent.AgentID] = agent.Stalled
		if agent.Hostname == "" {
			t.Fatalf("%s has no hostname; an id alone is not actionable", agent.AgentID)
		}
	}
	// 2. Exactly the beacon with the hour-old pending task is stalled.
	if !byAgent["a-2"] {
		t.Fatalf("the beacon with an hour-old pending task is not stalled: %+v", resp.Data.Agents)
	}
	if byAgent["a-1"] {
		t.Fatalf("a beacon with a five-second-old queue was called stalled: %+v", resp.Data.Agents)
	}
	if resp.Data.StalledAgents != 1 {
		t.Fatalf("stalled_agents = %d, want 1", resp.Data.StalledAgents)
	}

	// 2b. The same exercise scope every other report takes: an exercise with no hosts answers zero, an unusable value is a 400,
	// and the answer echoes the scope it applied.
	engagement := db.Engagement{TenantID: 1, Name: "DMZ", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	scopedRecorder := httptest.NewRecorder()
	cScoped, _ := gin.CreateTestContext(scopedRecorder)
	cScoped.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/queue?engagement="+strconv.FormatUint(uint64(engagement.ID), 10), nil)
	cScoped.Set("user", "op")
	cScoped.Set("user_role", db.RoleAdmin)
	cScoped.Set("user_id", uint(1))
	cScoped.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTaskQueueStats(cScoped)
	if scopedRecorder.Code != http.StatusOK {
		t.Fatalf("scoped = %d (%s)", scopedRecorder.Code, scopedRecorder.Body.String())
	}
	var scoped struct {
		Data struct {
			Engagement string `json:"engagement"`
			Queued     int    `json:"queued"`
		} `json:"data"`
	}
	if err := json.Unmarshal(scopedRecorder.Body.Bytes(), &scoped); err != nil {
		t.Fatalf("decode scoped: %v", err)
	}
	if scoped.Data.Engagement == "" || scoped.Data.Queued != 0 {
		t.Fatalf("scoped queue = %+v, want the scope echoed and no work for an exercise with no hosts", scoped.Data)
	}
	badRecorder := httptest.NewRecorder()
	cBad, _ := gin.CreateTestContext(badRecorder)
	cBad.Request = httptest.NewRequest(http.MethodGet, "/api/stats/tasks/queue?engagement=nope", nil)
	cBad.Set("user", "op")
	cBad.Set("user_role", db.RoleAdmin)
	cBad.Set("user_id", uint(1))
	cBad.Set(middleware.TenantIDContextKey, uint(1))
	s.handleTaskQueueStats(cBad)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid engagement = %d, want 400", badRecorder.Code)
	}

	// 3. With equal queued counts the tie-break is age, so the beacon holding the hour-old row sorts above.
	if resp.Data.Agents[0].AgentID != "a-2" {
		t.Fatalf("ordering = %s first, want the oldest queue", resp.Data.Agents[0].AgentID)
	}
}

// TestReadyReportsAStalledQueue closes the loop between the queue report and the probe.
//
// The probe is what runs when nobody is watching the console, which is exactly when a beacon stops polling: work keeps being
// accepted and nothing says so. The warning must use the same rule as the detail page, and a probe that cannot count must say
// "unknown" rather than reporting a reassuring zero.
func TestReadyReportsAStalledQueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	resetNotificationStats()
	resetRuleActionStats()
	resetAutoRetryCounter()
	t.Cleanup(func() {
		resetNotificationStats()
		resetRuleActionStats()
		resetAutoRetryCounter()
	})

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-stalled", TenantID: 1, Hostname: "db-02", Status: "online", LastSeen: now, CurrentInterval: 10,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := s.db.Create(&db.Task{
		AgentID: "a-stalled", TenantID: 1, Type: "ps", Command: "ps", Status: "pending",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleReadyCheck(c)

	// checks is not homogeneous: `listeners` is a count, everything else is prose.
	var payload struct {
		Checks   map[string]interface{} `json:"checks"`
		Warnings []string               `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	queueCheck, _ := payload.Checks["task_queue"].(string)
	if !strings.Contains(queueCheck, "1 beacon(s) not draining") {
		t.Fatalf("task_queue check = %q, want the stalled beacon counted", queueCheck)
	}
	found := false
	for _, warning := range payload.Warnings {
		if strings.Contains(warning, "not consuming") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want one naming the stalled beacon", payload.Warnings)
	}
	// The probe warns; it does not become unready - a beacon that is not polling is a feature failing, not this process.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want ready with a warning", w.Code)
	}
}
