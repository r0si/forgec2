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

// TestTaskExportFieldSelection covers ?fields=: choosing the columns of an export.
//
// Ten thousand rows of result text is often more than the caller needs - "what ran, did it work" wants four
// columns - and both formats must honour the same list in the same order, or a script and a spreadsheet would
// describe the same export differently.
func TestTaskExportFieldSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
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

	// 1. A selection returns exactly those columns, in the order asked for.
	w := call("?fields=status,type")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want header + 1 row", len(records))
	}
	if got := strings.Join(records[0], ","); got != "Status,Type" {
		t.Fatalf("header = %q, want Status,Type (the caller order)", got)
	}
	if got := strings.Join(records[1], ","); got != "completed,shell" {
		t.Fatalf("row = %q, want completed,shell", got)
	}

	// 2. The same selection in JSON gives the same two keys.
	w = call("?fields=status,type&format=json")
	var body struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(body.Tasks) != 1 {
		t.Fatalf("json tasks = %d", len(body.Tasks))
	}
	if len(body.Tasks[0]) != 2 || body.Tasks[0]["status"] != "completed" || body.Tasks[0]["type"] != "shell" {
		t.Fatalf("json row = %v, want exactly status and type", body.Tasks[0])
	}

	// 3. A selection without the defaults still works: the caller may ask for only the ids.
	w = call("?fields=task_id,agent_id")
	firstLine := strings.Split(strings.TrimSpace(w.Body.String()), "\n")[0]
	if !strings.Contains(firstLine, "Task ID") || !strings.Contains(firstLine, "Agent ID") {
		t.Fatalf("id-only header = %q", firstLine)
	}

	// 4. An unknown field is refused and the message says what is valid - a silently ignored column would look
	// like the field exists and was empty.
	bad := call("?fields=status,bogus")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", bad.Code)
	}
	if msg := bad.Body.String(); !strings.Contains(msg, "bogus") || !strings.Contains(msg, "engagement") {
		t.Fatalf("the error does not name the bad field and the valid ones: %s", msg)
	}

	// 5. Two different selections are two different documents, so their tags must differ.
	a := call("?fields=status")
	b := call("?fields=type")
	if a.Header().Get("ETag") == b.Header().Get("ETag") {
		t.Fatalf("the ETag ignores the selection: %q", a.Header().Get("ETag"))
	}

	// 6. The default set is unchanged for CSV, and includes agent_id for JSON.
	csvDefault := call("")
	header := strings.Split(strings.TrimSpace(csvDefault.Body.String()), "\n")[0]
	if strings.Contains(header, "Agent ID") {
		t.Fatalf("the CSV default gained an Agent ID column: %q", header)
	}
	jsonDefault := call("?format=json")
	if err := json.Unmarshal(jsonDefault.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode default json: %v", err)
	}
	if _, ok := body.Tasks[0]["agent_id"]; !ok {
		t.Fatalf("the JSON default lost agent_id: %v", body.Tasks[0])
	}
}
