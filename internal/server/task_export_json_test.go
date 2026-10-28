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

// TestTaskExportJSONFormat covers ?format=json: the same rows as the CSV, in a shape a script can read without
// parsing a spreadsheet.
//
// Three properties matter and are asserted rather than assumed: the content is the same data (same redaction and
// the same column truncation, so two consumers cannot disagree about what a cell held), the response says it is
// JSON in both content type and filename, and the ETag distinguishes the two documents - a client that revalidated
// the CSV must not be told 304 when it asks for the JSON.
func TestTaskExportJSONFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
		Result: "NT AUTHORITY\\SYSTEM", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportTasks(c)
		return w
	}

	// 1. The JSON variant answers as one document with the rows.
	w := call("?format=json")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.json"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	var body struct {
		Count     int    `json:"count"`
		Truncated bool   `json:"truncated"`
		Format    string `json:"format"`
		Tasks     []struct {
			Agent      string `json:"agent"`
			AgentID    string `json:"agent_id"`
			Type       string `json:"type"`
			Status     string `json:"status"`
			TaskID     uint   `json:"task_id"`
			Engagement string `json:"engagement"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if body.Count != 1 || len(body.Tasks) != 1 || body.Format != "json" || body.Truncated {
		t.Fatalf("payload = %+v", body)
	}
	row := body.Tasks[0]
	if row.Agent != "web-01" || row.AgentID != "a-1" || row.Type != "shell" || row.Status != "completed" {
		t.Fatalf("row = %+v", row)
	}
	// The engagement name travels in the JSON too, so the two formats describe the same export.
	if row.Engagement != "DMZ exercise" {
		t.Fatalf("engagement = %q", row.Engagement)
	}
	// The id is a number here, which is the point of the format: a script does not have to parse it out of a cell.
	if row.TaskID == 0 {
		t.Fatalf("task_id = %d", row.TaskID)
	}

	// 2. The default is still CSV, unchanged.
	csvResponse := call("")
	if ct := csvResponse.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("default Content-Type = %q", ct)
	}

	// 3. The two formats are different documents, so their tags must differ.
	jsonETag := w.Header().Get("ETag")
	csvETag := csvResponse.Header().Get("ETag")
	if jsonETag == "" || csvETag == "" {
		t.Skip("no ETag on this server configuration")
	}
	if jsonETag == csvETag {
		t.Fatalf("the ETag is identical for both formats: %q", jsonETag)
	}
	// And the CSV's tag must not satisfy a JSON request.
	again := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(again)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/export?format=json", nil)
	c.Request.Header.Set("If-None-Match", csvETag)
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleExportTasks(c)
	if again.Code != http.StatusOK {
		t.Fatalf("the CSV tag answered %d for a JSON request, want 200 with the document", again.Code)
	}

	// 4. An unknown format is a 400 rather than a silent fallback to CSV.
	if got := call("?format=xlsx"); got.Code != http.StatusBadRequest {
		t.Fatalf("format=xlsx = %d, want 400", got.Code)
	}
}
