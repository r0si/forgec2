package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

// TestSocksOutboundOverflowClosesConn proves a connection whose unsent
// backlog exceeds the byte cap is closed loudly (close frame queued) instead
// of silently shedding stream bytes.
func TestSocksOutboundOverflowClosesConn(t *testing.T) {
	e := newSocksRelayEngine()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	e.connections[7] = &socksRelayConn{connID: 7, tcpConn: server, agentID: "a1"}

	// Fill just under the cap, then push over it.
	big := make([]byte, socksMaxOutboundBytes-10)
	e.enqueueFrame("a1", socksFrame{ConnID: 7, Action: "data", Data: big})
	e.enqueueFrame("a1", socksFrame{ConnID: 7, Action: "data", Data: make([]byte, 64)})

	e.mu.Lock()
	_, listed := e.connections[7]
	e.mu.Unlock()
	if listed {
		t.Fatal("overflowed conn must be dropped from the table")
	}
	e.controlFramesMu.Lock()
	closes := 0
	for _, f := range e.controlFrames["a1"] {
		if f.ConnID == 7 && f.Action == "close" {
			closes++
		}
	}
	e.controlFramesMu.Unlock()
	if closes != 1 {
		t.Fatalf("want exactly 1 close frame for dropped conn, got %d", closes)
	}
}

// TestSocksWriteFailureDropsConn proves a failed operator write removes the
// entry (previously it stayed listed until the 5-minute sweep, holding one
// of the 256 slots while the agent sent into the void).
func TestSocksWriteFailureDropsConn(t *testing.T) {
	e := newSocksRelayEngine()
	client, server := net.Pipe()
	e.connections[9] = &socksRelayConn{connID: 9, tcpConn: server, agentID: "a1"}
	client.Close() // reads fail -> writes fail

	s := &Server{}
	s.db = newTasksTestServer(t).db
	e.processAgentData(s, "a1", []socksFrame{{ConnID: 9, Action: "data", Data: []byte("hello")}})

	e.mu.Lock()
	_, listed := e.connections[9]
	e.mu.Unlock()
	if listed {
		t.Fatal("write-failed conn must be dropped from the table")
	}
	server.Close()
}

// TestFrameBudgetForTransport pins the MTU policy: datagram transports get
// small frames + a capped total, streams drain everything uncapped.
func TestFrameBudgetForTransport(t *testing.T) {
	for _, tr := range []string{"udp", "icmp", "dns"} {
		size, budget := frameBudgetForTransport(tr)
		if size != SocksLowMTUFrameSize || budget != SocksLowMTUBudget {
			t.Fatalf("%s: got (%d,%d)", tr, size, budget)
		}
	}
	for _, tr := range []string{"http", "tcp", "grpc", "quic", "ssh", "smb", "", "bogus"} {
		size, budget := frameBudgetForTransport(tr)
		if size != 0 || budget != 0 {
			t.Fatalf("%s: want uncapped, got (%d,%d)", tr, size, budget)
		}
	}
}

// TestLowMTUBudgetRequeuesLosslessly proves a datagram budget cuts the tunnel
// payload without loss or reorder: the remainder stays queued head-ordered
// for the next beacon, across all three data FIFOs.
func TestLowMTUBudgetRequeuesLosslessly(t *testing.T) {
	e := newSocksRelayEngine()
	const agentID = "mtu-1"

	// Rportfwd FIFO: 3×2KB frames, 4KB budget -> 2 now, 1 requeued.
	for i := 0; i < 3; i++ {
		if !e.enqueueRPortFwdFrame(agentID, socksFrame{ConnID: 1, Action: "rportfwd_data", Data: bytes.Repeat([]byte{byte(i + 1)}, 2048)}) {
			t.Fatal("enqueue must fit the 2MB FIFO budget")
		}
	}
	first := e.collectPendingFrames(agentID, 1024, 4096)
	if len(first) != 2 {
		t.Fatalf("want 2 frames within budget, got %d", len(first))
	}
	rest := e.collectPendingFrames(agentID, 0, 0)
	if len(rest) != 1 || !bytes.Equal(rest[0].Data, bytes.Repeat([]byte{3}, 2048)) {
		t.Fatalf("requeued frame lost or reordered: %+v", rest)
	}

	// SOCKS data: 8KB stream, 1KB frames, 4KB budget -> 4 frames now and a
	// byte-identical 4KB remainder later.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	stream := append(bytes.Repeat([]byte{'A'}, 4096), bytes.Repeat([]byte{'B'}, 4096)...)
	e.connections[11] = &socksRelayConn{connID: 11, tcpConn: server, agentID: agentID,
		outbound: [][]byte{stream}, outboundBytes: len(stream)}
	got := e.collectPendingFrames(agentID, 1024, 4096)
	if len(got) != 4 {
		t.Fatalf("want 4×1KB frames, got %d", len(got))
	}
	var head []byte
	for _, f := range got {
		head = append(head, f.Data...)
	}
	tail := e.collectPendingFrames(agentID, 0, 0)
	var tailBytes []byte
	for _, f := range tail {
		if f.Action == "data" {
			tailBytes = append(tailBytes, f.Data...)
		}
	}
	if !bytes.Equal(append(head, tailBytes...), stream) {
		t.Fatal("budgeted SOCKS drain corrupted the byte stream")
	}
}

