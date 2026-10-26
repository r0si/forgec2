package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/gorilla/websocket"
)

// newWSConnPair returns a connected (server-side, client-side) websocket pair
// over an in-process httptest server. The session code only ever writes to the
// server side and closes it, which is exactly what these conns support.
func newWSConnPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(srv.Close)

	dialURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case server = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("server side of the websocket pair never arrived")
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

func readJSONFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("frame is not JSON: %v (%s)", err, raw)
	}
	return out
}

func TestWebRTCHubSessionLifecycle(t *testing.T) {
	cfg := &config.Config{}
	cfg.WebRTC.MaxSessions = 1
	hub := newWebRTCHub(cfg)

	sess, token, err := hub.createSession("agent-1", 7, 1, webrtcStartOptions{Width: 1280}, 0)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	defer sess.end("test_done")

	if sess.ID == "" || token == "" || sess.TurnPass == "" {
		t.Fatalf("missing identifiers: %+v", sess)
	}
	if sess.TokenHash != hashToken(token) {
		t.Fatal("token hash does not match the returned token")
	}
	if sess.TurnUser != "fc2_"+sess.ID {
		t.Fatalf("turn user = %q", sess.TurnUser)
	}
	if !sess.ExpiresAt.After(time.Now()) {
		t.Fatalf("expiry in the past: %v", sess.ExpiresAt)
	}
	if got := hub.byAgentSession("agent-1"); got != sess {
		t.Fatal("session is not registered by agent")
	}

	// One live session per agent.
	if _, _, err := hub.createSession("agent-1", 7, 1, webrtcStartOptions{}, 0); err == nil {
		t.Fatal("second session for the same agent was accepted")
	}
	// And the fleet cap holds.
	if _, _, err := hub.createSession("agent-2", 7, 1, webrtcStartOptions{}, 0); err == nil {
		t.Fatal("session limit was not enforced")
	}

	// TTL clamping: a zero ttl takes the default, an absurd one the maximum.
	mild, _, err := newWebRTCHub(&config.Config{}).createSession("agent-3", 7, 1, webrtcStartOptions{}, 0)
	if err != nil {
		t.Fatalf("create with default ttl: %v", err)
	}
	defer mild.end("test_done")
	if d := time.Until(mild.ExpiresAt); d > webrtcDefaultTTL {
		t.Fatalf("default ttl not applied: %v", d)
	}
	long, _, err := newWebRTCHub(&config.Config{}).createSession("agent-4", 7, 1, webrtcStartOptions{}, time.Hour)
	if err != nil {
		t.Fatalf("create with long ttl: %v", err)
	}
	defer long.end("test_done")
	if d := time.Until(long.ExpiresAt); d > webrtcMaxTTL+time.Minute {
		t.Fatalf("ttl not clamped: %v", d)
	}
}

func TestWebRTCSessionSignalingAndTeardown(t *testing.T) {
	hub := newWebRTCHub(&config.Config{})
	sess, _, err := hub.createSession("agent-sig", 7, 1, webrtcStartOptions{}, 0)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	opSrv, opCli := newWSConnPair(t)
	agSrv, agCli := newWSConnPair(t)

	// The operator attaches before the agent: that must succeed (the operator
	// dials first in the real flow) and must not report the agent as ready.
	if sess.agentAttached() {
		t.Fatal("agent reported attached before it attached")
	}
	if !sess.attachOperator(opSrv) {
		t.Fatal("operator attach failed with no agent present")
	}
	if sess.agentAttached() {
		t.Fatal("agent reported attached after an operator-only attach")
	}
	if sess.attachOperator(opSrv) {
		t.Fatal("a second operator attach was accepted")
	}
	if !sess.sendToOperator(map[string]string{"type": "hello"}) {
		t.Fatal("sendToOperator failed")
	}
	if frame := readJSONFrame(t, opCli); frame["type"] != "hello" {
		t.Fatalf("operator frame = %+v", frame)
	}

	// No agent yet: frames must queue instead of being dropped.
	if !sess.sendRawToAgent([]byte(`{"type":"offer"}`)) {
		t.Fatal("sendRawToAgent refused to buffer")
	}
	pending, ok := sess.attachAgent(agSrv)
	if !ok || len(pending) != 1 {
		t.Fatalf("attachAgent = (%d frames, %v)", len(pending), ok)
	}
	if frame := readJSONFrame(t, agCli); frame["type"] != "offer" {
		t.Fatalf("agent did not receive the buffered offer: %+v", frame)
	}
	if !sess.peersReady() || !sess.agentAttached() {
		t.Fatal("peers not ready after both attaches")
	}
	if _, ok := sess.attachAgent(agSrv); ok {
		t.Fatal("a second agent attach was accepted")
	}

	if !sess.sendRawToAgent([]byte(`{"type":"answer"}`)) {
		t.Fatal("live send to agent failed")
	}
	if frame := readJSONFrame(t, agCli); frame["type"] != "answer" {
		t.Fatalf("agent frame = %+v", frame)
	}

	sess.end("operator_stop")
	if hub.byAgentSession("agent-sig") != nil {
		t.Fatal("ended session is still registered")
	}
	if sess.peersReady() {
		t.Fatal("ended session still reports ready peers")
	}
	// Both sides learn why, and a second end is a no-op.
	if frame := readJSONFrame(t, opCli); frame["type"] != "session_end" || frame["reason"] != "operator_stop" {
		t.Fatalf("operator end frame = %+v", frame)
	}
	if frame := readJSONFrame(t, agCli); frame["type"] != "session_end" {
		t.Fatalf("agent end frame = %+v", frame)
	}
	sess.end("again")
	if sess.sendToOperator(map[string]string{"type": "late"}) {
		t.Fatal("a torn-down session still writes to the operator")
	}
	if sess.sendRawToAgent([]byte(`{"type":"late"}`)) {
		t.Fatal("a torn-down session still buffers for the agent")
	}
}

func TestWebRTCSessionPendingBufferOverflow(t *testing.T) {
	hub := newWebRTCHub(&config.Config{})
	sess, _, err := hub.createSession("agent-flood", 7, 1, webrtcStartOptions{}, 0)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	for i := 0; i < webrtcMaxPendingFrames; i++ {
		if !sess.sendRawToAgent([]byte(`{"type":"candidate"}`)) {
			t.Fatalf("frame %d was refused below the buffer cap", i)
		}
	}
	// One past the cap drops the frame and tears the session down instead of
	// letting an operator pin memory indefinitely.
	if sess.sendRawToAgent([]byte(`{"type":"candidate"}`)) {
		t.Fatal("frame past the buffer cap was accepted")
	}
	if hub.byAgentSession("agent-flood") != nil {
		t.Fatal("overflowing session was not removed from the hub")
	}
}
