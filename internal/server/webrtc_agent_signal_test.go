package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// rdAgentHarness wires the agent signaling route to a real listener.
type rdAgentHarness struct {
	server *Server
	srv    *httptest.Server
}

func newRDAgentHarness(t *testing.T) *rdAgentHarness {
	t.Helper()
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/agent", s.handleRDWebRTCAgent)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return &rdAgentHarness{server: s, srv: srv}
}

func (h *rdAgentHarness) dial(t *testing.T, sessionID, token, agentID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/agent?session=" + sessionID + "&token=" + token
	if agentID != "" {
		url += "&agent=" + agentID
	}
	return websocket.DefaultDialer.Dial(url, nil)
}

// TestRDWebRTCAgentTokenGuards covers the refusals before the upgrade: missing
// parameters, an unknown token, and a token used for the wrong agent.
func TestRDWebRTCAgentTokenGuards(t *testing.T) {
	h := newRDAgentHarness(t)
	if err := h.server.db.Create(&db.Implant{ID: "rd-agent", TenantID: 1, Hostname: "rd", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	h.server.cfg.WebRTC.Enabled = true
	sess, token, err := h.server.webRTCHub().createSession("rd-agent", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.end("test")

	// Missing session or token → 400.
	_, resp, err := h.dial(t, "", "", "rd-agent")
	if err == nil {
		t.Fatal("dial without session/token was allowed")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing params status = %v", resp)
	}

	// Unknown token → 401.
	_, resp, err = h.dial(t, sess.ID, "not-the-token", "rd-agent")
	if err == nil {
		t.Fatal("an invalid token was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token status = %v", resp)
	}

	// A valid token presented for another agent id → 401, and the one-shot
	// token is consumed: the session stays without an agent.
	_, resp, err = h.dial(t, sess.ID, token, "someone-else")
	if err == nil {
		t.Fatal("a token was accepted for the wrong agent id")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong agent status = %v", resp)
	}
	if sess.agentAttached() {
		t.Fatal("the session attached an agent after a rejected token")
	}
}

// TestRDWebRTCAgentSignalingLoop drives the agent socket: the operator is told
// the agent is ready, answer/ICE/state frames reach the operator, oversized or
// empty frames are dropped, and stop ends the session.
func TestRDWebRTCAgentSignalingLoop(t *testing.T) {
	h := newRDAgentHarness(t)
	if err := h.server.db.Create(&db.Implant{ID: "rd-agent2", TenantID: 1, Hostname: "rd2", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	h.server.cfg.WebRTC.Enabled = true
	sess, token, err := h.server.webRTCHub().createSession("rd-agent2", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// The operator socket is attached first, mirroring a browser that dials as
	// soon as it has the session.
	opSrv, opCli := newWSConnPair(t)
	defer opCli.Close()
	if !sess.attachOperator(opSrv) {
		t.Fatal("operator attach failed")
	}

	agentConn, _, err := h.dial(t, sess.ID, token, "rd-agent2")
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer agentConn.Close()

	// The operator hears that the agent arrived.
	frame := readJSONFrame(t, opCli)
	if frame["type"] != "agent_ready" {
		t.Fatalf("operator frame = %+v, want agent_ready", frame)
	}

	// A valid answer is forwarded verbatim.
	if err := agentConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"answer","sdp":"v=0\r\n"}`)); err != nil {
		t.Fatal(err)
	}
	frame = readJSONFrame(t, opCli)
	if frame["type"] != "answer" {
		t.Fatalf("forwarded frame = %+v", frame)
	}

	// An empty answer is dropped, so the next frame the operator sees is the
	// ICE candidate that follows it.
	if err := agentConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"answer","sdp":""}`)); err != nil {
		t.Fatal(err)
	}
	if err := agentConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ice","candidate":"candidate:1"}`)); err != nil {
		t.Fatal(err)
	}
	frame = readJSONFrame(t, opCli)
	if frame["type"] != "ice" {
		t.Fatalf("operator saw %+v, want the ICE frame (empty answer must be dropped)", frame)
	}

	// A state frame is forwarded; an oversized error reason is truncated but
	// still delivered.
	if err := agentConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"state","state":"desktop"}`)); err != nil {
		t.Fatal(err)
	}
	if frame = readJSONFrame(t, opCli); frame["type"] != "state" {
		t.Fatalf("state frame = %+v", frame)
	}
	big := strings.Repeat("x", 2048)
	payload, err := json.Marshal(map[string]any{"type": "error", "reason": big})
	if err != nil {
		t.Fatal(err)
	}
	if err := agentConn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatal(err)
	}
	frame = readJSONFrame(t, opCli)
	if frame["type"] != "error" {
		t.Fatalf("error frame = %+v", frame)
	}
	if reason, _ := frame["reason"].(string); len(reason) != 512 {
		t.Fatalf("error reason length = %d, want it truncated to 512", len(reason))
	}

	// stop ends the session.
	if err := agentConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"stop"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.server.webRTCHub().byAgentSession("rd-agent2") == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("agent stop did not end the session")
}

// TestRDWebRTCAgentTokenIsSingleUse pins the actual behaviour of a second agent
// dial: the one-shot token was consumed by the first connection, so the reuse
// is refused as unauthorized. The handler also has a 409 "already has an
// agent" branch, but with a single-use token it cannot be reached (recorded in
// the changelog rather than silently deleted).
func TestRDWebRTCAgentTokenIsSingleUse(t *testing.T) {
	h := newRDAgentHarness(t)
	if err := h.server.db.Create(&db.Implant{ID: "rd-agent3", TenantID: 1, Hostname: "rd3", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	h.server.cfg.WebRTC.Enabled = true
	sess, token, err := h.server.webRTCHub().createSession("rd-agent3", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	first, _, err := h.dial(t, sess.ID, token, "rd-agent3")
	if err != nil {
		t.Fatalf("first agent dial: %v", err)
	}
	defer first.Close()

	// Wait until the first socket is registered before racing a second one.
	deadline := time.Now().Add(5 * time.Second)
	for !sess.agentAttached() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !sess.agentAttached() {
		t.Fatal("first agent socket never attached")
	}

	_, resp, err := h.dial(t, sess.ID, token, "rd-agent3")
	if err == nil {
		t.Fatal("a second agent socket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused token status = %v", resp)
	}
}

var _ = sync.Mutex{}
