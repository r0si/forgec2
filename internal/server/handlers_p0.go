package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// ── P0-3: rportfwd server-side relay (binds local port, relays via beacon) ─
func (s *Server) handleRPortFwdRelayStart(c *gin.Context) {
	// Similar to SOCKS relay: server binds port, tunnels through beacon
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	localPortStr := c.PostForm("lport")
	if localPortStr == "" {
		localPortStr = "1081"
	}
	forwardTarget := c.PostForm("target")
	if forwardTarget == "" {
		respondError(c, http.StatusBadRequest, "target host:port required")
		return
	}

	lport, err := strconv.Atoi(localPortStr)
	if err != nil || lport < 1 || lport > 65535 {
		respondError(c, http.StatusBadRequest, "invalid port")
		return
	}
	if _, _, err := net.SplitHostPort(forwardTarget); err != nil {
		respondError(c, http.StatusBadRequest, "invalid target, want host:port")
		return
	}

	s.rportfwdMu.Lock()
	defer s.rportfwdMu.Unlock()

	if s.rportfwdListeners == nil {
		s.rportfwdListeners = make(map[string]*rportfwdRelay)
	}

	key := fmt.Sprintf("%s:%d", id, lport)
	if _, exists := s.rportfwdListeners[key]; exists {
		respondError(c, http.StatusBadRequest, "rportfwd already active for this agent:port")
		return
	}
	if len(s.rportfwdListeners) >= MaxRPortFwdListeners {
		respondError(c, http.StatusTooManyRequests, "reverse port forward limit reached")
		return
	}

	relay := newRPortFwdRelay(s, id, lport, forwardTarget)
	if err := relay.bind(); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to bind rportfwd: "+err.Error())
		return
	}
	s.rportfwdListeners[key] = relay
	go relay.acceptLoop()

	slog.Info("Reverse port forward relay started", "agent_id", id, "lport", lport, "target", forwardTarget)
	s.LogAuditRecord(c, "rportfwd_relay_start", "agent", id, fmt.Sprintf("rportfwd relay :%d -> %s via %s", lport, forwardTarget, id), true, nil)
	respond(c, gin.H{"message": fmt.Sprintf("rportfwd relay :%d -> %s via %s", lport, forwardTarget, id)})
}

func (s *Server) handleRPortFwdStatus(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	s.rportfwdMu.Lock()
	var active *rportfwdRelay
	for _, relay := range s.rportfwdListeners {
		if relay.agentID == id {
			active = relay
			break
		}
	}
	var port int
	var target string
	if active != nil {
		port = active.localPort
		target = active.forwardTarget
	}
	s.rportfwdMu.Unlock()

	if active == nil {
		respond(c, gin.H{"active": false})
		return
	}
	respond(c, gin.H{
		"active": true,
		"port":   port,
		"target": target,
	})
}

// handleRPortFwdGlobalStatus returns status of all active reverse port forwards.
func (s *Server) handleRPortFwdGlobalStatus(c *gin.Context) {
	type fwdInfo struct {
		AgentID    string `json:"agent_id"`
		LocalPort  int    `json:"local_port"`
		RemoteHost string `json:"remote_host"`
		RemotePort int    `json:"remote_port"`
		Protocol   string `json:"protocol"`
		Active     bool   `json:"active"`
	}
	s.rportfwdMu.Lock()
	forwards := make([]fwdInfo, 0, len(s.rportfwdListeners))
	for _, relay := range s.rportfwdListeners {
		host, portStr, _ := strings.Cut(relay.forwardTarget, ":")
		rport, err := strconv.Atoi(portStr)
		if err != nil || rport < 1 || rport > 65535 {
			continue
		}
		forwards = append(forwards, fwdInfo{
			AgentID:    relay.agentID,
			LocalPort:  relay.localPort,
			RemoteHost: host,
			RemotePort: rport,
			Protocol:   "tcp",
			Active:     relay.listener != nil,
		})
	}
	s.rportfwdMu.Unlock()
	if forwards == nil {
		forwards = []fwdInfo{}
	}
	respond(c, gin.H{"forwards": forwards})
}

