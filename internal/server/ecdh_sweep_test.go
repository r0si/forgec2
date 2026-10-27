package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
)

// TestSweepECDHSessions covers the interval-aware eviction: an agent that has
// been silent longer than max(30m, 3x its sleep interval) loses its session, an
// agent inside that window keeps it, and a long-sleep agent is not evicted just
// because its interval exceeds the floor.
func TestSweepECDHSessions(t *testing.T) {
	s := mustTenantServer(t)
	sm, err := crypto.NewSessionManager()
	if err != nil {
		t.Fatal(err)
	}
	s.sessionManager = sm

	// Sessions for three agents with different sleep intervals.
	agents := []db.Implant{
		{ID: "chatty", TenantID: 1, Hostname: "chatty", CurrentInterval: 60, LastSeen: time.Now()},
		{ID: "longsleep", TenantID: 1, Hostname: "longsleep", CurrentInterval: 3600, LastSeen: time.Now()},
		{ID: "unknown", TenantID: 1, Hostname: "unknown", LastSeen: time.Now()},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range agents {
		// EstablishSession needs an agent public key; a random 32-byte key is
		// enough for the map entry this sweep reads.
		key := make([]byte, 32)
		for j := range key {
			key[j] = byte(j + 1)
		}
		if err := sm.EstablishSession(a.ID, key); err != nil {
			t.Fatalf("establish %s: %v", a.ID, err)
		}
	}
	if len(sm.SessionsLastUsed()) != 3 {
		t.Fatalf("setup produced %d sessions", len(sm.SessionsLastUsed()))
	}

	// Age the sessions: chatty and unknown are past the floor (30m); longsleep
	// is past the floor but inside 3x its interval (3h).
	now := time.Now()
	sm.SessionsLastUsed() // snapshot only; age them through the exported path:
	sm.SetSessionLastUsedForTest("chatty", now.Add(-45*time.Minute))
	sm.SetSessionLastUsedForTest("unknown", now.Add(-45*time.Minute))
	sm.SetSessionLastUsedForTest("longsleep", now.Add(-45*time.Minute))

	s.sweepECDHSessions()

	live := sm.SessionsLastUsed()
	if _, ok := live["chatty"]; ok {
		t.Error("a chatty agent idle past the floor kept its session")
	}
	if _, ok := live["unknown"]; ok {
		t.Error("an agent with no interval row kept its session past the floor")
	}
	if _, ok := live["longsleep"]; !ok {
		t.Error("a long-sleep agent was evicted inside 3x its interval")
	}

	// A second sweep with nothing idle is a no-op.
	s.sweepECDHSessions()
	if len(sm.SessionsLastUsed()) != 1 {
		t.Fatalf("second sweep changed the session count: %d", len(sm.SessionsLastUsed()))
	}
}

// TestSweepECDHSessionsGuards covers the early returns: no manager, no
// database, and an empty session table must all be silent no-ops.
func TestSweepECDHSessionsGuards(t *testing.T) {
	s := &Server{}
	s.sweepECDHSessions() // no manager and no db

	s2 := mustTenantServer(t)
	sm, err := crypto.NewSessionManager()
	if err != nil {
		t.Fatal(err)
	}
	s2.sessionManager = sm
	s2.sweepECDHSessions() // manager present, no sessions

	s3 := mustTenantServer(t)
	s3.sweepECDHSessions() // no manager
	_ = fmt.Sprint()
}

// TestBlockedReasonSuffix pins the audit-detail helper: a blank reason must not
// produce dangling parentheses.
func TestBlockedReasonSuffix(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"   ":         "",
		"\t\n":        "",
		"policy":      " (policy)",
		" ops window": " ( ops window)",
	}
	for in, want := range cases {
		if got := blockedReasonSuffix(in); got != want {
			t.Errorf("blockedReasonSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