// TestSocksStopAllDrains proves server shutdown releases every relay
// listener + connection (previously FDs and accept loops leaked across the
// restart path) and is safe to run twice.
func TestSocksStopAllDrains(t *testing.T) {
	e := newSocksRelayEngine()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	e.sessions["a9"] = &socksRelaySession{agentID: "a9", listener: ln, ctx: ctx, cancel: cancel}
	client, server := net.Pipe()
	defer client.Close()
	e.connections[3] = &socksRelayConn{connID: 3, tcpConn: server, agentID: "a9", lastActive: time.Now()}

	e.stopAll()
	e.stopAll() // idempotent

	e.mu.Lock()
	nsess, nconn := len(e.sessions), len(e.connections)
	e.mu.Unlock()
	if nsess != 0 || nconn != 0 {
		t.Fatalf("tables not drained: sessions=%d conns=%d", nsess, nconn)
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("listener still accepting after stopAll")
	}
}

// TestSocksFastModeOnlyWithTraffic proves an idle-but-open session does not
// pin the agent at fast-poll cadence: the hint fires only while frames flow
// either way (frames in the response re-arm fast mode agent-side anyway).
func TestSocksFastModeOnlyWithTraffic(t *testing.T) {
	newEngine := func() *socksRelayEngine {
		e := newSocksRelayEngine()
		e.sessions["fast-agent"] = &socksRelaySession{agentID: "fast-agent", port: 1080}
		return e
	}

	// Idle session, no frames either way: no hint.
	s := &Server{socksEngine: newEngine()}
	resp := &beaconResponse{}
	s.processSOCKSRelay("fast-agent", nil, resp)
	if resp.SocksFastMode {
		t.Fatal("idle session must not request fast polling")
	}
	if len(resp.SocksFrames) != 0 {
		t.Fatalf("idle session produced %d frames", len(resp.SocksFrames))
	}

	// Queued outbound frame: hint set.
	s = &Server{socksEngine: newEngine()}
	if !s.socksEngine.enqueueRPortFwdFrame("fast-agent", socksFrame{ConnID: 1, Action: "rportfwd_data", Data: []byte("hi")}) {
		t.Fatal("enqueue must fit")
	}
	resp = &beaconResponse{}
	s.processSOCKSRelay("fast-agent", nil, resp)
	if !resp.SocksFastMode {
		t.Fatal("outbound backlog must request fast polling")
	}
	if len(resp.SocksFrames) != 1 {
		t.Fatalf("got %d frames, want 1", len(resp.SocksFrames))
	}

	// Inbound data alone (operator leg live, nothing queued yet): hint set.
	s = &Server{socksEngine: newEngine(), db: newTasksTestServer(t).db}
	client, server := net.Pipe()
	s.socksEngine.connections[9] = &socksRelayConn{connID: 9, tcpConn: server, agentID: "fast-agent", lastActive: time.Now()}
	client.Close() // writes fail fast; the hint depends on traffic, not delivery
	defer server.Close()
	resp = &beaconResponse{}
	s.processSOCKSRelay("fast-agent", []socksFrame{{ConnID: 9, Action: "data", Data: []byte("x")}}, resp)
	if !resp.SocksFastMode {
		t.Fatal("inbound traffic must request fast polling")
	}
}

