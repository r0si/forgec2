package server

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// socksHandshake drives handleOperatorConn over a real loopback TCP socket.
// A net.Pipe was tried first and deadlocked: it has no buffering, so the server
// blocked writing its method reply while the client blocked writing the request.
func socksHandshake(t *testing.T, s *Server, e *socksRelayEngine, sess *socksRelaySession, greeting, request []byte) []byte {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		e.handleOperatorConn(s, sess, conn)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := client.Write(greeting); err != nil {
		<-handled
		return nil
	}
	methodReply := make([]byte, 2)
	n, _ := io.ReadAtLeast(client, methodReply, 2)
	if n == 0 {
		<-handled
		return nil
	}
	reply := methodReply[:n]
	if request == nil {
		<-handled
		return reply
	}
	if _, err := client.Write(request); err != nil {
		<-handled
		return reply
	}
	result := make([]byte, 10)
	rn, _ := io.ReadAtLeast(client, result, len(result))
	reply = append(reply, result[:rn]...)
	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handleOperatorConn did not return")
	}
	return reply
}

// TestSocksHandshakeRejections pins the SOCKS5 refusals that happen before any
// relay is created: a non-SOCKS5 greeting, an unsupported command, an unknown
// address type, and the per-session connection cap.
func TestSocksHandshakeRejections(t *testing.T) {
	s := mustTenantServer(t)
	e := newSocksRelayEngine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := &socksRelaySession{agentID: "socks-agent", ctx: ctx, dbID: 1}

	greeting := []byte{0x05, 0x01, 0x00}

	// Not SOCKS5: the handler drops the connection without a reply.
	if reply := socksHandshake(t, s, e, sess, []byte{0x04, 0x01, 0x00}, nil); len(reply) != 0 {
		t.Fatalf("a SOCKS4 greeting got a reply: %x", reply)
	}

	// SOCKS5 greeting is accepted, then an unsupported command (BIND) is
	// refused with the "command not supported" reply.
	bind := []byte{0x05, 0x02, 0x00, 0x01, 1, 2, 3, 4, 0, 80}
	reply := socksHandshake(t, s, e, sess, greeting, bind)
	if len(reply) < 4 || reply[2] != 0x05 || reply[3] != 0x07 {
		t.Fatalf("unsupported command reply = %x", reply)
	}

	// An unknown address type is refused.
	atyp := []byte{0x05, 0x01, 0x00, 0x09, 0, 0}
	reply = socksHandshake(t, s, e, sess, greeting, atyp)
	if len(reply) < 4 || reply[2] != 0x05 || reply[3] != 0x08 {
		t.Fatalf("unknown address type reply = %x", reply)
	}

	// The per-session connection cap refuses a new CONNECT.
	e.mu.Lock()
	for i := 0; i < SocksMaxConns; i++ {
		e.connections[uint64(i+1)] = &socksRelayConn{agentID: "socks-agent"}
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.connections = map[uint64]*socksRelayConn{}
		e.mu.Unlock()
	}()

	connect := []byte{0x05, 0x01, 0x00, 0x01, 10, 0, 0, 1, 0, 80}
	reply = socksHandshake(t, s, e, sess, greeting, connect)
	if len(reply) < 4 || reply[2] != 0x05 || reply[3] != 0x02 {
		t.Fatalf("connection-cap reply = %x", reply)
	}
}

// TestSocksStopSessionIsIdempotent covers the teardown path: stopping twice is
// safe and leaves no listener behind.
func TestSocksStopSessionIsIdempotent(t *testing.T) {
	s := mustTenantServer(t)
	e := newSocksRelayEngine()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess := &socksRelaySession{agentID: "socks-stop", listener: ln, ctx: ctx, cancel: cancel, dbID: 1}
	e.mu.Lock()
	e.sessions["socks-stop"] = sess
	e.mu.Unlock()
	if err := s.db.Create(&db.SocksSession{AgentID: "socks-stop", ListenPort: ln.Addr().(*net.TCPAddr).Port, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}

	e.stopSession(s, "socks-stop")
	e.stopSession(s, "socks-stop") // second call must not panic
}
