package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestEmergencyStatusCounts pins the two figures on the emergency panel. It is
// the screen an operator reads while deciding whether the fleet is really down,
// so a wrong number here is worse than an error: both counts now answer 500 on
// a database failure instead of falling through with zeros.
func TestEmergencyStatusCounts(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()

	agents := []db.Implant{
		{ID: "em-online", TenantID: 1, Hostname: "a", Status: "online", LastSeen: now},
		{ID: "em-stale", TenantID: 1, Hostname: "b", Status: "stale", LastSeen: now},
		{ID: "em-offline", TenantID: 1, Hostname: "c", Status: "offline", LastSeen: now},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	tasks := []db.Task{
		{AgentID: "em-online", TenantID: 1, Type: "kill", Status: "pending"},
		{AgentID: "em-stale", TenantID: 1, Type: "kill", Status: "running"},
		{AgentID: "em-offline", TenantID: 1, Type: "kill", Status: "completed"},
		{AgentID: "em-online", TenantID: 1, Type: "shell", Status: "pending"},
	}
	for i := range tasks {
		if err := s.db.Create(&tasks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/emergency/status", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleEmergencyStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		OnlineAgents int `json:"online_agents"`
		PendingKills int `json:"pending_kills"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.OnlineAgents != 2 {
		t.Fatalf("online_agents = %d, want 2 (online + stale, not offline)", out.OnlineAgents)
	}
	if out.PendingKills != 2 {
		t.Fatalf("pending_kills = %d, want 2 (pending + running kills only)", out.PendingKills)
	}
}