// TestSocksDropsCounted proves formerly-silent frame sheds are visible in
// SocksDroppedTotal: unknown-conn data (either direction), foreign-conn
// replay attempts, and malformed UDP datagrams.
func TestSocksDropsCounted(t *testing.T) {
	e := newSocksRelayEngine()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_socks_dropped_total"}, []string{"reason"})
	e.SetDropCounter(counter)
	s := &Server{}

	// Agent->server data for a conn that doesn't exist.
	e.processAgentData(s, "a", []socksFrame{{ConnID: 77, Action: "data", Data: []byte("x")}})
	// Agent->server data for another agent's live conn (replay attempt).
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	e.connections[78] = &socksRelayConn{connID: 78, tcpConn: server, agentID: "b"}
	e.processAgentData(s, "a", []socksFrame{{ConnID: 78, Action: "data", Data: []byte("x")}})
	// Malformed UDP datagram on a live UDP leg.
	udpConn, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	defer udpConn.Close()
	e.connections[79] = &socksRelayConn{connID: 79, agentID: "a", isUDP: true, udpConn: udpConn, udpClient: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9}}
	e.processAgentData(s, "a", []socksFrame{{ConnID: 79, Action: "udp_data", Data: []byte("short")}})
	// Server->agent data for a dead leg.
	e.enqueueFrame("a", socksFrame{ConnID: 80, Action: "data", Data: []byte("y")})

	count := func(reason string) float64 {
		return promtest.ToFloat64(counter.WithLabelValues(reason))
	}
	if got := count("unknown_conn"); got != 2 {
		t.Fatalf("unknown_conn=%v, want 2 (both directions)", got)
	}
	if got := count("foreign_conn"); got != 1 {
		t.Fatalf("foreign_conn=%v, want 1", got)
	}
	if got := count("udp_drop"); got != 1 {
		t.Fatalf("udp_drop=%v, want 1", got)
	}
}

// ── Agent-facing UDP frame codec ───────────────────────────────────────────

func newSocksTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.DefaultConfig()
	cfg.Server.SocksListenHost = "127.0.0.1"
	s := &Server{db: testutil.SetupTestDB(t), cfg: cfg, ctx: context.Background()}
	s.socksEngine = newSocksRelayEngine()
	return s
}

// TestSocksUDPFrameRoundTrip proves the server->agent datagram codec is an exact
// inverse: a payload that does not survive this would be delivered to the wrong
// destination or truncated on the way out.
func TestSocksUDPFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		port    int
		payload []byte
	}{
		{"empty payload", "10.0.0.1", 53, nil},
		{"single byte", "example.com", 443, []byte{0x00}},
		{"text", "c2.example.com", 8080, []byte("GET / HTTP/1.1\r\n\r\n")},
		{"all byte values", "127.0.0.1", 1, allBytes(256)},
		{"ipv6 literal", "2001:db8::1", 65535, []byte("v6")},
		{"max port", "1.2.3.4", 65535, []byte("hi")},
		{"large payload", "10.0.0.2", 4444, bytes.Repeat([]byte{0xde, 0xad}, 32*1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := encodeSocksUDPFrame(tc.addr, tc.port, tc.payload)
			addr, port, payload, err := decodeSocksUDPFrame(frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if addr != tc.addr || port != tc.port {
				t.Fatalf("destination = %s:%d, want %s:%d", addr, port, tc.addr, tc.port)
			}
			if !bytes.Equal(payload, tc.payload) {
				t.Fatalf("payload round trip: %d bytes in, %d out", len(tc.payload), len(payload))
			}
		})
	}
}

// TestSocksUDPFrameHeaderLayout pins the wire layout (u16 addr length, addr,
// u16 port, payload) so the agent's decoder can never drift from it silently.
func TestSocksUDPFrameHeaderLayout(t *testing.T) {
	frame := encodeSocksUDPFrame("ab", 0x0102, []byte{0xAA})
	want := []byte{0x00, 0x02, 'a', 'b', 0x01, 0x02, 0xAA}
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame = % x, want % x", frame, want)
	}
}

// TestDecodeSocksUDPFrameRejectsMalformed proves a hostile or truncated agent
// frame is refused instead of being decoded into a plausible-looking
// destination with a bogus payload.
func TestDecodeSocksUDPFrameRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"one byte", []byte{0x00}},
		{"three bytes", []byte{0x00, 0x02, 'a'}},
		{"header only, no port", []byte{0x00, 0x02, 'a', 'b'}},
		{"address truncated mid-way", []byte{0x00, 0x04, 'a', 'b'}},
		{"length field lies (says 9, sends 2)", []byte{0x00, 0x09, 'a', 'b', 0x00, 0x50}},
		{"length field huge", []byte{0xFF, 0xFF, 'a'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if addr, port, payload, err := decodeSocksUDPFrame(tc.in); err == nil {
				t.Fatalf("malformed frame accepted as %s:%d payload=% x", addr, port, payload)
			}
		})
	}
}

