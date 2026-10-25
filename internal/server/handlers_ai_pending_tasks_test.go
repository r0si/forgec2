package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlCounter wraps a GORM logger and tallies the statements a handler issued.
// "implants" counts every query touching the implants table; "implantsMultiID"
// narrows that to multi-id (`IN (...)`) lookups, which is what distinguishes a
// batched fetch from a per-row one. createTask independently reads the implants
// table for every task it queues, so a raw implant-query count would be
// dominated by dispatch rather than by the loop under test.
//
// Counting is the point: an implementation that renders identical JSON while
// slipping back to a per-row lookup would pass any output assertion.
type sqlCounter struct {
	logger.Interface
	total          *int
	implants       *int
	implantsMultiI *int
}

func (c sqlCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	*c.total++
	if !strings.Contains(sql, "implants") {
		return
	}
	*c.implants++
	if strings.Contains(sql, " IN (") || strings.Contains(sql, " IN ?") {
		*c.implantsMultiI++
	}
}

func withSQLCounter(t *testing.T, s *Server) func() (total, implants, multiID int) {
	t.Helper()
	total, implants, multiID := 0, 0, 0
	s.db = s.db.Session(&gorm.Session{
		Logger: sqlCounter{
			Interface:      logger.Discard,
			total:          &total,
			implants:       &implants,
			implantsMultiI: &multiID,
		},
	})
	return func() (int, int, int) { return total, implants, multiID }
}

func seedPendingApproval(t *testing.T, s *Server, id, agentID, command string, tenantID uint) {
	t.Helper()
	task := db.Task{
		AgentID:   agentID,
		Type:      "shell",
		Command:   command,
		Status:    TaskStatusPendingApproval,
		CreatedBy: "ai",
		TenantID:  tenantID,
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed pending task %s: %v", id, err)
	}
}

// TestAIPendingTasksHostnameLookupIsBatched pins the query count: a full page
// of 12 rows must cost one implant lookup, not one per row and not one per
// row per task. The old loop issued 12 SELECTs here, and repeated the same
// SELECT for every task belonging to one agent.
func TestAIPendingTasksHostnameLookupIsBatched(t *testing.T) {
	s := mustTenantServer(t)
	seedReportAgent(t, s, "ai-batch-1", "HOST-ONE", 1)
	seedReportAgent(t, s, "ai-batch-2", "HOST-TWO", 1)
	// 12 tasks across only 2 distinct agents: the naive per-row lookup pays
	// 12 queries, and 10 of them are duplicates.
	for i := 0; i < 12; i++ {
		agent := "ai-batch-1"
		if i%2 == 1 {
			agent = "ai-batch-2"
		}
		seedPendingApproval(t, s, string(rune('a'+i)), agent, "cmd", 1)
	}

	ctx, rec := tenantScopedAdminContext(s, t, "ai-batch-admin", 1)
	served := withSQLCounter(t, s)
	s.handleAIPendingTasks(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	_, _, implantQueries := served()
	if implantQueries > 1 {
		t.Fatalf("issued %d implant queries for 12 rows sharing 2 agents; want 1 batched lookup", implantQueries)
	}

	var body struct {
		Success bool `json:"success"`
		Tasks   []struct {
			AgentID  string `json:"agent_id"`
			Hostname string `json:"hostname"`
		} `json:"tasks"`
	}
	unmarshalData(t, rec.Body.Bytes(), &body)
	if len(body.Tasks) != 12 {
		t.Fatalf("returned %d tasks, want 12", len(body.Tasks))
	}
	// Batching must not lose or cross-wire hostnames.
	want := map[string]string{"ai-batch-1": "HOST-ONE", "ai-batch-2": "HOST-TWO"}
	for _, task := range body.Tasks {
		if got := want[task.AgentID]; task.Hostname != got {
			t.Fatalf("agent %s: hostname = %q, want %q", task.AgentID, task.Hostname, got)
		}
	}
}

// TestAIPendingTasksUnknownAgentYieldsEmptyHostname keeps the old observable
// behaviour: an agent that no longer exists renders a blank hostname rather
// than failing the request or borrowing another agent's name.
func TestAIPendingTasksUnknownAgentYieldsEmptyHostname(t *testing.T) {
	s := mustTenantServer(t)
	seedReportAgent(t, s, "ai-known", "KNOWN-HOST", 1)
	seedPendingApproval(t, s, "a", "ai-known", "cmd", 1)
	seedPendingApproval(t, s, "b", "ai-gone", "cmd", 1)

	ctx, rec := tenantScopedAdminContext(s, t, "ai-gone-admin", 1)
	s.handleAIPendingTasks(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "KNOWN-HOST") {
		t.Fatalf("known agent hostname missing: %s", rec.Body.String())
	}
	var body struct {
		Tasks []struct {
			AgentID  string `json:"agent_id"`
			Hostname string `json:"hostname"`
		} `json:"tasks"`
	}
	unmarshalData(t, rec.Body.Bytes(), &body)
	for _, task := range body.Tasks {
		if task.AgentID == "ai-gone" && task.Hostname != "" {
			t.Fatalf("missing agent rendered hostname %q, want empty", task.Hostname)
		}
	}
}

// TestAIPendingTasksNoTasksIssuesNoLookup keeps the empty case free of a
// pointless round trip.
func TestAIPendingTasksNoTasksIssuesNoLookup(t *testing.T) {
	s := mustTenantServer(t)
	ctx, rec := tenantScopedAdminContext(s, t, "ai-empty-admin", 1)
	served := withSQLCounter(t, s)
	s.handleAIPendingTasks(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	_, _, implantQueries := served()
	if implantQueries != 0 {
		t.Fatalf("issued %d implant queries with no pending tasks, want 0", implantQueries)
	}
}

// TestAgentIDsOfDedupes guards the helper directly: order-preserving dedup is
// what turns 12 tasks over 2 agents into a 2-element IN clause.
func TestAgentIDsOfDedupes(t *testing.T) {
	got := agentIDsOf([]db.Task{
		{AgentID: "b"}, {AgentID: "a"}, {AgentID: "b"}, {AgentID: ""}, {AgentID: "a"},
	})
	want := []string{"b", "a"}
	if len(got) != len(want) {
		t.Fatalf("agentIDsOf = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agentIDsOf = %v, want %v (order must be first-seen)", got, want)
		}
	}
}

var _ = httptest.NewRecorder
