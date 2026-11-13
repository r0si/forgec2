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

// TestLateResultIsExportable pins the export contract for the late-result
// fields: a cancelled task's status column says cancelled, so the file needs a
// separate column to say what the agent reported after the abort. It is
// selectable and in the full preset, but never added to the default CSV.
func TestLateResultIsExportable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{ID: "late-export-agent", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now}).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{
		AgentID: "late-export-agent", TenantID: 1, Type: "shell", Command: "whoami",
		Status: "cancelled", Error: "cancelled by operator", Result: "administrator",
		LateResult: true, LateResultStatus: "completed", LateResultAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	export := func(query string) [][]string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportTasks(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", query, w.Code, w.Body.String())
		}
		records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
		if err != nil {
			t.Fatalf("csv: %v", err)
		}
		return records
	}

	def := export("")
	if strings.Contains(strings.Join(def[0], ","), "Late result") {
		t.Fatalf("the default CSV gained a Late result column: %v", def[0])
	}

	selected := export("?fields=type,late_result")
	if got := strings.Join(selected[0], ","); got != "Type,Late result" {
		t.Fatalf("selection header = %q", got)
	}
	if len(selected) < 2 || selected[1][1] != "completed" {
		t.Fatalf("late result row = %v, want completed", selected)
	}

	full := export("?fields=full")
	found := false
	for _, column := range full[0] {
		if column == "Late result" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the full preset does not include Late result: %v", full[0])
	}
}
