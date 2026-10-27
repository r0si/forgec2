package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestNTLMRelayStoreOnePerAgent pins the replacement rule: a second relay start
// on the same agent must not leave an orphan session behind. The stop handler
// looks sessions up by agent, so an orphan was unreachable — it stayed "active"
// forever.
func TestNTLMRelayStoreOnePerAgent(t *testing.T) {
	st := newNTLMRelayStore()
	st.add(&ntlmRelaySession{AgentID: "a1", Target: "first", StartedAt: time.Now()})
	first := st.findByAgent("a1")
	if first == nil {
		t.Fatal("the first session was not stored")
	}

	st.add(&ntlmRelaySession{AgentID: "a1", Target: "second", StartedAt: time.Now()})
	if got := len(st.list()); got != 1 {
		t.Fatalf("sessions for one agent = %d, want 1", got)
	}
	if got := st.findByAgent("a1"); got == nil || got.Target != "second" {
		t.Fatalf("the new session did not replace the old one: %+v", got)
	}
	if st.remove(first.ID) {
		t.Fatal("the replaced session id is still removable")
	}

	// A different agent still gets its own session.
	st.add(&ntlmRelaySession{AgentID: "a2", Target: "third", StartedAt: time.Now()})
	if got := len(st.list()); got != 2 {
		t.Fatalf("sessions = %d, want 2", got)
	}
}

// TestNTLMRelayStoreSweep pins the age sweep: a session that was never stopped
// is dropped once it is older than the threshold, and a fresh one is kept.
func TestNTLMRelayStoreSweep(t *testing.T) {
	st := newNTLMRelayStore()
	now := time.Now()
	st.add(&ntlmRelaySession{AgentID: "old", Target: "t", StartedAt: now.Add(-2 * ntlmRelaySessionMaxAge)})
	st.add(&ntlmRelaySession{AgentID: "fresh", Target: "t", StartedAt: now})

	if dropped := st.sweep(now, ntlmRelaySessionMaxAge); dropped != 1 {
		t.Fatalf("swept %d sessions, want 1", dropped)
	}
	if st.findByAgent("old") != nil {
		t.Fatal("the stale session survived")
	}
	if st.findByAgent("fresh") == nil {
		t.Fatal("the fresh session was swept")
	}
	if dropped := st.sweep(now, ntlmRelaySessionMaxAge); dropped != 0 {
		t.Fatalf("a second sweep dropped %d", dropped)
	}
}

// TestNTLMRelayStatusHidesStaleSessions is the end-to-end guard: the status
// endpoint must not report a relay that is no longer running, and the nil-store
// path must still answer instead of 500ing.
func TestNTLMRelayStatusHidesStaleSessions(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "66666666-6666-6666-6666-666666666666"
	seedIOCAgent(t, s, agentID)
	// mustTenantServer does not build the store (NewServer does), so wire it the
	// way startup does; the nil-store path is covered separately below.
	s.ntlmRelays = newNTLMRelayStore()

	s.ntlmRelays.add(&ntlmRelaySession{AgentID: agentID, Target: "stale", StartedAt: time.Now().Add(-2 * ntlmRelaySessionMaxAge)})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ntlm/relay_status", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleNTLMRelayStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var out struct {
		Count   int  `json:"count"`
		Running bool `json:"running"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Count != 0 || out.Running {
		t.Fatalf("a stale relay is still reported as running: %+v", out)
	}

	// A fresh session is reported.
	s.ntlmRelays.add(&ntlmRelaySession{AgentID: agentID, Target: "live", StartedAt: time.Now()})
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ntlm/relay_status", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleNTLMRelayStatus(c)
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Count != 1 || !out.Running {
		t.Fatalf("a live relay was not reported: %+v", out)
	}

	// The nil store answers instead of failing.
	bare := &Server{}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ntlm/relay_status", nil)
	bare.handleNTLMRelayStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("nil store status = %d", w.Code)
	}
}
