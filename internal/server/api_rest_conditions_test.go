package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestMatchCondition(t *testing.T) {
	s := mustTenantServer(t)
	evt := Event{
		Type:      "agent.checkin",
		AgentID:   "a1",
		AgentHost: "WIN-DC01.corp.example",
		Data:      map[string]interface{}{"severity": "High", "count": 3},
	}

	cases := []struct {
		name string
		cond RuleCondition
		want bool
	}{
		{"contains is case-insensitive", RuleCondition{Field: "agent.hostname", Operator: "contains", Value: "dc01"}, true},
		{"contains miss", RuleCondition{Field: "agent.hostname", Operator: "contains", Value: "linux"}, false},
		{"equals is exact", RuleCondition{Field: "agent.hostname", Operator: "equals", Value: "WIN-DC01.corp.example"}, true},
		{"equals is case-sensitive", RuleCondition{Field: "agent.hostname", Operator: "equals", Value: "win-dc01.corp.example"}, false},
		{"data field contains", RuleCondition{Field: "severity", Operator: "contains", Value: "high"}, true},
		{"numeric data field", RuleCondition{Field: "count", Operator: "equals", Value: "3"}, true},
		{"missing data field is empty", RuleCondition{Field: "absent", Operator: "contains", Value: "x"}, false},
		// This case used to be called "unknown operator allows" and expected TRUE: it codified the fail-open default, and it passed
		// because `regex` was never implemented. Both halves are now right - the operator works, and an invalid pattern fails closed.
		{"regex matches", RuleCondition{Field: "severity", Operator: "regex", Value: "^H"}, true},
		{"regex miss", RuleCondition{Field: "severity", Operator: "regex", Value: "^z"}, false},
		{"invalid regex fails closed", RuleCondition{Field: "severity", Operator: "regex", Value: "[a-z"}, false},
		{"unknown operator fails closed", RuleCondition{Field: "severity", Operator: "startswith", Value: "H"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.matchCondition(tc.cond, evt); got != tc.want {
				t.Fatalf("matchCondition(%+v) = %v, want %v", tc.cond, got, tc.want)
			}
		})
	}
}

func TestParseCronSchedule(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"hourly", time.Hour},
		{"daily", 24 * time.Hour},
		{"weekly", 7 * 24 * time.Hour},
		{"monthly", 30 * 24 * time.Hour},
		{"90m", 90 * time.Minute},
		{"1h30m", 90 * time.Minute},
	}
	for _, tc := range cases {
		got, err := parseCronSchedule(tc.in)
		if err != nil {
			t.Fatalf("parseCronSchedule(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseCronSchedule(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := parseCronSchedule("every-full-moon"); err == nil || !strings.Contains(err.Error(), "invalid backup schedule") {
		t.Fatalf("invalid schedule error = %v", err)
	}
}

func TestAPIRestTaskEndpoints(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "rest-agent", 1)
	seedTenantAgent(t, s, "rest-foreign", 2)

	// Create: validation first.
	c, w := missingAPICtx(http.MethodPost, "/api/v1/tasks", `{"type":"shell"}`, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing agent = %d", w.Code)
	}
	c, w = missingAPICtx(http.MethodPost, "/api/v1/tasks", `{"agent_id":"rest-agent","type":"not_a_type"}`, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unknown task type") {
		t.Fatalf("unknown type = %d %s", w.Code, w.Body.String())
	}

	c, w = missingAPICtx(http.MethodPost, "/api/v1/tasks", `{"agent_id":"rest-agent","type":"shell","command":"whoami"}`, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID uint `json:"id"`
	}
	unmarshalData(t, w.Body.Bytes(), &created)
	if created.ID == 0 {
		t.Fatalf("created task = %+v", created)
	}

	// Get: strict numeric id, own task visible, foreign task 404.
	taskID := jsonNumber(created.ID)
	c, w = missingAPICtx(http.MethodGet, "/api/v1/tasks/x", "",
		gin.Params{{Key: "id", Value: "abc"}})
	s.apiGetTask(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id = %d", w.Code)
	}
	c, w = missingAPICtx(http.MethodGet, "/api/v1/tasks/x", "",
		gin.Params{{Key: "id", Value: taskID}})
	s.apiGetTask(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "whoami") {
		t.Fatalf("own task = %d %s", w.Code, w.Body.String())
	}

	foreign := db.Task{AgentID: "rest-foreign", TenantID: 2, Type: "shell", Command: "foreign", Status: "pending"}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodGet, "/api/v1/tasks/x", "",
		gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}})
	s.apiGetTask(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign task = %d %s", w.Code, w.Body.String())
	}

	// Bulk results: the documented {success, results, total} list shape.
	c, w = missingAPICtx(http.MethodGet, "/api/v1/bulk/results", "", nil)
	s.apiBulkResults(c)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk results = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"success":true`) || !strings.Contains(w.Body.String(), `"total"`) {
		t.Fatalf("bulk results shape = %s", w.Body.String())
	}

	// The listener list delegates to the shared handler.
	c, w = missingAPICtx(http.MethodGet, "/api/v1/listeners", "", nil)
	s.apiListListeners(c)
	if w.Code != http.StatusOK {
		t.Fatalf("listeners = %d %s", w.Code, w.Body.String())
	}
}

func TestAPIRestTaskEndpointsRequireOperator(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	// The viewer role is the documented non-operator (requireOperator is a
	// blocklist: it refuses "viewer" and the empty role).
	for name, fn := range map[string]func(*gin.Context){
		"create": s.apiCreateTask,
		"get":    s.apiGetTask,
		"list":   s.apiListListeners,
	} {
		c, w := missingAPICtx(http.MethodPost, "/api/v1/tasks", `{}`, gin.Params{{Key: "id", Value: "1"}})
		c.Set("user_role", "viewer")
		fn(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s as viewer = %d", name, w.Code)
		}
	}
}