func (s *Server) handleRPortFwdRelayStop(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	localPortStr := c.PostForm("lport")
	if localPortStr == "" {
		localPortStr = c.DefaultQuery("lport", "1081")
	}

	key := fmt.Sprintf("%s:%s", id, localPortStr)
	s.rportfwdMu.Lock()
	relay, exists := s.rportfwdListeners[key]
	if exists {
		delete(s.rportfwdListeners, key)
	}
	s.rportfwdMu.Unlock()

	if !exists {
		respondError(c, http.StatusNotFound, "no active rportfwd for this agent:port")
		return
	}
	relay.stop()
	slog.Info("Reverse port forward relay stopped", "agent_id", id, "lport", localPortStr)
	s.LogAuditRecord(c, "rportfwd_relay_stop", "agent", id, fmt.Sprintf("rportfwd relay stopped :%s", localPortStr), true, nil)
	respond(c, gin.H{"message": "rportfwd relay stopped"})
}

// rportfwdRelay manages a local TCP listener that tunnels through a beacon channel.
type rportfwdRelay struct {
	server        *Server
	agentID       string
	localPort     int
	forwardTarget string
	listener      *rportfwdListener
	stopCh        chan struct{}
	stopOnce      sync.Once
}

// rportfwdMaxConns caps simultaneous operator connections per relay: each
// holds an FD plus beacon-queue backlog, so unbounded accepts are an FD
// exhaustion primitive.
const rportfwdMaxConns = 64

// rportfwdConnIdleTimeout closes operator connections idle this long. Idle
// tunnels otherwise pin FDs and beacon backlog forever.
const rportfwdConnIdleTimeout = 10 * time.Minute

// rportfwdWriteTimeout bounds a single write to the operator socket: the
// lookup locks are released first, but a hung operator must not wedge the
// beacon dispatch goroutine forever.
const rportfwdWriteTimeout = 30 * time.Second

type rportfwdListener struct {
	ln      net.Listener
	connMap map[uint64]net.Conn
	nextID  uint64
	mu      sync.Mutex
}

func newRPortFwdRelay(s *Server, agentID string, lport int, target string) *rportfwdRelay {
	return &rportfwdRelay{
		server:        s,
		agentID:       agentID,
		localPort:     lport,
		forwardTarget: target,
		stopCh:        make(chan struct{}),
	}
}

func (r *rportfwdRelay) bind() error {
	listenHost := "127.0.0.1"
	if r.server != nil && r.server.cfg != nil && r.server.cfg.Server.SocksListenHost != "" {
		listenHost = r.server.cfg.Server.SocksListenHost
	}
	addr := fmt.Sprintf("%s:%d", listenHost, r.localPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	r.listener = &rportfwdListener{
		ln:      ln,
		connMap: make(map[uint64]net.Conn),
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("recovered from panic", "err", rec, "stack", string(debug.Stack()))
			}
		}()
		<-r.stopCh
		ln.Close()
	}()
	return nil
}

func (r *rportfwdRelay) acceptLoop() {
	if r.listener == nil || r.listener.ln == nil {
		return
	}
	ln := r.listener.ln
	slog.Info("Rportfwd relay listening", "addr", ln.Addr().String(), "target", r.forwardTarget, "agent_id", r.agentID)
	backoff := time.Second
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Stopped: listener closed via stopCh.
			select {
			case <-r.stopCh:
				return
			default:
			}
			// Transient accept errors (EMFILE, ENFILE, ECONNABORTED) must
			// back off, not spin or die: a single error used to kill the
			// whole relay permanently.
			slog.Warn("Rportfwd accept error, backing off", "addr", ln.Addr().String(), "err", err, "backoff", backoff)
			select {
			case <-r.stopCh:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		backoff = time.Second
		go r.handleConn(conn)
	}
}

