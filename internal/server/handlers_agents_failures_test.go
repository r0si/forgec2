package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// agentListResponse is the fleet list payload the console reads. unmarshalData
// unwraps the envelope, so the fields map to the payload's keys.
type agentListResponse struct {
	Agents []struct {
		ID          string `json:"id"`
		Hostname    string `json:"hostname"`
		Failures24h int64  `json:"failures_24h"`
	} `json:"agents"`
	Total int64 `json:"total"`
}

// TestAgentListSortsByRecentFailures proves the fleet list can be ordered by
// the beacons that keep failing - the triage question the dashboard's tile
// asks but could not answer.
//
// The counts come from one bounded grouped query joined into the list, so the
// pagination stays in SQL and the number in the row is the number it was
// sorted by.
func TestAgentListSortsByRecentFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newAgentTestServer(t)

	now := time.Now()
	agents := []db.Implant{
		{ID: "quiet", Hostname: "QUIET01", IP: "10.0.0.1", LastSeen: now},
		{ID: "broken", Hostname: "BROKEN01", IP: "10.0.0.2", LastSeen: now},
		{ID: "older", Hostname: "OLDER01", IP: "10.0.0.3", LastSeen: now},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}

	// Two recent failures on one beacon, one on another, and a failure that
	// fell out of the 24h window.
	rows := []db.AuditLog{
		{User: "op", Action: "command_result", Resource: "task", AgentID: "broken", Success: false, CreatedAt: now.Add(-2 * time.Hour)},
		{User: "op", Action: "command_result", Resource: "task", AgentID: "broken", Success: false, CreatedAt: now.Add(-3 * time.Hour)},
		{User: "op", Action: "command_result", Resource: "task", AgentID: "quiet", Success: false, CreatedAt: now.Add(-time.Hour)},
		// Outside the window: it must not count.
		{User: "op", Action: "command_result", Resource: "task", AgentID: "older", Success: false, CreatedAt: now.Add(-40 * time.Hour)},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}

	call := func(query string) agentListResponse {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents"+query, nil)
		c.Set("user", "root")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		s.handleListAgents(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var resp agentListResponse
		unmarshalData(t, w.Body.Bytes(), &resp)
		return resp
	}

	sorted := call("?sort_key=failures&sort_dir=desc")
	if len(sorted.Agents) != 3 {
		t.Fatalf("agents = %d, want 3: %+v", len(sorted.Agents), sorted.Agents)
	}
	if sorted.Agents[0].ID != "broken" {
		t.Fatalf("first agent = %q, want the beacon with the most failures: %+v", sorted.Agents[0].ID, sorted.Agents)
	}
	if sorted.Agents[0].Failures24h != 2 {
		t.Fatalf("first agent failures = %d, want 2", sorted.Agents[0].Failures24h)
	}
	// The failure outside the window must not count, and the row still has to
	// be listed.
	for _, a := range sorted.Agents {
		if a.ID == "older" && a.Failures24h != 0 {
			t.Fatalf("a failure outside the 24h window counted: %+v", a)
		}
	}

	// The count is published for the page when the console asks for it,
	// without changing the order.
	counted := call("?failures=1")
	counts := map[string]int64{}
	for _, a := range counted.Agents {
		counts[a.ID] = a.Failures24h
	}
	if counts["broken"] != 2 || counts["quiet"] != 1 || counts["older"] != 0 {
		t.Fatalf("failure counts = %+v, want broken=2 quiet=1 older=0", counts)
	}

	// Ascending is the other direction of the same sort.
	asc := call("?sort_key=failures&sort_dir=asc")
	if len(asc.Agents) != 3 {
		t.Fatalf("asc agents = %d, want 3", len(asc.Agents))
	}
	if asc.Agents[0].Failures24h != 0 {
		t.Fatalf("asc first agent failures = %d, want 0 (fewest failures first)", asc.Agents[0].Failures24h)
	}
}

// TestAgentListGroupsByHostSortsByFailures proves the grouped view sorts by
// the failures of the whole host, not of one session.
func TestAgentListGroupsByHostSortsByFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newAgentTestServer(t)

	now := time.Now()
	agents := []db.Implant{
		{ID: "s1", Hostname: "HOST-A", IP: "10.0.0.1", LastSeen: now},
		{ID: "s2", Hostname: "HOST-B", IP: "10.0.0.2", LastSeen: now},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	rows := []db.AuditLog{
		{User: "op", Action: "command_result", Resource: "task", AgentID: "s1", Success: false, CreatedAt: now.Add(-time.Hour)},
		{User: "op", Action: "command_result", Resource: "task", AgentID: "s2", Success: false, CreatedAt: now.Add(-time.Hour)},
		{User: "op", Action: "command_result", Resource: "task", AgentID: "s2", Success: false, CreatedAt: now.Add(-2 * time.Hour)},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents?group=host&sort_key=failures&sort_dir=desc", nil)
	c.Set("user", "root")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleListAgents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp agentListResponse
	unmarshalData(t, w.Body.Bytes(), &resp)
	if len(resp.Agents) == 0 {
		t.Fatal("no agents in the grouped list")
	}
	// HOST-B carries two failures, HOST-A one: the host with more comes first.
	if resp.Agents[0].Hostname != "HOST-B" {
		t.Fatalf("first host = %q, want HOST-B (two failures): %+v", resp.Agents[0].Hostname, resp.Agents)
	}
}
