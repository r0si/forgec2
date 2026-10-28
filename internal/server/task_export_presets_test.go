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

// TestTaskExportFieldPresets covers the three named column sets.
//
// The presets exist because the useful requests are few and verbal: "just the outcome" (minimal), "what the CSV
// always showed" (default) and "everything, including the id" (full). A preset that is combined with field names is
// refused rather than guessed - "minimal,time" has two plausible meanings.
func TestTaskExportFieldPresets(t *testing.T) {
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
		Result: "out", CreatedAt: now, UpdatedAt: now,
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
	header := func(query string) []string {
		w := call(query)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", query, w.Code, w.Body.String())
		}
		records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
		if err != nil {
			t.Fatalf("csv: %v", err)
		}
		return records[0]
	}
	jsonKeys := func(query string) map[string]bool {
		w := call(query)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", query, w.Code, w.Body.String())
		}
		var body struct {
			Tasks []map[string]interface{} `json:"tasks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		keys := make(map[string]bool, len(body.Tasks[0]))
		for key := range body.Tasks[0] {
			keys[key] = true
		}
		return keys
	}

	// 1. minimal: identifiers plus the outcome, in that order.
	got := strings.Join(header("?fields=minimal"), ",")
	if got != "Task ID,Agent,Type,Status" {
		t.Fatalf("minimal header = %q", got)
	}

	// 2. default is what an absent value produces, for each format.
	if a, b := strings.Join(header("?fields=default"), ","), strings.Join(header(""), ","); a != b {
		t.Fatalf("default preset = %q, absent value = %q", a, b)
	}
	jsonDefaultKeys := jsonKeys("?format=json&fields=default")
	jsonAbsentKeys := jsonKeys("?format=json")
	if len(jsonDefaultKeys) != len(jsonAbsentKeys) {
		t.Fatalf("the default preset and an absent value disagree in JSON: %d vs %d keys", len(jsonDefaultKeys), len(jsonAbsentKeys))
	}

	// 3. full adds agent_id to the default CSV set.
	full := header("?fields=full")
	hasAgentID := false
	for _, column := range full {
		if column == "Agent ID" {
			hasAgentID = true
		}
	}
	if !hasAgentID {
		t.Fatalf("full header = %v, want it to include Agent ID", full)
	}
	if len(full) != len(exportFields) {
		t.Fatalf("full header has %d columns, the catalogue has %d", len(full), len(exportFields))
	}

	// 4. A preset mixed with field names is refused, and the message explains the choice.
	mixed := call("?fields=minimal,time")
	if mixed.Code != http.StatusBadRequest {
		t.Fatalf("mixed preset = %d, want 400 (%s)", mixed.Code, mixed.Body.String())
	}
	if !strings.Contains(mixed.Body.String(), "cannot be combined") {
		t.Fatalf("the error does not explain the rule: %s", mixed.Body.String())
	}

	// 5. An unknown name still lists both the fields and the presets.
	bad := call("?fields=bogus")
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "presets:") {
		t.Fatalf("unknown field = %d (%s)", bad.Code, bad.Body.String())
	}

	// 6. Each preset is a different document from the default, so the tags differ.
	if call("?fields=minimal").Header().Get("ETag") == call("?fields=full").Header().Get("ETag") {
		t.Fatal("minimal and full share an ETag")
	}
}