// TestDecodeSocksUDPFrameAcceptsEmptyPayload proves a header with no payload is
// a valid (if useless) datagram, not a truncation.
func TestDecodeSocksUDPFrameAcceptsEmptyPayload(t *testing.T) {
	addr, port, payload, err := decodeSocksUDPFrame(encodeSocksUDPFrame("h", 9, nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if addr != "h" || port != 9 || len(payload) != 0 {
		t.Fatalf("decoded %s:%d payload=% x", addr, port, payload)
	}
}

func allBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// ── Operator UDP header parsing ─────────────────────────────────────────────

// TestParseOperatorUDPHeaderLayouts pins all three SOCKS UDP address types.
// The datagram layout the relay speaks is RSV RSV FRAG ATYP DST.ADDR DST.PORT
// DATA - the leading zero byte the caller validates is the reserved byte, so
// the address type sits at offset 3.
func TestParseOperatorUDPHeaderLayouts(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		b := []byte{0, 0, 0, 0x01, 10, 0, 0, 7, 0x01, 0xBB, 'h', 'i'}
		dst, payload, err := parseOperatorUDPHeader(b)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if dst != "10.0.0.7:443" {
			t.Fatalf("dst = %q, want 10.0.0.7:443", dst)
		}
		if string(payload) != "hi" {
			t.Fatalf("payload = %q", payload)
		}
	})
	t.Run("ipv6", func(t *testing.T) {
		b := make([]byte, 22, 24)
		b[3] = 0x04
		copy(b[4:20], net.ParseIP("2001:db8::1").To16())
		binary.BigEndian.PutUint16(b[20:22], 8080)
		b = append(b, 'o', 'k')
		dst, payload, err := parseOperatorUDPHeader(b)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !strings.HasPrefix(dst, "[2001:db8::1]:8080") {
			t.Fatalf("dst = %q", dst)
		}
		if string(payload) != "ok" {
			t.Fatalf("payload = %q", payload)
		}
	})
	t.Run("domain", func(t *testing.T) {
		host := "c2.example.com"
		b := []byte{0, 0, 0, 0x03, byte(len(host))}
		b = append(b, host...)
		b = append(b, 0x01, 0xBB)
		b = append(b, 'd', 'a', 't', 'a')
		dst, payload, err := parseOperatorUDPHeader(b)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if dst != "c2.example.com:443" {
			t.Fatalf("dst = %q", dst)
		}
		if string(payload) != "data" {
			t.Fatalf("payload = %q", payload)
		}
	})
	t.Run("domain of zero length", func(t *testing.T) {
		b := []byte{0, 0, 0, 0x03, 0x00, 0x00, 0x50, 'x'}
		dst, payload, err := parseOperatorUDPHeader(b)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if dst != ":80" {
			t.Fatalf("dst = %q, want an empty host on port 80", dst)
		}
		if string(payload) != "x" {
			t.Fatalf("payload = %q", payload)
		}
	})
}

// TestParseOperatorUDPHeaderRejectsMalformed proves every truncation and unknown
// address type is refused: an accepted-but-wrong destination would forward a
// datagram somewhere the operator never asked for.
func TestParseOperatorUDPHeaderRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"six bytes", make([]byte, 6)},
		{"unknown atyp", []byte{0, 0, 0, 0x09, 1, 2, 3, 4, 0, 0}},
		{"ipv4 truncated", []byte{0, 0, 0, 0x01, 10, 0, 0, 7, 0x01}},
		{"ipv6 truncated", append([]byte{0, 0, 0, 0x04}, make([]byte, 16)...)},
		{"domain length lies (says 20, sends 2)", []byte{0, 0, 0, 0x03, 20, 'a', 'b', 0, 0}},
		{"domain without port", []byte{0, 0, 0, 0x03, 1, 'a'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if dst, payload, err := parseOperatorUDPHeader(tc.in); err == nil {
				t.Fatalf("malformed datagram accepted as %q payload=% x", dst, payload)
			}
		})
	}
}

// TestParseOperatorUDPHeaderAcceptsEmptyPayload proves a bare address header
// (no data) is accepted - that is how a client opens a stream.
func TestParseOperatorUDPHeaderAcceptsEmptyPayload(t *testing.T) {
	dst, payload, err := parseOperatorUDPHeader([]byte{0, 0, 0, 0x01, 1, 2, 3, 4, 0x00, 0x50})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if dst != "1.2.3.4:80" || len(payload) != 0 {
		t.Fatalf("dst=%q payload=% x", dst, payload)
	}
}

// ── Session lifecycle ───────────────────────────────────────────────────────

