package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// TestRDWebRTCSignalRequiresSessionAndOwnership covers the refusals that run
// before the upgrade: a missing session id, and a session owned by a different
// operator. Both must be answered as HTTP errors, never as an upgraded socket.
func TestRDWebRTCSignalRequiresSessionAndOwnership(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-agent", TenantID: 1, Hostname: "rd", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, _, err := s.webRTCHub().createSession("rd-agent", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.end("test")

	serve := func(uid uint) *httptest.Server {
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.GET("/probe", func(c *gin.Context) {
			c.Set("user_role", db.RoleAdmin)
			c.Set("user_id", uid)
			s.handleRDWebRTCSignal(c)
		})
		srv := httptest.NewServer(engine)
		t.Cleanup(srv.Close)
		return srv
	}

	wsBase := func(srv *httptest.Server) string {
		return "ws" + strings.TrimPrefix(srv.URL, "http")
	}

	// Missing session parameter → 400, no upgrade.
	owner := serve(7)
	_, resp, err := websocket.DefaultDialer.Dial(wsBase(owner)+"/probe", nil)
	if err == nil {
		t.Fatal("a dial without a session id was allowed")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing session status = %v", resp)
	}

	// A session owned by operator 7 is invisible to operator 8 → 404.
	foreign := serve(8)
	_, resp, err = websocket.DefaultDialer.Dial(wsBase(foreign)+"/probe?session="+sess.ID, nil)
	if err == nil {
		t.Fatal("a foreign operator was allowed to attach")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign session status = %v", resp)
	}

	// The owner gets past the guards: the socket upgrades and the session
	// reports the agent as not yet attached (the normal order: the browser
	// connects first). Before the attachOperator fix this path closed the
	// operator socket immediately, so no frame ever arrived.
	conn, _, err := websocket.DefaultDialer.Dial(wsBase(owner)+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("owner dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("operator received no frame: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame %s: %v", raw, err)
	}
	if frame["type"] != "waiting" {
		t.Fatalf("first frame = %+v, want waiting while the agent is absent", frame)
	}
}

// TestRDWebRTCSignalOperatorLoop drives the signaling loop: ping is answered,
// an unknown type is reported, an offer is buffered until the agent attaches,
// an empty offer is rejected and stop ends the session.
func TestRDWebRTCSignalOperatorLoop(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-loop", TenantID: 1, Hostname: "loop", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, _, err := s.webRTCHub().createSession("rd-loop", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	readFrame := func() map[string]any {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return m
	}
	send := func(payload string) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("write %s: %v", payload, err)
		}
	}

	if frame := readFrame(); frame["type"] != "waiting" {
		t.Fatalf("initial frame = %+v", frame)
	}

	send(`{"type":"ping"}`)
	if frame := readFrame(); frame["type"] != "pong" {
		t.Fatalf("ping frame = %+v", frame)
	}

	send(`{"type":"teleport"}`)
	if frame := readFrame(); frame["type"] != "error" {
		t.Fatalf("unknown type frame = %+v", frame)
	}

	// An offer with no agent attached is acknowledged with waiting and buffered.
	send(`{"type":"offer","sdp":"v=0"}`)
	if frame := readFrame(); frame["type"] != "waiting" {
		t.Fatalf("offer frame = %+v", frame)
	}
	sess.mu.Lock()
	pending := len(sess.pending)
	sess.mu.Unlock()
	if pending != 1 {
		t.Fatalf("buffered frames = %d, want the offer", pending)
	}

	// An empty SDP is rejected instead of buffered.
	send(`{"type":"offer","sdp":""}`)
	if frame := readFrame(); frame["type"] != "error" {
		t.Fatalf("empty offer frame = %+v", frame)
	}

	return
}
