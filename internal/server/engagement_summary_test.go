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

// TestEngagementSummary covers GET /api/engagements/:id/summary: "what did this exercise actually do".
//
// The window rule is the part worth pinning: counting past the effective end would attribute the next
// engagement's work to this one, which is the same mistake the deadline enforcement avoids by merging the
// two bounds.
func TestEngagementSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	engagement := db.Engagement{
		TenantID: 1, Name: "wrapped", StartsAt: now.Add(-2 * day), EndsAt: now.Add(-day),
		ClosedAt: ptrTime(now.Add(-day)),
	}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	other := db.Engagement{TenantID: 2, Name: "foreign", StartsAt: now.Add(-2 * day), EndsAt: now}
	if err := s.db.Create(&other).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}

	seedAgent := func(id string, tenant uint, engagementID *uint, lastSeen time.Time, expires *time.Time) {
		if err := s.db.Create(&db.Implant{
			ID: id, TenantID: tenant, Hostname: id, Status: "online", LastSeen: lastSeen,
			EngagementID: engagementID, ExpiresAt: expires,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Derived from the server threshold rather than hard-coded: the definition of "online" belongs to
	// the server, and a fixture that guesses it becomes a false failure (which is exactly what happened
	// with a one-minute guess).
	live := now.Add(-s.offlineThreshold() / 2)
	expired := now.Add(-time.Hour)
	seedAgent("inside-a", 1, &engagement.ID, live, &expired)
	seedAgent("inside-b", 1, &engagement.ID, now.Add(-2*s.offlineThreshold()), nil) // offline
	seedAgent("outside", 1, nil, live, nil)
	seedAgent("foreign", 2, &other.ID, live, nil)

	seedTask := func(agentID string, status string, created time.Time) {
		if err := s.db.Create(&db.Task{
			AgentID: agentID, Type: "shell", Command: "whoami", Status: status,
			CreatedAt: created, UpdatedAt: created,
		}).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}
	// Inside the window.
	seedTask("inside-a", "completed", now.Add(-36*time.Hour))
	seedTask("inside-a", "failed", now.Add(-30*time.Hour))
	seedTask("inside-b", "pending", now.Add(-28*time.Hour))
	// Before the window opened: it belongs to whatever came before.
	seedTask("inside-a", "completed", now.Add(-5*day))
	// Another engagement's agent: must not be counted here.
	seedTask("outside", "completed", now.Add(-30*time.Hour))

	seedAudit := func(action string, tenant uint, created time.Time) {
		if err := s.db.Create(&db.AuditLog{
			Action: action, User: "op", TenantID: tenant, Success: true,
			CreatedAt: created, PrevHash: "p", EntryHash: "e",
		}).Error; err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}
	seedAudit("task_create", 1, now.Add(-36*time.Hour))
	seedAudit("task_create", 1, now.Add(-35*time.Hour))
	seedAudit("login", 1, now.Add(-34*time.Hour))
	seedAudit("task_create", 1, now.Add(-5*day))        // outside
	seedAudit("task_create", 2, now.Add(-36*time.Hour)) // other tenant

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/summary", nil)
	c.Params = gin.Params{{Key: "id", Value: itoaUintTest(engagement.ID)}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleEngagementSummary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Window struct {
				EndsAt string `json:"ends_at"`
				Open   bool   `json:"open"`
			} `json:"window"`
			Agents struct {
				Total   int `json:"total"`
				Online  int `json:"online"`
				Expired int `json:"expired"`
			} `json:"agents"`
			Tasks struct {
				Pending   int `json:"pending"`
				Completed int `json:"completed"`
				Failed    int `json:"failed"`
			} `json:"tasks"`
			Actions []struct {
				Action string `json:"action"`
				Count  int    `json:"count"`
			} `json:"actions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// Fleet: only this engagement's two agents, counted across their states.
	if resp.Data.Agents.Total != 2 || resp.Data.Agents.Online != 1 || resp.Data.Agents.Expired != 1 {
		t.Fatalf("agents = %+v, want total 2 / online 1 / expired 1", resp.Data.Agents)
	}

	// Tasks: three inside the window, and the pre-window task and the other engagement's task excluded.
	if resp.Data.Tasks.Completed != 1 || resp.Data.Tasks.Failed != 1 || resp.Data.Tasks.Pending != 1 {
		t.Fatalf("tasks = %+v, want completed 1 / failed 1 / pending 1", resp.Data.Tasks)
	}

	// Actions: highest count first, outside-window and other-tenant rows excluded.
	if len(resp.Data.Actions) != 2 {
		t.Fatalf("actions = %+v, want two distinct actions", resp.Data.Actions)
	}
	if resp.Data.Actions[0].Action != "task_create" || resp.Data.Actions[0].Count != 2 {
		t.Fatalf("first action = %+v, want task_create x2", resp.Data.Actions[0])
	}
	if resp.Data.Actions[1].Action != "login" || resp.Data.Actions[1].Count != 1 {
		t.Fatalf("second action = %+v", resp.Data.Actions[1])
	}

	// The window is closed and ends where the engagement was closed, not at its agreed end.
	if resp.Data.Window.Open {
		t.Fatal("a closed engagement reported an open window")
	}

	// Another tenant's engagement is a 404, and so is a non-existent one.
	for _, id := range []string{itoaUintTest(other.ID), "999999"} {
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/"+id+"/summary", nil)
		c2.Params = gin.Params{{Key: "id", Value: id}}
		c2.Set("user", "op")
		c2.Set("user_role", db.RoleAdmin)
		c2.Set("user_id", uint(1))
		c2.Set(middleware.TenantIDContextKey, uint(1))
		s.handleEngagementSummary(c2)
		if w2.Code != http.StatusNotFound {
			t.Fatalf("summary for engagement %s = %d, want 404", id, w2.Code)
		}
	}
}

// ptrTime is a tiny helper for the optional timestamps in these fixtures.
func ptrTime(t time.Time) *time.Time { return &t }