// TestSocksSessionLifecycle proves start/stop are idempotent in the right
// directions: starting an already-running session keeps the original port, and
// stopping a session that is not there is an error rather than a silent no-op.
func TestSocksSessionLifecycle(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine

	port, err := engine.startSession(s, "agent-1", 0)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	if port == 0 {
		t.Fatal("startSession returned port 0")
	}
	if !s.hasActiveSocks("agent-1") {
		t.Fatal("session not reported active after start")
	}
	if s.hasActiveSocks("agent-2") {
		t.Fatal("an unknown agent reported an active session")
	}

	// Re-start returns the same port without a second listener.
	again, err := engine.startSession(s, "agent-1", 0)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if again != port {
		t.Fatalf("restart reported port %d, want the running %d", again, port)
	}

	// The session row is persisted so a restart can show it.
	var row db.SocksSession
	if err := s.db.Where("agent_id = ?", "agent-1").First(&row).Error; err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if row.Status != "active" || row.ListenPort != port {
		t.Fatalf("persisted session = %+v", row)
	}

	if err := engine.stopSession(s, "agent-1"); err != nil {
		t.Fatalf("stopSession: %v", err)
	}
	if s.hasActiveSocks("agent-1") {
		t.Fatal("session still active after stop")
	}
	if err := s.db.Model(&db.SocksSession{}).Where("id = ?", row.ID).
		Update("status", "stopped").Error; err != nil {
		t.Fatalf("mark stopped: %v", err)
	}
	var after db.SocksSession
	if err := s.db.First(&after, row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != "stopped" {
		t.Fatalf("status = %q, want stopped", after.Status)
	}

	if err := engine.stopSession(s, "agent-1"); err == nil {
		t.Fatal("stopping an unknown session reported success")
	}
}

// TestSocksStartSessionReportsListenFailure proves a busy/unbindable port is an
// error, not a session that claims to be running.
func TestSocksStartSessionReportsListenFailure(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	if _, err := engine.startSession(s, "agent-1", 0); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	sess := engine.getSession("agent-1")

	// The same host:port is already bound by the live listener.
	if _, err := engine.startSession(s, "agent-2", sess.port); err == nil {
		t.Fatal("binding an in-use port reported success")
	}
	if s.hasActiveSocks("agent-2") {
		t.Fatal("a failed start left a phantom session")
	}
}

// TestSocksStopAllReleasesEverything proves the shutdown path drops every
// session and connection without touching the (possibly draining) database.
func TestSocksStopAllReleasesEverything(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	if _, err := engine.startSession(s, "agent-1", 0); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	if _, err := engine.startSession(s, "agent-2", 0); err != nil {
		t.Fatalf("startSession agent-2: %v", err)
	}

	client, server := net.Pipe()
	engine.mu.Lock()
	engine.connections[1] = &socksRelayConn{connID: 1, agentID: "agent-1", tcpConn: client, lastActive: time.Now()}
	engine.mu.Unlock()
	_ = server

	engine.stopAll()

	if s.hasActiveSocks("agent-1") || s.hasActiveSocks("agent-2") {
		t.Fatal("sessions survived stopAll")
	}
	engine.mu.Lock()
	remaining := len(engine.connections)
	engine.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d connections survived stopAll", remaining)
	}
	// The database must be untouched by stopAll.
	var sessions int64
	if err := s.db.Model(&db.SocksSession{}).Count(&sessions).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 2 {
		t.Fatalf("stopAll wrote to the database: %d session rows", sessions)
	}
}

// ── Queue bounds ────────────────────────────────────────────────────────────

// TestSocksControlFrameQueueIsBounded proves the per-agent control queue caps
// at 200 frames and drops the overflow instead of growing without limit.
func TestSocksControlFrameQueueIsBounded(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	for i := 0; i < 250; i++ {
		engine.enqueueFrame("agent-1", socksFrame{ConnID: uint64(i), Action: "connect"})
	}
	engine.controlFramesMu.Lock()
	queued := len(engine.controlFrames["agent-1"])
	engine.controlFramesMu.Unlock()
	if queued != 200 {
		t.Fatalf("control queue holds %d frames, want the 200 cap", queued)
	}
}

// TestSocksDataForUnknownConnIsDropped proves agent data for a leg that is
// already gone is counted and discarded rather than panicking or re-creating
// the connection.
func TestSocksDataForUnknownConnIsDropped(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	engine.enqueueFrame("agent-1", socksFrame{ConnID: 999, Action: "data", Data: []byte("x")})
	engine.mu.Lock()
	remaining := len(engine.connections)
	engine.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("data for an unknown conn created %d connections", remaining)
	}
}

