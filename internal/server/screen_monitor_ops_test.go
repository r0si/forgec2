package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestReserveScreenMonitorOpExpires pins the fix for a permanent lockout: the
// handlers clear their start/stop reservation on the success and error paths,
// but a panic unwinds the handler and skips the clear. With no sweep anywhere,
// that agent could never start or stop its stream again.
func TestReserveScreenMonitorOpExpires(t *testing.T) {
	s := mustTenantServer(t)
	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()

	// A fresh reservation blocks, and reports which operation is in flight.
	if inFlight := s.reserveScreenMonitorOp("a", "start"); inFlight != "" {
		t.Fatalf("the first reservation reported %q in flight", inFlight)
	}
	if inFlight := s.reserveScreenMonitorOp("a", "stop"); inFlight != "start" {
		t.Fatalf("a second reservation reported %q, want start", inFlight)
	}

	// An expired reservation is cleared instead of blocking forever.
	s.screenMonitorOps["a"] = screenMonitorOp{kind: "start", at: time.Now().Add(-2 * screenMonitorOpTTL)}
	if inFlight := s.reserveScreenMonitorOp("a", "stop"); inFlight != "" {
		t.Fatalf("an expired reservation still blocked: %q", inFlight)
	}
	op := s.screenMonitorOps["a"]
	s.screenMonitorMu.Unlock()
	if op.kind != "stop" {
		t.Fatalf("the new reservation is %q, want stop", op.kind)
	}

	// A different agent is unaffected.
	s.screenMonitorMu.Lock()
	if inFlight := s.reserveScreenMonitorOp("b", "start"); inFlight != "" {
		t.Fatalf("another agent was blocked: %q", inFlight)
	}
	s.screenMonitorMu.Unlock()
}

// TestScreenMonitorStartClearsExpiredReservation is the end-to-end consequence:
// an agent whose previous start panicked can start again.
func TestScreenMonitorStartClearsExpiredReservation(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "77777777-7777-7777-7777-777777777777"
	seedIOCAgent(t, s, agentID)

	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorOps[strings.ToLower(agentID)] = screenMonitorOp{kind: "start", at: time.Now().Add(-2 * screenMonitorOpTTL)}
	s.screenMonitorMu.Unlock()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/screen-monitor/start", strings.NewReader("interval=1&quality=50"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleStartScreenMonitor(c)

	if w.Code == http.StatusConflict {
		t.Fatalf("the expired reservation still blocked the start: %s", w.Body.String())
	}
	// Whatever the outcome, the stale reservation must be gone.
	s.screenMonitorMu.Lock()
	op, still := s.screenMonitorOps[strings.ToLower(agentID)]
	s.screenMonitorMu.Unlock()
	if still && time.Since(op.at) > screenMonitorOpTTL {
		t.Fatal("the expired reservation was not replaced")
	}
}

// TestScreenMonitorStartStillConflictsWhileInFlight pins the other direction: the
// single-flight guard still refuses a genuinely concurrent request.
func TestScreenMonitorStartStillConflictsWhileInFlight(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "88888888-8888-8888-8888-888888888888"
	seedIOCAgent(t, s, agentID)

	s.screenMonitorMu.Lock()
	s.ensureScreenMonitorMapsLocked()
	s.screenMonitorOps[strings.ToLower(agentID)] = screenMonitorOp{kind: "start", at: time.Now()}
	s.screenMonitorMu.Unlock()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/screen-monitor/start", strings.NewReader("interval=1&quality=50"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleStartScreenMonitor(c)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in progress") {
		t.Fatalf("body = %s", w.Body.String())
	}
}
