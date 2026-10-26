package server

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestSocksAcceptLoopAcceptsAndStops covers the accept loop: it accepts
// operator connections handed to the handshake, and a cancelled context makes it
// return instead of spinning on a closed listener.
func TestSocksAcceptLoopAcceptsAndStops(t *testing.T) {
	s := mustTenantServer(t)
	e := newSocksRelayEngine()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess := &socksRelaySession{agentID: "acc-agent", listener: ln, ctx: ctx, cancel: cancel, dbID: 1}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.acceptLoop(s, sess)
	}()

	// Two connections must be accepted (each then fails the handshake because
	// the client says nothing, which is fine for this test).
	for i := 0; i < 2; i++ {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
		if err != nil {
			cancel()
			_ = ln.Close()
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = conn.Close()
	}

	// Cancelling and closing the listener must stop the loop.
	cancel()
	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop did not return after the context was cancelled")
	}
}