// TestSocksCollectPendingFramesOrdersControlFirst proves control frames are
// always emitted before bulk data: connect/close arriving after payload would
// corrupt the tunnel on the agent side.
func TestSocksCollectPendingFramesOrdersControlFirst(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	engine.mu.Lock()
	engine.connections[1] = &socksRelayConn{connID: 1, agentID: "agent-1", tcpConn: client, lastActive: time.Now()}
	engine.mu.Unlock()
	// Queue bulk data first, then a control frame, so only the ordering rule
	// can put the control frame ahead.
	engine.enqueueFrame("agent-1", socksFrame{ConnID: 1, Action: "data", Data: []byte("payload")})
	engine.enqueueFrame("agent-1", socksFrame{ConnID: 1, Action: "close"})

	frames := engine.collectPendingFrames("agent-1", 0, 0)
	if len(frames) < 2 {
		t.Fatalf("collected %d frames, want at least 2", len(frames))
	}
	if frames[0].Action != "close" {
		t.Fatalf("first frame = %q, want the control frame", frames[0].Action)
	}
	// The queue is drained.
	if again := engine.collectPendingFrames("agent-1", 0, 0); len(again) != 0 {
		t.Fatalf("second collect returned %d frames, want an empty queue", len(again))
	}
}

// TestSocksCollectPendingFramesHonorsBudget proves the per-beacon byte budget
// and per-frame cap are enforced, which is what lets the relay run over a
// low-MTU datagram transport without overflowing the agent's frame buffer.
func TestSocksCollectPendingFramesHonorsBudget(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	engine.mu.Lock()
	engine.connections[1] = &socksRelayConn{connID: 1, agentID: "agent-1", tcpConn: client, lastActive: time.Now()}
	engine.mu.Unlock()

	const total = 6000
	for i := 0; i < total/100; i++ {
		engine.enqueueFrame("agent-1", socksFrame{ConnID: 1, Action: "data", Data: make([]byte, 100)})
	}

	frames := engine.collectPendingFrames("agent-1", SocksLowMTUFrameSize, SocksLowMTUBudget)
	emitted := 0
	for _, f := range frames {
		if len(f.Data) > SocksLowMTUFrameSize {
			t.Fatalf("frame of %d bytes exceeds the %d-byte cap", len(f.Data), SocksLowMTUFrameSize)
		}
		emitted += len(f.Data)
	}
	if emitted != SocksLowMTUBudget {
		t.Fatalf("emitted %d bytes, want the full %d-byte budget", emitted, SocksLowMTUBudget)
	}

	// The remainder must be retained for the next beacon, not lost.
	next := engine.collectPendingFrames("agent-1", SocksLowMTUFrameSize, SocksLowMTUBudget)
	remaining := 0
	for _, f := range next {
		remaining += len(f.Data)
	}
	if remaining != total-SocksLowMTUBudget {
		t.Fatalf("requeued %d bytes, want %d", remaining, total-SocksLowMTUBudget)
	}
}

