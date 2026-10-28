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

// TestTaskExportFiltersByEngagement completes the trio: the agent list can be filtered by engagement
// (round 25), the export can say which engagement a row came from (round 31), and this makes the export
// itself selectable by the same parameter - so an operator who exports "this exercise" gets this exercise.
func TestTaskExportFiltersByEngagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	foreign := db.Engagement{TenantID: 2, Name: "other", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatalf("seed foreign: %v", err)
	}

	for _, agent := range []db.Implant{
		{ID: "in-scope", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, EngagementID: &engagement.ID},
		{ID: "unscoped", TenantID: 1, Hostname: "web-02", Status: "online", LastSeen: now},
		{ID: "foreign", TenantID: 2, Hostname: "evil", Status: "online", LastSeen: now, EngagementID: &foreign.ID},
	} {
		agent := agent
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	for i, agentID := range []string{"in-scope", "in-scope", "unscoped", "foreign"} {
		tenant := uint(1)
		if agentID == "foreign" {
			tenant = 2
		}
		if err := s.db.Create(&db.Task{
			AgentID: agentID, TenantID: tenant, Type: "shell", Command: "whoami", Status: "completed",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}

	call := func(query string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleExportTasks(c)
		return w
	}

	rows := func(w *httptest.ResponseRecorder) []string {
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
		if err != nil {
			t.Fatalf("csv: %v", err)
		}
		out := make([]string, 0, len(records))
		for _, row := range records[1:] { // skip the header
			out = append(out, row[1]) // the Agent column
		}
		return out
	}

	// 1. Filtering by the engagement returns only its tasks (two of them), never another tenant's.
	members := rows(call("?engagement="+itoaUintTest(engagement.ID), 1))
	if len(members) != 2 {
		t.Fatalf("engagement export = %v, want the two in-scope tasks", members)
	}
	for _, agent := range members {
		if agent != "web-01" {
			t.Fatalf("export = %v, want only web-01 rows", members)
		}
	}

	// 2. "none" is the inverse: the tasks of agents that belong to no exercise.
	unscoped := rows(call("?engagement=none", 1))
	if len(unscoped) != 1 || unscoped[0] != "web-02" {
		t.Fatalf("engagement=none export = %v, want only web-02's task", unscoped)
	}

	// 3. Without the filter the whole tenant is exported: the parameter narrows, it never replaces.
	if all := rows(call("", 1)); len(all) != 3 {
		t.Fatalf("unfiltered export = %v, want all three tenant tasks", all)
	}

	// 4. An invalid value is a 400 - and it must not be answered from the ETag cache either, which is why
	// the validation runs before the tag is computed.
	if got := call("?engagement=not-a-number", 1); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid engagement = %d, want 400", got.Code)
	}

	// 5. Another tenant naming this engagement id gets nothing: the subquery is tenant-scoped.
	if got := rows(call("?engagement="+itoaUintTest(engagement.ID), 2)); len(got) != 0 {
		t.Fatalf("tenant 2 saw %v by naming tenant 1's engagement", got)
	}
}
