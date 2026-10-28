package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestTaskExportCarriesTheEngagement covers the Engagement column.
//
// The agent list can already be filtered by engagement, so an export that cannot say which exercise a task
// belonged to would contradict the filter that selected it. The cell is filled from the agent's engagement
// and stays empty for an agent that belongs to none - which is the honest value, not a placeholder.
func TestTaskExportCarriesTheEngagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}

	for _, agent := range []db.Implant{
		{ID: "in-scope", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, EngagementID: &engagement.ID},
		{ID: "unscoped", TenantID: 1, Hostname: "web-02", Status: "online", LastSeen: now},
	} {
		agent := agent
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	for i, agentID := range []string{"in-scope", "unscoped"} {
		if err := s.db.Create(&db.Task{
			AgentID: agentID, TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export", nil)
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportTasks(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want header + 2 rows", len(records))
	}
	header := records[0]
	if header[len(header)-1] != "Engagement" {
		t.Fatalf("header = %v, want Engagement as the last column", header)
	}

	byAgent := make(map[string]string, 2)
	for _, row := range records[1:] {
		if len(row) != len(header) {
			t.Fatalf("row has %d fields, header has %d: %v", len(row), len(header), row)
		}
		// Agent is column 1, Engagement is the last.
		byAgent[row[1]] = row[len(row)-1]
	}
	if got := byAgent["web-01"]; got != "DMZ exercise" {
		t.Fatalf("in-scope engagement = %q, want the exercise name", got)
	}
	if got, ok := byAgent["web-02"]; !ok || got != "" {
		t.Fatalf("unscoped engagement = %q (present=%v), want an empty cell", got, ok)
	}
}