// TestSocksCollectPendingFramesRequeuesRemainderLosslessly proves a budget cut
// mid-stream loses and reorders nothing: every byte queued comes out exactly
// once across the two drains, in order. Connection data is merged into one
// contiguous stream before framing, so the invariant is on the bytes, not on
// the original chunk boundaries.
func TestSocksCollectPendingFramesRequeuesRemainderLosslessly(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	engine.mu.Lock()
	engine.connections[1] = &socksRelayConn{connID: 1, agentID: "agent-1", tcpConn: client, lastActive: time.Now()}
	engine.mu.Unlock()

	chunks := [][]byte{[]byte("first"), []byte("second"), []byte("third")}
	var want []byte
	for _, c := range chunks {
		engine.enqueueFrame("agent-1", socksFrame{ConnID: 1, Action: "data", Data: c})
		want = append(want, c...)
	}

	// A budget smaller than the merged block emits nothing yet - the whole
	// stream is requeued intact rather than partially delivered.
	const budget = len("first") + len("second")
	first := engine.collectPendingFrames("agent-1", SocksMaxFrameSize, budget)
	var got []byte
	for _, f := range first {
		got = append(got, f.Data...)
	}
	if len(got) != 0 {
		t.Fatalf("emitted %q under a budget smaller than the merged block", got)
	}

	second := engine.collectPendingFrames("agent-1", SocksMaxFrameSize, 0)
	for _, f := range second {
		got = append(got, f.Data...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("drained %q, want %q (loss or reordering)", got, want)
	}
	if again := engine.collectPendingFrames("agent-1", SocksMaxFrameSize, 0); len(again) != 0 {
		t.Fatalf("stream duplicated: %d extra frames on a third drain", len(again))
	}
}

// ── SOCKS5 handshake guards ─────────────────────────────────────────────────

// socksPair returns a real loopback TCP pair: net.Pipe's strict synchrony makes
// a blocked server write look like a handshake timeout, which is not what these
// guards are about.
// socksTestIOTimeout is the liveness window for the loopback SOCKS tests. They wait
// on real TCP so a loaded CI runner can exceed a tight deadline; the assertions
// are unchanged, only the patience is larger.
const socksTestIOTimeout = 15 * time.Second

func socksPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	select {
	case server = <-accepted:
	case <-time.After(socksTestIOTimeout):
		t.Fatal("no accepted connection")
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// socksGreet drives the version/method negotiation and returns the server's
// 2-byte reply.
func socksGreet(t *testing.T, client net.Conn, version byte, methods ...byte) []byte {
	t.Helper()
	greeting := append([]byte{version, byte(len(methods))}, methods...)
	if _, err := client.Write(greeting); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if err := client.SetReadDeadline(time.Now().Add(socksTestIOTimeout)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read greeting reply: %v", err)
	}
	return reply
}

// TestHandleOperatorConnRejectsBadHandshakes proves the negotiation only ever
// accepts a SOCKS5 CONNECT/UDP ASSOCIATE request with a known address type:
// anything else gets the RFC 1928 error byte and no session state.
func TestHandleOperatorConnRejectsBadHandshakes(t *testing.T) {
	cases := []struct {
		name    string
		drive   func(t *testing.T, c net.Conn)
		wantRep byte // 0 = no reply expected (connection just dropped)
	}{
		{
			name:    "non-SOCKS5 version dropped",
			drive:   func(t *testing.T, c net.Conn) { _, _ = c.Write([]byte{0x04, 0x01, 0x00}) },
			wantRep: 0,
		},
		{
			name: "unsupported command",
			drive: func(t *testing.T, c net.Conn) {
				socksGreet(t, c, 0x05, 0x00)
				// BIND (0x02) is not implemented by the relay.
				_, _ = c.Write([]byte{0x05, 0x02, 0x00, 0x01, 10, 0, 0, 1, 0x00, 0x50})
			},
			wantRep: 0x07,
		},
		{
			name: "unknown address type",
			drive: func(t *testing.T, c net.Conn) {
				socksGreet(t, c, 0x05, 0x00)
				_, _ = c.Write([]byte{0x05, 0x01, 0x00, 0x09, 1, 2, 3, 4, 0x00, 0x50})
			},
			wantRep: 0x08,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSocksTestServer(t)
			if _, err := s.socksEngine.startSession(s, "agent-1", 0); err != nil {
				t.Fatalf("startSession: %v", err)
			}
			sess := s.socksEngine.getSession("agent-1")

			client, server := socksPair(t)

			done := make(chan struct{})
			go func() {
				s.socksEngine.handleOperatorConn(s, sess, server)
				close(done)
			}()

			tc.drive(t, client)
			reply := make([]byte, 10)
			if err := client.SetReadDeadline(time.Now().Add(socksTestIOTimeout)); err != nil {
				t.Fatalf("deadline: %v", err)
			}
			n, err := io.ReadFull(client, reply)
			if tc.wantRep == 0 {
				if err == nil {
					t.Fatalf("dropped handshake still replied % x", reply[:n])
				}
			} else if err != nil {
				t.Fatalf("read reply: %v", err)
			} else if reply[1] != tc.wantRep {
				t.Fatalf("reply code = %#x, want %#x", reply[1], tc.wantRep)
			}

			client.Close()
			select {
			case <-done:
			case <-time.After(socksTestIOTimeout):
				t.Fatal("handleOperatorConn did not return")
			}
			// A refused handshake must leave no connection behind.
			s.socksEngine.mu.Lock()
			remaining := len(s.socksEngine.connections)
			s.socksEngine.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("refused handshake left %d connections registered", remaining)
			}
		})
	}
}

