package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestReadyReportsDroppedAuditEntries pins the observability contract for the
// bounded audit queue: a drop is a count on /ready and a warning, not a line
// buried in the log. The probe deliberately stays ready (the process is fine;
// the log has a gap), matching the audit-chain policy.
func TestReadyReportsDroppedAuditEntries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	s.auditDropped.Store(3)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleReadyCheck(c)

	if w.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200 (a gap is an alarm, not unreadiness)", w.Code)
	}
	var body struct {
		Status   string                 `json:"status"`
		Checks   map[string]interface{} `json:"checks"`
		Warnings []string               `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ready: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q", body.Status)
	}
	if got, _ := body.Checks["audit_dropped"].(float64); got != 3 {
		t.Fatalf("audit_dropped = %v, want 3", body.Checks["audit_dropped"])
	}
	found := false
	for _, warning := range body.Warnings {
		if strings.Contains(warning, "audit") && strings.Contains(warning, "dropped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings do not name dropped audit entries: %v", body.Warnings)
	}
}