func (r *rportfwdRelay) handleConn(operatorConn net.Conn) {
	defer operatorConn.Close()

	r.listener.mu.Lock()
	if len(r.listener.connMap) >= rportfwdMaxConns {
		r.listener.mu.Unlock()
		slog.Warn("Rportfwd relay at connection cap, refusing", "agent_id", r.agentID, "cap", rportfwdMaxConns)
		return
	}
	// Globally-unique conn ID across all relays: per-relay counters restart
	// at 1, so two relays for one agent would otherwise share IDs and the
	// agent's data frames would fan out to both operator sockets.
	var connID uint64
	if r.server != nil {
		connID = r.server.rportfwdNextID.Add(1)
	} else {
		r.listener.nextID++
		connID = r.listener.nextID
	}
	r.listener.connMap[connID] = operatorConn
	r.listener.mu.Unlock()

	defer func() {
		r.listener.mu.Lock()
		delete(r.listener.connMap, connID)
		r.listener.mu.Unlock()
	}()

	// Tell agent to connect to target
	r.server.sendRPortFwdFrame(r.agentID, connID, "rportfwd_connect", []byte(r.forwardTarget))

	// Relay operator->agent via beacon frames
	buf := make([]byte, 10240)
	for {
		// Idle tunnels are reaped: without a deadline a forgotten session
		// pins the FD and the agent side forever.
		_ = operatorConn.SetReadDeadline(time.Now().Add(rportfwdConnIdleTimeout))
		n, err := operatorConn.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			r.server.sendRPortFwdFrame(r.agentID, connID, "rportfwd_data", data)
		}
		if err != nil {
			r.server.sendRPortFwdFrame(r.agentID, connID, "rportfwd_close", nil)
			return
		}
	}
}

func (r *rportfwdRelay) stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	if r.listener != nil {
		// Close the listener here, not only via the bind watcher goroutine:
		// stop() must release the port even if the watcher already fired or
		// the relay was assembled without bind(). Close is idempotent.
		if r.listener.ln != nil {
			_ = r.listener.ln.Close()
		}
		r.listener.mu.Lock()
		for id, conn := range r.listener.connMap {
			conn.Close()
			delete(r.listener.connMap, id)
		}
		r.listener.mu.Unlock()
	}
}

// stopAllRPortFwd stops every relay and clears the table. Shutdown path:
// FDs + accept loops are released even for agents that never sent close.
func (s *Server) stopAllRPortFwd() {
	s.rportfwdMu.Lock()
	relays := make([]*rportfwdRelay, 0, len(s.rportfwdListeners))
	for _, r := range s.rportfwdListeners {
		relays = append(relays, r)
	}
	s.rportfwdListeners = make(map[string]*rportfwdRelay)
	s.rportfwdMu.Unlock()
	for _, r := range relays {
		r.stop()
	}
}

// closeRPortFwdConn drops one operator leg. Used for loud overflow/close
// handling without holding the global relay lock across socket I/O.
func (s *Server) closeRPortFwdConn(agentID string, connID uint64) {
	s.rportfwdMu.Lock()
	var target net.Conn
	for key, relay := range s.rportfwdListeners {
		if !strings.HasPrefix(key, agentID+":") {
			continue
		}
		if relay.listener == nil {
			continue
		}
		relay.listener.mu.Lock()
		if c, ok := relay.listener.connMap[connID]; ok {
			delete(relay.listener.connMap, connID)
			target = c
		}
		relay.listener.mu.Unlock()
		if target != nil {
			break
		}
	}
	s.rportfwdMu.Unlock()
	if target != nil {
		_ = target.Close()
	}
}

// sendRPortFwdFrame enqueues a frame for the agent to pick up on next beacon.
// Overflow closes the operator leg loudly: dropping data silently corrupts
// the TCP stream without either end noticing.
func (s *Server) sendRPortFwdFrame(agentID string, connID uint64, action string, data []byte) {
	if s.socksEngine == nil {
		return
	}
	ok := s.socksEngine.enqueueRPortFwdFrame(agentID, socksFrame{
		ConnID: connID,
		Action: action,
		Data:   data,
	})
	if !ok {
		s.closeRPortFwdConn(agentID, connID)
	}
}