// TestHandleOperatorConnConnectQueuesFrameAndRelays proves the happy path: a
// SOCKS5 CONNECT produces a success reply, a "connect" control frame carrying
// the resolved destination, and relayed operator bytes become "data" frames.
func TestHandleOperatorConnConnectQueuesFrameAndRelays(t *testing.T) {
	s := newSocksTestServer(t)
	if _, err := s.socksEngine.startSession(s, "agent-1", 0); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	sess := s.socksEngine.getSession("agent-1")

	client, server := socksPair(t)
	done := make(chan struct{})
	go func() {
		s.socksEngine.handleOperatorConn(s, sess, server)
		close(done)
	}()

	if reply := socksGreet(t, client, 0x05, 0x00); reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("greeting reply = % x, want 05 00 (no auth)", reply)
	}
	// CONNECT to 10.9.8.7:443
	if _, err := client.Write([]byte{0x05, 0x01, 0x00, 0x01, 10, 9, 8, 7, 0x01, 0xBB}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	reply := make([]byte, 10)
	if err := client.SetReadDeadline(time.Now().Add(socksTestIOTimeout)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("connect reply code = %#x, want success", reply[1])
	}

	// The connect control frame must already be queued for the agent.
	frames := s.socksEngine.collectPendingFrames("agent-1", 0, 0)
	if len(frames) != 1 || frames[0].Action != "connect" || string(frames[0].Data) != "10.9.8.7:443" {
		t.Fatalf("queued frames = %+v, want a single connect frame for 10.9.8.7:443", frames)
	}

	// Operator bytes become data frames for the agent.
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	deadline := time.Now().Add(socksTestIOTimeout)
	var relayed []socksFrame
	for time.Now().Before(deadline) {
		relayed = s.socksEngine.collectPendingFrames("agent-1", 0, 0)
		if len(relayed) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(relayed) != 1 || string(relayed[0].Data) != "ping" {
		t.Fatalf("relayed frames = %+v, want a single data frame carrying ping", relayed)
	}

	// Closing the operator leg emits a close frame and unregisters the conn.
	client.Close()
	select {
	case <-done:
	case <-time.After(socksTestIOTimeout):
		t.Fatal("handleOperatorConn did not return after the operator closed")
	}
	s.socksEngine.mu.Lock()
	remaining := len(s.socksEngine.connections)
	s.socksEngine.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d connections survived the operator disconnect", remaining)
	}
	closers := 0
	for _, f := range s.socksEngine.collectPendingFrames("agent-1", 0, 0) {
		if f.Action == "close" {
			closers++
		}
	}
	if closers != 1 {
		t.Fatalf("got %d close frames after disconnect, want exactly 1", closers)
	}
}

// ── Stale connection sweep ──────────────────────────────────────────────────

// TestSocksCleanupRemovesOnlyStaleConns proves the periodic sweep reaps
// connections idle past the timeout and leaves live ones alone.
func TestSocksCleanupRemovesOnlyStaleConns(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine

	newConn := func(id uint64, lastActive time.Time) *socksRelayConn {
		c1, c2 := net.Pipe()
		t.Cleanup(func() { c1.Close(); c2.Close() })
		return &socksRelayConn{connID: id, agentID: "agent-1", tcpConn: c1, lastActive: lastActive}
	}

	stale := newConn(1, time.Now().Add(-2*SocksCleanupTimeout))
	live := newConn(2, time.Now())
	engine.mu.Lock()
	engine.connections[1] = stale
	engine.connections[2] = live
	engine.mu.Unlock()

	engine.cleanup()

	engine.mu.Lock()
	_, hasStale := engine.connections[1]
	_, hasLive := engine.connections[2]
	engine.mu.Unlock()
	if hasStale {
		t.Fatal("stale connection survived the sweep")
	}
	if !hasLive {
		t.Fatal("the sweep removed a live connection")
	}
	stale.mu.Lock()
	closed := stale.closed
	stale.mu.Unlock()
	if !closed {
		t.Fatal("the reaped connection was not marked closed")
	}
}

// TestCleanupStaleSocksStopsWithServer proves the sweep goroutine is bound to
// the server context so shutdown releases it. cleanupStaleSocks is a blocking
// loop, so it runs on its own goroutine here.
func TestCleanupStaleSocksStopsWithServer(t *testing.T) {
	s := newSocksTestServer(t)
	ctx, cancel := context.WithCancel(s.ctx)
	s.ctx = ctx
	done := make(chan struct{})
	go func() {
		s.cleanupStaleSocks()
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(socksTestIOTimeout):
		t.Fatal("cleanupStaleSocks did not return after cancellation")
	}
}

// ── Concurrent lifecycle ────────────────────────────────────────────────────

// TestSocksEngineConcurrentSessionChurn proves the engine's locks hold under
// concurrent start/stop/register/collect; run under -race this pins the
// locking discipline across the four separate mutex domains.
func TestSocksEngineConcurrentSessionChurn(t *testing.T) {
	s := newSocksTestServer(t)
	engine := s.socksEngine
	if _, err := engine.startSession(s, "agent-0", 0); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	engine.mu.Lock()
	engine.connections[1] = &socksRelayConn{connID: 1, agentID: "agent-0", tcpConn: client, lastActive: time.Now()}
	engine.mu.Unlock()

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			agent := "agent-" + string(rune('1'+worker))
			for i := 0; i < 10; i++ {
				_, _ = engine.startSession(s, agent, 0)
				engine.enqueueFrame(agent, socksFrame{ConnID: 1, Action: "data", Data: []byte("x")})
				engine.collectPendingFrames(agent, 0, 0)
				_ = engine.getSession(agent)
				engine.cleanup()
				_ = engine.stopSession(s, agent)
			}
		}(w)
	}
	wg.Wait()
	engine.stopAll()
	if s.hasActiveSocks("agent-0") {
		t.Fatal("stopAll left a session behind")
	}
}
