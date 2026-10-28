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

// TestAuditExportFilters covers the investigation filters added to the export.
//
// The export's promise is that a third party can verify the chain offline, and that promise only holds
// for a CONTIGUOUS range. A filtered export is a selection - row N+1's prev_hash points at a row the
// filter dropped - so the response has to say so, and it does it in headers rather than by injecting a
// metadata line, because a verifier must be able to treat every line as an audit row.
func TestAuditExportFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now()
	seed := func(user, action, agentID string, tenant uint, age time.Duration) {
		row := db.AuditLog{
			User: user, Action: action, AgentID: agentID, TenantID: tenant,
			Success: true, CreatedAt: now.Add(-age), PrevHash: "p", EntryHash: "e",
		}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed("alice", "login", "", 1, time.Hour)
	seed("alice", "task_create", "a-1", 1, 2*time.Hour)
	seed("bob", "task_create", "a-1", 1, 3*time.Hour)
	seed("carol", "task_create", "b-2", 2, time.Hour) // another tenant

	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportAuditChain(c)
		return w
	}

	rows := func(w *httptest.ResponseRecorder) []auditChainExportRow {
		var out []auditChainExportRow
		for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
			if line == "" {
				continue
			}
			var row auditChainExportRow
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("line is not an audit row: %v (%q)", err, line)
			}
			out = append(out, row)
		}
		return out
	}

	// Unfiltered: the tenant's three rows, both hashes present, and the header says "not filtered".
	w := call("")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	if got := rows(w); len(got) != 3 {
		t.Fatalf("unfiltered rows = %d, want 3 (tenant isolation): %s", len(got), w.Body.String())
	}
	if w.Header().Get("X-Audit-Export-Filtered") != "false" {
		t.Fatalf("unfiltered export reported filtered: %q", w.Header().Get("X-Audit-Export-Filtered"))
	}
	if w.Header().Get("Warning") != "" {
		t.Fatalf("unfiltered export carried a warning: %q", w.Header().Get("Warning"))
	}
	if w.Header().Get("X-Audit-Export-Rows") != "3" {
		t.Fatalf("row count header = %q", w.Header().Get("X-Audit-Export-Rows"))
	}
	for _, row := range rows(w) {
		if row.EntryHash == "" || row.PrevHash == "" {
			t.Fatalf("a row lost its hashes: %+v", row)
		}
	}

	// Filtered by action: only task_create, and the response declares the selection.
	w2 := call("?action=task_create")
	if got := rows(w2); len(got) != 2 {
		t.Fatalf("action filter returned %d rows, want 2", len(got))
	}
	if w2.Header().Get("X-Audit-Export-Filtered") != "true" {
		t.Fatal("a filtered export was not marked filtered")
	}
	if !strings.Contains(w2.Header().Get("Warning"), "not a contiguous hash chain") {
		t.Fatalf("the warning does not explain the discontinuity: %q", w2.Header().Get("Warning"))
	}
	if !strings.Contains(w2.Header().Get("Content-Disposition"), "filtered") {
		t.Fatalf("the filename does not say filtered: %q", w2.Header().Get("Content-Disposition"))
	}

	// Filtered by user and by agent id.
	if got := rows(call("?user=bob")); len(got) != 1 || got[0].User != "bob" {
		t.Fatalf("user filter returned %+v", got)
	}
	if got := rows(call("?agent_id=a-1")); len(got) != 2 {
		t.Fatalf("agent filter returned %d rows, want 2", len(got))
	}

	// A time window, in both accepted forms: the duration the dashboard uses and an RFC3339 stamp.
	if got := rows(call("?since=90m")); len(got) != 1 {
		t.Fatalf("since=90m returned %d rows, want 1 (the login 1h ago)", len(got))
	}
	cutoff := now.Add(-150 * time.Minute).UTC().Format(time.RFC3339)
	wCut := call("?since=" + cutoff)
	if got := rows(wCut); len(got) != 2 {
		for _, row := range got {
			t.Logf("row user=%s action=%s created=%s", row.User, row.Action, row.CreatedAt)
		}
		t.Fatalf("since=%s returned %d rows, want 2 (status %d, filtered=%q)", cutoff, len(got), wCut.Code, wCut.Header().Get("X-Audit-Export-Filtered"))
	}

	// An unparseable window is a 400 rather than silently exporting everything.
	if got := call("?since=forever"); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid since = %d, want 400", got.Code)
	}

	// since_id stays a contiguous continuation and is therefore NOT marked filtered.
	if got := call("?since_id=1"); got.Header().Get("X-Audit-Export-Filtered") != "false" {
		t.Fatalf("since_id was treated as a filter: %q", got.Header().Get("X-Audit-Export-Filtered"))
	}
}
