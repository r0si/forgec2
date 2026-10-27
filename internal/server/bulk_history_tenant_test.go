package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestBulkHistoryIsTenantScoped pins the fix for a cross-tenant read: the history
// is process-wide, so both readers returned every tenant's rows - including the
// command string, which can carry credentials (a batched shell command).
func TestBulkHistoryIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	// currentTenantID resolves the caller's tenant from the user row, so the
	// caller has to exist: an unknown (or tenant-less) caller resolves to 0,
	// which is the intentionally unscoped single-tenant view.
	seedAIUser(t, s, "admin1", 1)

	s.bulkHistoryMu.Lock()
	s.bulkHistory = []BulkResult{
		{ID: 1, TenantID: 1, Command: "whoami", TaskType: "shell", Operator: "mine", Timestamp: time.Now()},
		{ID: 2, TenantID: 2, Command: "net user admin P@ssw0rd /add", TaskType: "shell", Operator: "foreign", Timestamp: time.Now()},
		{ID: 3, TenantID: 0, Command: "legacy", TaskType: "shell", Operator: "system", Timestamp: time.Now()},
	}
	s.bulkHistoryMu.Unlock()

	ctx := func() (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/bulk/results", nil)
		c.Set("user", "admin1")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		return c, w
	}

	// A tenant-1 operator must not see tenant 2's command (nor its password).
	c, _ := ctx()
	got := s.bulkHistoryFor(c)
	for _, r := range got {
		if r.TenantID == 2 {
			t.Fatalf("another tenant's bulk command leaked: %+v", r)
		}
	}
	if len(got) != 2 {
		t.Fatalf("visible rows = %d, want 2 (own + legacy)", len(got))
	}

	// The endpoints use the same filter.
	c, w := ctx()
	s.handleBulkResults(c)
	if w.Code != http.StatusOK {
		t.Fatalf("handleBulkResults = %d", w.Code)
	}
	if body := w.Body.String(); containsAny(body, "P@ssw0rd", "foreign") {
		t.Fatalf("the endpoint leaked a foreign bulk command: %s", body)
	}

	c, w = ctx()
	s.apiBulkResults(c)
	if w.Code != http.StatusOK {
		t.Fatalf("apiBulkResults = %d", w.Code)
	}
	if body := w.Body.String(); containsAny(body, "P@ssw0rd", "foreign") {
		t.Fatalf("the API endpoint leaked a foreign bulk command: %s", body)
	}
}

// TestBulkResultRecordsTheTenant pins the producer side: without stamping the
// tenant there is nothing for the readers to filter on.
func TestBulkResultRecordsTheTenant(t *testing.T) {
	s := mustTenantServer(t)

	s.pushBulkResult(BulkResult{TenantID: 1, Command: "whoami", TaskType: "shell", Timestamp: time.Now()})
	s.pushBulkResult(BulkResult{TenantID: 2, Command: "secret", TaskType: "shell", Timestamp: time.Now()})

	s.bulkHistoryMu.Lock()
	defer s.bulkHistoryMu.Unlock()
	if len(s.bulkHistory) != 2 {
		t.Fatalf("history = %d", len(s.bulkHistory))
	}
	if s.bulkHistory[0].TenantID != 1 || s.bulkHistory[1].TenantID != 2 {
		t.Fatalf("tenant ids were not preserved: %+v", s.bulkHistory)
	}
	// The trim keeps the newest entries and never exceeds the cap.
	s.bulkHistoryMu.Unlock()
	for i := 0; i < maxBulkHistory+5; i++ {
		s.pushBulkResult(BulkResult{TenantID: 1, Timestamp: time.Now()})
	}
	s.bulkHistoryMu.Lock()
	if len(s.bulkHistory) > maxBulkHistory {
		t.Fatalf("history grew to %d, cap is %d", len(s.bulkHistory), maxBulkHistory)
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if len(n) > 0 && len(haystack) >= len(n) {
			for i := 0; i+len(n) <= len(haystack); i++ {
				if haystack[i:i+len(n)] == n {
					return true
				}
			}
		}
	}
	return false
}
