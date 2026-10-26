package server

import (
	"net"
	"testing"
	"time"
)

// TestRPortFwdAcceptLoopAcceptsAndStops covers the reverse-port-forward accept
// loop: it serves operator connections through the relay, and closing the
// listener (the stop path) makes it return instead of spinning.
func TestRPortFwdAcceptLoopAcceptsAndStops(t *testing.T) {
	s := mustTenantServer(t)
	r := newRPortFwdRelay(s, "rp-agent", 0, "127.0.0.1:445")
	if err := r.bind(); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if r.listener == nil || r.listener.ln == nil {
		t.Fatal("relay did not bind a listener")
	}
	addr := r.listener.ln.Addr().String()

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.acceptLoop()
	}()

	// A connection must be accepted and handed to handleConn (which will close
	// it once the agent side is unavailable — fine for this test).
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	_ = conn.Close()

	// Closing the listener is the stop signal; the loop must return.
	close(r.stopCh)
	_ = r.listener.ln.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop did not return after the listener was closed")
	}
}

// TestRPortFwdAcceptLoopWithoutListener covers the guard: a relay that never
// bound returns immediately instead of panicking on a nil listener.
func TestRPortFwdAcceptLoopWithoutListener(t *testing.T) {
	s := mustTenantServer(t)
	r := newRPortFwdRelay(s, "rp-agent", 0, "10.0.0.1:445")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		r.acceptLoop()
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptLoop with no listener did not return")
	}
}
