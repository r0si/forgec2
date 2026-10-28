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

// TestRetryCountIsVisible covers the two places an automatic retry has to be visible: the exported row and the task detail.
//
// The retry is automatic, so "this result is the second attempt" is information the operator did not ask for and cannot infer
// - and a report that hides it makes a flaky enumeration look like a healthy one.
func TestRetryCountIsVisible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	task := db.Task{
		AgentID: "a-1", TenantID: 1, Type: "ps", Command: "ps", Status: "failed",
		Error: "context deadline exceeded", CreatedAt: now, UpdatedAt: now, RetryCount: 1,
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	// The column default would mask the value on insert, the same way it does for other defaults in this codebase.
	if err := s.db.Model(&db.Task{}).Where("id = ?", task.ID).Update("retry_count", 1).Error; err != nil {
		t.Fatalf("set retry count: %v", err)
	}

	export := func(query string) []string {
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
		return records[0]
	}

	// 1. The default column set is unchanged: a consumer parsing nine columns keeps nine.
	def := strings.Join(export(""), ",")
	if strings.Contains(def, "Retries") {
		t.Fatalf("the default export gained a Retries column: %q", def)
	}
	// 2. It is selectable by name.
	selected := export("?fields=type,retry_count")
	if got := strings.Join(selected, ","); got != "Type,Retries" {
		t.Fatalf("selection header = %q", got)
	}
	// 3. And `full` includes it, because that preset promises every column.
	full := export("?fields=full")
	found := false
	for _, column := range full {
		if column == "Retries" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the full preset does not include Retries: %v", full)
	}

	// 4. The detail names the attempt, but only when there was more than one.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/x", nil)
	c.Params = gin.Params{{Key: "taskId", Value: itoaUint(task.ID)}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleGetTaskStatus(c)
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if resp.Data["retry_count"] != float64(1) {
		t.Fatalf("detail retry_count = %v, want 1", resp.Data["retry_count"])
	}
	if resp.Data["failure_reason"] != "timeout" {
		t.Fatalf("the reason and the attempt should travel together: %v", resp.Data)
	}
}
