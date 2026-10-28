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

// TestAuditExportUntilBounds covers ?until= on the chain export, the other half of the pair the task export got
// in the previous round.
//
// An investigation usually wants "the night of the incident", not "everything after it": without an upper bound
// the only way to stop at an instant was to read the file and ignore the tail, which is exactly the kind of manual
// step that gets skipped.
func TestAuditExportUntilBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// LOCAL time, not UTC: these columns are stored as text carrying the writer offset, so a fixture that
	// seeds UTC rows and filters with a local instant compares "15:39+00:00" against "23:39+08:00" and gets
	// nonsense. Production writes time.Now(), which is local, so the fixture has to match the writer.
	now := time.Now().Truncate(time.Second)
	seed := func(user string, age time.Duration) {
		if err := s.db.Create(&db.AuditLog{
			User: user, Action: "login", TenantID: 1, Success: true,
			CreatedAt: now.Add(-age), PrevHash: "p", EntryHash: "e",
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed("minute-ago", time.Minute)
	seed("hours-ago", 5*time.Hour)
	seed("days-ago", 3*24*time.Hour)

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
	users := func(w *httptest.ResponseRecorder) []string {
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
			if line == "" {
				continue
			}
			var row auditChainExportRow
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("line is not an audit row: %v (%q)", err, line)
			}
			out = append(out, row.User)
		}
		return out
	}

	// 1. No bounds: the whole tenant.
	if got := users(call("")); len(got) != 3 {
		t.Fatalf("unbounded export = %v, want all three", got)
	}

	// 2. until keeps the older half only.
	if got := users(call("?until=48h")); len(got) != 1 || got[0] != "days-ago" {
		t.Fatalf("until=48h = %v, want only the three-day-old row", got)
	}

	// 3. A window keeps the middle.
	if got := users(call("?since=4d&until=2d")); len(got) != 1 || got[0] != "days-ago" {
		t.Fatalf("window = %v", got)
	}
	if got := users(call("?since=6h&until=30m")); len(got) != 1 || got[0] != "hours-ago" {
		t.Fatalf("six-hour to thirty-minute window = %v, want the five-hour-old row", got)
	}

	// 4. An until-only request is a FILTER, so the response must keep saying the rows are a selection rather
	// than a contiguous chain.
	tagged := call("?until=48h")
	if got := tagged.Header().Get("X-Audit-Export-Filtered"); got != "true" {
		t.Fatalf("X-Audit-Export-Filtered = %q, want true", got)
	}
	if !strings.Contains(tagged.Header().Get("Warning"), "not a contiguous hash chain") {
		t.Fatalf("the warning is missing: %q", tagged.Header().Get("Warning"))
	}

	// 5. Invalid or reversed bounds are refused.
	for _, query := range []string{"?until=whenever", "?since=2d&until=4d"} {
		if got := call(query); got.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", query, got.Code)
		}
	}

	// 6. The ETag distinguishes the two windows: an until-only export must not be served from a tag computed
	// without it.
	without := call("")
	withUntil := call("?until=48h")
	if without.Header().Get("ETag") == withUntil.Header().Get("ETag") {
		t.Fatalf("the ETag ignores until: %q", without.Header().Get("ETag"))
	}
}
