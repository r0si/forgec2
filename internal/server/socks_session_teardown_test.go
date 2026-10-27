package server

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestStopSocksSessionDropsQueuedFrames pins the teardown: the per-agent control
// and rportfwd queues are keyed by agent id and nothing else removes the key, so
// stopping a tunnel must clear them — otherwise every agent that ever used a
// tunnel left entries behind, and a re-used id could inherit stale close frames.
func TestStopSocksSessionDropsQueuedFrames(t *testing.T) {
	s := mustTenantServer(t)
	e := s.socksEngine

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	sess := &socksRelaySession{agentID: "agent-q", listener: ln, cancel: cancel, dbID: 12345}

	e.mu.Lock()
	if e.sessions == nil {
		e.sessions = make(map[string]*socksRelaySession)
	}
	e.sessions["agent-q"] = sess
	e.mu.Unlock()

	e.controlFramesMu.Lock()
	if e.controlFrames == nil {
		e.controlFrames = make(map[string][]socksFrame)
	}
	e.controlFrames["agent-q"] = []socksFrame{{ConnID: 1, Action: "open"}}
	e.controlFramesMu.Unlock()

	e.rportfwdMu.Lock()
	if e.rportfwdFrames == nil {
		e.rportfwdFrames = make(map[string][]socksFrame)
		e.rportfwdBytes = make(map[string]int)
	}
	e.rportfwdFrames["agent-q"] = []socksFrame{{ConnID: 2, Action: "rportfwd_connect"}}
	e.rportfwdBytes["agent-q"] = 4
	e.rportfwdMu.Unlock()

	if err := e.stopSession(s, "agent-q"); err != nil {
		t.Fatalf("stopSession: %v", err)
	}

	e.mu.Lock()
	_, sessionLeft := e.sessions["agent-q"]
	e.mu.Unlock()
	if sessionLeft {
		t.Fatal("the session survived the stop")
	}

	e.controlFramesMu.Lock()
	_, controlLeft := e.controlFrames["agent-q"]
	e.controlFramesMu.Unlock()
	if controlLeft {
		t.Fatal("control frames were left behind")
	}

	e.rportfwdMu.Lock()
	_, framesLeft := e.rportfwdFrames["agent-q"]
	_, bytesLeft := e.rportfwdBytes["agent-q"]
	e.rportfwdMu.Unlock()
	if framesLeft || bytesLeft {
		t.Fatal("rportfwd frames were left behind")
	}

	// Stopping an unknown agent is a clean error, not a panic.
	if err := e.stopSession(s, "never-started"); err == nil {
		t.Fatal("stopping an unknown agent reported success")
	}
	_ = time.Now()
}
