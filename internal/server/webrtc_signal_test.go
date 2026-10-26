package server

import (
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

	// The owner gets past the guards: the socket upgrades. The signaling loop
	// itself is not asserted here (see the note in the changelog).
	conn, _, err := websocket.DefaultDialer.Dial(wsBase(owner)+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("owner dial: %v", err)
	}
	_ = conn.Close()
}
