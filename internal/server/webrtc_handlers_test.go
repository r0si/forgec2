package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func seedWebRTCOperator(t *testing.T, s *Server) {
	t.Helper()
	if err := s.db.Create(&db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed operator: %v", err)
	}
	if err := s.db.Create(&db.User{Username: "other", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed other operator: %v", err)
	}
}

func TestWebRTCHubLazyInitAndSingleton(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.WebRTC.Enabled = true
	first := s.webRTCHub()
	if first == nil || first.cfg == nil {
		t.Fatal("hub not built")
	}
	if second := s.webRTCHub(); second != first {
		t.Fatal("webRTCHub should be a singleton per Server")
	}
}

func TestHandleWebRTCStartRefusals(t *testing.T) {
	s := mustTenantServer(t)
	seedWebRTCOperator(t, s)
	seedTenantAgent(t, s, "rd-online", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "rd-online").Update("status", "online").Error; err != nil {
		t.Fatal(err)
	}
	seedTenantAgent(t, s, "rd-offline", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "rd-offline").Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}

	// Offline agent: 409 before any session work.
	c, w := missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{}`,
		gin.Params{{Key: "id", Value: "rd-offline"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "offline") {
		t.Fatalf("offline start = %d %s", w.Code, w.Body.String())
	}

	// Unknown agent: 404 from getAgentOrFail.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{}`,
		gin.Params{{Key: "id", Value: "ghost"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}

	// Malformed body: 400.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{`,
		gin.Params{{Key: "id", Value: "rd-online"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d", w.Code)
	}

	// Out-of-range settings: 400 with the normalizer's message.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{"width":99999}`,
		gin.Params{{Key: "id", Value: "rd-online"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad settings = %d %s", w.Code, w.Body.String())
	}

	// The still-image monitor and WebRTC cannot both own the agent.
	s.screenMonitorMu.Lock()
	if s.screenMonitorImplants == nil {
		s.screenMonitorImplants = map[string]time.Time{}
	}
	s.screenMonitorImplants["rd-online"] = time.Now()
	s.screenMonitorMu.Unlock()
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{}`,
		gin.Params{{Key: "id", Value: "rd-online"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "screen monitor") {
		t.Fatalf("monitor conflict = %d %s", w.Code, w.Body.String())
	}
	s.screenMonitorMu.Lock()
	delete(s.screenMonitorImplants, "rd-online")
	s.screenMonitorMu.Unlock()

	// WebRTC disabled in config: 503.
	s.cfg.WebRTC.Enabled = false
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{}`,
		gin.Params{{Key: "id", Value: "rd-online"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled = %d %s", w.Code, w.Body.String())
	}

	// A live session on the same agent is refused before TURN is consulted.
	s.cfg.WebRTC.Enabled = true
	if _, _, err := s.webRTCHub().createSession("rd-online", 1, 1, webrtcStartOptions{}, 0); err != nil {
		t.Fatalf("pre-create session: %v", err)
	}
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/start", `{}`,
		gin.Params{{Key: "id", Value: "rd-online"}})
	s.handleWebRTCStart(c)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already has a live WebRTC session") {
		t.Fatalf("duplicate session = %d %s", w.Code, w.Body.String())
	}
}

func TestHandleWebRTCStop(t *testing.T) {
	s := mustTenantServer(t)
	seedWebRTCOperator(t, s)
	seedTenantAgent(t, s, "rd-stop", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "rd-stop").Update("status", "online").Error; err != nil {
		t.Fatal(err)
	}
	hub := s.webRTCHub()

	// No session yet: a friendly 200, not a 404.
	c, w := missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/stop", `{}`,
		gin.Params{{Key: "id", Value: "rd-stop"}})
	s.handleWebRTCStop(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "no active WebRTC session") {
		t.Fatalf("stop without session = %d %s", w.Code, w.Body.String())
	}

	// A session owned by another operator is refused, and stays alive.
	sess, _, err := hub.createSession("rd-stop", 99, 1, webrtcStartOptions{}, 0)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.end("test")
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/stop", `{}`,
		gin.Params{{Key: "id", Value: "rd-stop"}})
	s.handleWebRTCStop(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign stop = %d %s", w.Code, w.Body.String())
	}
	if hub.byAgentSession("rd-stop") != sess {
		t.Fatal("foreign stop tore the session down")
	}

	// A stale session id (the browser stopping a previous session) is a no-op.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/stop", `{"session_id":"stale-id"}`,
		gin.Params{{Key: "id", Value: "rd-stop"}})
	s.handleWebRTCStop(c)
	if w.Code != http.StatusOK {
		t.Fatalf("stale stop = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no longer active") {
		t.Fatalf("stale stop body = %s", w.Body.String())
	}
	if hub.byAgentSession("rd-stop") != sess {
		t.Fatal("stale stop tore the session down")
	}

	// The owning operator stops it; the agent gets a fallback stop task.
	if err := s.db.Model(&db.User{}).Where("username = ?", "admin").Update("id", 1).Error; err != nil {
		t.Fatal(err)
	}
	// Sessions live in memory; make this operator the owner.
	sess.OperatorID = 1
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/webrtc/stop", `{}`,
		gin.Params{{Key: "id", Value: "rd-stop"}})
	s.handleWebRTCStop(c)
	if w.Code != http.StatusOK {
		t.Fatalf("owner stop = %d %s", w.Code, w.Body.String())
	}
	if hub.byAgentSession("rd-stop") != nil {
		t.Fatal("owner stop left the session registered")
	}
	if !strings.Contains(w.Body.String(), "session_id") {
		t.Fatalf("owner stop body = %s", w.Body.String())
	}
}

func TestParseWebRTCSignalBounds(t *testing.T) {
	// Oversized frames are rejected rather than buffered.
	big := make([]byte, webrtcMaxSignalBytes+1)
	if _, err := parseWebRTCSignal(big); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized signal = %v", err)
	}
	if _, err := parseWebRTCSignal([]byte(`{`)); err == nil {
		t.Fatal("malformed signal accepted")
	}
	var sig webrtcSignal
	raw, _ := json.Marshal(gin.H{"type": "offer", "sdp": "v=0", "reason": "start"})
	if err := json.Unmarshal(raw, &sig); err != nil {
		t.Fatal(err)
	}
	if sig.Type != "offer" || sig.SDP != "v=0" {
		t.Fatalf("signal = %+v", sig)
	}
}