// processRPortFwdData handles rportfwd data coming FROM the agent back to the operator.
func (s *Server) processRPortFwdData(agentID string, frame socksFrame) {
	switch frame.Action {
	case "rportfwd_connected":
		slog.Info("Rportfwd agent connected to target", "agent_id", agentID, "conn", frame.ConnID)
		return
	case "rportfwd_error", "rportfwd_close":
		s.closeRPortFwdConn(agentID, frame.ConnID)
		return
	case "rportfwd_data":
		// fall through to write path below
	default:
		return
	}
	if len(frame.Data) == 0 {
		return
	}
	// Snapshot the socket under the locks, write outside: holding
	// s.rportfwdMu across a blocking conn.Write stalls Start/Stop/Status
	// and cleanup for every relay.
	s.rportfwdMu.Lock()
	var target net.Conn
	for key, relay := range s.rportfwdListeners {
		if !strings.HasPrefix(key, agentID+":") {
			continue
		}
		if relay.listener == nil {
			continue
		}
		relay.listener.mu.Lock()
		c, ok := relay.listener.connMap[frame.ConnID]
		relay.listener.mu.Unlock()
		if ok {
			target = c
			break
		}
	}
	s.rportfwdMu.Unlock()
	if target == nil {
		return
	}
	_ = target.SetWriteDeadline(time.Now().Add(rportfwdWriteTimeout))
	_, err := target.Write(frame.Data)
	_ = target.SetWriteDeadline(time.Time{})
	if err != nil {
		slog.Warn("Rportfwd write to operator failed, closing", "agent_id", agentID, "conn", frame.ConnID, "error", err)
		s.closeRPortFwdConn(agentID, frame.ConnID)
		s.sendRPortFwdFrame(agentID, frame.ConnID, "rportfwd_close", nil)
	}
}

// cleanupStaleRPortFwd removes stale rportfwd listeners on agent disconnect
func (s *Server) cleanupStaleRPortFwd() {
	// Snapshot under lock, DB + stop outside: holding rportfwdMu across a
	// DB query stalls beacon dispatch and Start/Stop/Status for all relays.
	s.rportfwdMu.Lock()
	snapshot := make(map[string]*rportfwdRelay, len(s.rportfwdListeners))
	for key, relay := range s.rportfwdListeners {
		snapshot[key] = relay
	}
	agentIDs := make([]string, 0, len(snapshot))
	for _, relay := range snapshot {
		agentIDs = append(agentIDs, relay.agentID)
	}
	s.rportfwdMu.Unlock()

	agentMap := make(map[string]*db.Implant, len(agentIDs))
	if len(agentIDs) > 0 {
		var agents []db.Implant
		if err := s.db.Where("id IN ?", agentIDs).Limit(len(agentIDs)).Find(&agents).Error; err != nil {
			slog.Error("Failed to batch-load agents for rportfwd cleanup", "error", err)
			return
		}
		for i := range agents {
			agentMap[agents[i].ID] = &agents[i]
		}
	}

	var stale []string
	for key, relay := range snapshot {
		agent, ok := agentMap[relay.agentID]
		if !ok {
			stale = append(stale, key)
			continue
		}
		if time.Since(agent.LastSeen) > s.offlineThreshold()*2 {
			stale = append(stale, key)
		}
	}
	if len(stale) == 0 {
		return
	}
	s.rportfwdMu.Lock()
	defer s.rportfwdMu.Unlock()
	for _, key := range stale {
		if relay, ok := s.rportfwdListeners[key]; ok {
			relay.stop()
			delete(s.rportfwdListeners, key)
		}
	}
}

// Add rportfwdMu and rportfwdListeners to Server (handled via init in server.go)
