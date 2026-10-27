package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func readyCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	return c, w
}

// TestReadyReportsAuditChainIntegrity pins the operational visibility: a tampered
// audit log shows up on /ready (a probe every few seconds sees it without anyone
// opening a console page), but it does NOT take the server unready — the C2 has to
// keep serving, and a tampered log is an alarm about the past, not a live fault.
func TestReadyReportsAuditChainIntegrity(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 3)

	c, w := readyCtx()
	s.handleReadyCheck(c)
	if w.Code != http.StatusOK {
		t.Fatalf("intact chain: status %d (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"audit_chain"`) || !strings.Contains(body, "ok (") {
		t.Fatalf("the ready check does not report the chain: %s", body)
	}
	if strings.Contains(body, "warnings") {
		t.Fatalf("an intact chain produced a warning: %s", body)
	}

	// Tamper with a row.
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[1].ID).
		Update("details", "tampered").Error; err != nil {
		t.Fatal(err)
	}

	c, w = readyCtx()
	s.handleReadyCheck(c)
	if w.Code != http.StatusOK {
		t.Fatalf("a tampered chain made the server unready: %d", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, "integrity failure at row") {
		t.Fatalf("the failure is not reported: %s", body)
	}
	if !strings.Contains(body, `"warnings"`) || !strings.Contains(body, "audit chain integrity failure") {
		t.Fatalf("no warning was surfaced: %s", body)
	}
}

// TestReadyAuditTamperIsReportedOnce pins the rate limit: /ready is polled every
// few seconds, so the same finding must not write an audit row each time.
func TestReadyAuditTamperIsReportedOnce(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 2)
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[0].ID).
		Update("success", false).Error; err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		c, w := readyCtx()
		s.handleReadyCheck(c)
		if w.Code != http.StatusOK {
			t.Fatalf("poll %d: status %d", i, w.Code)
		}
	}

	var count int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "audit_chain_tamper").Count(&count)
	if count != 1 {
		t.Fatalf("tamper audit rows = %d, want 1 (rate-limited)", count)
	}
}

// TestReadySurvivesNilAuditTables covers the degraded shapes: a bare Server and one
// whose audit table is empty must still answer.
func TestReadySurvivesNilAuditTables(t *testing.T) {
	bare := &Server{}
	c, w := readyCtx()
	bare.handleReadyCheck(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an uninitialised server answered %d, want 503", w.Code)
	}

	s := mustTenantServer(t)
	c, w = readyCtx()
	s.handleReadyCheck(c)
	if w.Code != http.StatusOK {
		t.Fatalf("an empty audit table answered %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok (0 entries)") {
		t.Fatalf("empty chain = %s", w.Body.String())
	}
}
