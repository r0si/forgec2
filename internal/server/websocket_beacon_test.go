package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// wsTestServer exposes only the beacon websocket route on a real listener, so
// the handler and both pumps run for real.
func wsTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := mustTenantServer(t)
	// The write pump selects on s.ctx; a nil context panics.
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/ws/beacon", s.handleWebSocketBeacon)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return s, srv
}

func wsURL(srv *httptest.Server, agentID string) string {
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	if agentID == "" {
		return base + "/ws/beacon"
	}
	return base + "/ws/beacon?agent_id=" + agentID
}

const testAgentUUID = "11111111-2222-3333-4444-555555555555"

// TestWebSocketBeaconRejectsInvalidAgentID pins the query validation: a
// malformed id closes the socket with a protocol error instead of being
// processed.
func TestWebSocketBeaconRejectsInvalidAgentID(t *testing.T) {
	_, srv := wsTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "not-a-uuid"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !asWebsocketClose(readErr, &closeErr) {
		t.Fatalf("expected a close frame, got %v", readErr)
	}
	if closeErr.Code != websocket.CloseProtocolError {
		t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.CloseProtocolError)
	}
}

// TestWebSocketBeaconRejectsGarbageFrame proves an unauthenticated frame is
// dropped without tearing the socket down: the envelope decoder rejects it and
// the pump keeps looping.
func TestWebSocketBeaconRejectsGarbageFrame(t *testing.T) {
	s, srv := wsTestServer(t)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(srv, testAgentUUID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("this is not a beacon envelope")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	// Give the read pump a moment to reject it.
	time.Sleep(100 * time.Millisecond)
	if hub := s.wsHub; hub != nil {
		if beacon := hub.Get(testAgentUUID); beacon == nil {
			t.Fatal("connection was unregistered after a rejected frame")
		}
	}
	// The socket must still be usable: closing from the client drives the
	// cleanup path (unregister + disconnect handling) without a panic.
	_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestWebSocketBeaconRejectsDuplicateConnection pins the exclusivity guard.
func TestWebSocketBeaconRejectsDuplicateConnection(t *testing.T) {
	_, srv := wsTestServer(t)
	first, _, err := websocket.DefaultDialer.Dial(wsURL(srv, testAgentUUID), nil)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	defer first.Close()

	second, _, err := websocket.DefaultDialer.Dial(wsURL(srv, testAgentUUID), nil)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, readErr := second.ReadMessage()
	var closeErr *websocket.CloseError
	if !asWebsocketClose(readErr, &closeErr) {
		t.Fatalf("expected the duplicate to be closed, got %v", readErr)
	}
	if closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.ClosePolicyViolation)
	}
}

// asWebsocketClose reports whether err is a websocket close error.
func asWebsocketClose(err error, out **websocket.CloseError) bool {
	if err == nil {
		return false
	}
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		return false
	}
	*out = ce
	return true
}

var _ = http.StatusOK
