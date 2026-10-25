package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// tunEngine pairs an implant TUN with a teamserver UDP helper. Each UDP
// datagram on 127.0.0.1:port is one IP packet framed as tun_data over the
// beacon SOCKS channel. Linux uses /dev/net/tun; Windows uses the bundled
// signed Wintun runtime (adapter creation needs an elevated implant); macOS
// implants return an honest error. The teamserver does not open a TAP.

type tunEngine struct {
	mu       sync.Mutex
	sessions map[string]*tunSession
	// dropCounter counts silently-dropped packets by reason (tun_overflow,
	// tun_oversize). Wired by the server after metrics init; nil in tests.
	dropCounter *prometheus.CounterVec
}

// SetDropCounter wires the drop metric. Safe to call once at startup.
func (e *tunEngine) SetDropCounter(c *prometheus.CounterVec) {
	e.dropCounter = c
}

func (e *tunEngine) noteDrop(reason string) {
	if e.dropCounter != nil {
		e.dropCounter.WithLabelValues(reason).Inc()
	}
}

type tunSession struct {
	agentID  string
	cidr     string
	iface    string
	status   string
	udpConn  *net.UDPConn
	lastPeer *net.UDPAddr
	port     int
	pending  [][]byte
	// pendingBytes bounds buffered-but-undrained packets; beyond it the
	// oldest is shed loudly (logged + counted) instead of an unbounded
	// 256×64KB≈16MB backlog per beacon.
	pendingBytes int
	lastActive   time.Time
	mu           sync.Mutex
	stop         chan struct{}
	stopOnce     sync.Once
}

// TUN queue bounds: 256 packets AND a 2MB byte budget — a flood of max-size
// datagrams must not build a 16MB backlog per beacon, and drops are logged +
// counted instead of silent.
const (
	tunMaxPendingPackets = 256
	tunMaxPendingBytes   = 2 << 20
	// tunMaxPacketBytes rejects jumbo frames that can never be a single IP
	// packet / UDP datagram on this helper.
	tunMaxPacketBytes = 65535
	// tunSessionIdleTimeout reaps helper sessions with no activity this long.
	// TUN is long-lived by nature, so this is looser than the 10m leg idle
	// used by rportfwd/lportfwd — it only catches dead agents that never
	// sent tun_down.
	tunSessionIdleTimeout = 30 * time.Minute
)

func newTunEngine() *tunEngine {
	return &tunEngine{sessions: make(map[string]*tunSession)}
}

func (e *tunEngine) get(agentID string) *tunSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessions[agentID]
}

func (e *tunEngine) startUDP(s *Server, agentID, cidr string, port int) (int, error) {
	// Reserve the agent slot first: without the placeholder, two concurrent
	// starts both pass the existence check, both ListenUDP, and the loser
	// leaks its socket + udpLoop goroutine (TOCTOU).
	e.mu.Lock()
	if sess, ok := e.sessions[agentID]; ok {
		if sess.udpConn != nil {
			e.mu.Unlock()
			return sess.port, nil
		}
		e.mu.Unlock()
		return 0, fmt.Errorf("tun helper for agent %s is already starting", agentID)
	}
	placeholder := &tunSession{agentID: agentID, cidr: cidr, status: "starting", lastActive: time.Now(), stop: make(chan struct{})}
	e.sessions[agentID] = placeholder
	e.mu.Unlock()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	pc, err := net.ListenUDP("udp", addr)
	if err != nil {
		e.mu.Lock()
		if cur, ok := e.sessions[agentID]; ok && cur == placeholder {
			delete(e.sessions, agentID)
		}
		e.mu.Unlock()
		return 0, err
	}
	actual := pc.LocalAddr().(*net.UDPAddr).Port
	sess := &tunSession{
		agentID: agentID, cidr: cidr, status: "starting",
		udpConn: pc, port: actual, lastActive: time.Now(), stop: make(chan struct{}),
	}
	e.mu.Lock()
	// A racing stop() may have removed the placeholder; don't resurrect.
	if cur, ok := e.sessions[agentID]; !ok || cur != placeholder {
		e.mu.Unlock()
		_ = pc.Close()
		return 0, fmt.Errorf("tun helper for agent %s was stopped during start", agentID)
	}
	e.sessions[agentID] = sess
	e.mu.Unlock()
	go e.udpLoop(sess)
	return actual, nil
}

func (e *tunEngine) udpLoop(sess *tunSession) {
	buf := make([]byte, 65535)
	for {
		select {
		case <-sess.stop:
			return
		default:
		}
		_ = sess.udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, peer, err := sess.udpConn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-sess.stop:
				return
			default:
			}
			slog.Warn("tun UDP helper read", "agent_id", sess.agentID, "err", err)
			return
		}
		if n == 0 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		sess.mu.Lock()
		sess.lastPeer = peer
		sess.lastActive = time.Now()
		if len(sess.pending) >= tunMaxPendingPackets || sess.pendingBytes+len(pkt) > tunMaxPendingBytes {
			slog.Warn("tun helper: pending overflow, dropping packet", "agent_id", sess.agentID, "pending", len(sess.pending))
			e.noteDrop("tun_overflow")
		} else {
			sess.pending = append(sess.pending, pkt)
			sess.pendingBytes += len(pkt)
		}
		sess.mu.Unlock()
	}
}

func (e *tunEngine) drain(agentID string, budget int) []socksFrame {
	e.mu.Lock()
	sess := e.sessions[agentID]
	e.mu.Unlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	pending := sess.pending
	sess.pending = nil
	sess.pendingBytes = 0
	if len(pending) > 0 {
		sess.lastActive = time.Now()
	}
	sess.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	frames := make([]socksFrame, 0, len(pending))
	used := 0
	i := 0
	for ; i < len(pending); i++ {
		// budget<=0 drains all; otherwise cap total bytes, requeueing the
		// remainder losslessly below.
		if budget > 0 && used+len(pending[i]) > budget {
			break
		}
		frames = append(frames, socksFrame{ConnID: 0, Action: "tun_data", Data: pending[i]})
		used += len(pending[i])
	}
	if rest := pending[i:]; len(rest) > 0 {
		// Budget cut the backlog: requeue the remainder at the head, in
		// order, so packets cross in later beacons instead of dropping.
		sess.mu.Lock()
		sess.pending = append(rest, sess.pending...)
		for _, pkt := range rest {
			sess.pendingBytes += len(pkt)
		}
		sess.mu.Unlock()
	}
	return frames
}

func (e *tunEngine) handleAgentFrame(agentID, action string, data []byte) {
	e.mu.Lock()
	sess := e.sessions[agentID]
	e.mu.Unlock()
	if sess == nil {
		if action == "tun_up" || action == "tun_data" {
			sess = &tunSession{agentID: agentID, status: "up", lastActive: time.Now(), stop: make(chan struct{})}
			e.mu.Lock()
			e.sessions[agentID] = sess
			e.mu.Unlock()
		} else {
			return
		}
	}
	switch action {
	case "tun_up":
		sess.mu.Lock()
		sess.status = "up"
		sess.iface = strings.TrimSpace(string(data))
		sess.lastActive = time.Now()
		sess.mu.Unlock()
	case "tun_down":
		sess.mu.Lock()
		sess.status = "down"
		sess.lastActive = time.Now()
		sess.mu.Unlock()
	case "tun_data":
		if len(data) == 0 {
			return
		}
		if len(data) > tunMaxPacketBytes {
			slog.Warn("tun helper: oversize packet from agent, dropping", "agent_id", agentID, "bytes", len(data))
			e.noteDrop("tun_oversize")
			return
		}
		sess.mu.Lock()
		pc, peer := sess.udpConn, sess.lastPeer
		sess.lastActive = time.Now()
		sess.mu.Unlock()
		if pc == nil || peer == nil {
			slog.Warn("tun helper: data with no UDP peer, dropping", "agent_id", agentID)
			e.noteDrop("tun_overflow")
			return
		}
		_, _ = pc.WriteToUDP(data, peer)
	}
}

func (e *tunEngine) stop(agentID string) error {
	e.mu.Lock()
	sess, ok := e.sessions[agentID]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("no tun helper for agent %s", agentID)
	}
	delete(e.sessions, agentID)
	e.mu.Unlock()
	// stopOnce: concurrent Stop + sweep double-close used to panic on a bare
	// close(stopCh). The nil-guard covers auto-created sessions from
	// handleAgentFrame, which never owned a UDP helper.
	sess.stopOnce.Do(func() {
		if sess.stop != nil {
			close(sess.stop)
		}
	})
	if sess.udpConn != nil {
		_ = sess.udpConn.Close()
	}
	return nil
}

// stopAll tears down every helper. Used by server shutdown so UDP sockets
// and udpLoop goroutines don't leak across the restart path.
func (e *tunEngine) stopAll() {
	e.mu.Lock()
	agents := make([]string, 0, len(e.sessions))
	for id := range e.sessions {
		agents = append(agents, id)
	}
	e.mu.Unlock()
	for _, id := range agents {
		_ = e.stop(id)
	}
}

// cleanupStaleTun reaps helper sessions for dead agents plus sessions idle
// past tunSessionIdleTimeout. TUN previously had no periodic GC at all —
// unlike SOCKS/rportfwd/lportfwd — so a dead agent with no explicit tun_down
// leaked its UDP socket forever.
func (s *Server) cleanupStaleTun() {
	if s.tunEngine == nil {
		return
	}
	s.tunEngine.mu.Lock()
	type snapshot struct {
		agentID    string
		lastActive time.Time
	}
	snaps := make([]snapshot, 0, len(s.tunEngine.sessions))
	agentSet := make(map[string]bool)
	for id, sess := range s.tunEngine.sessions {
		sess.mu.Lock()
		la := sess.lastActive
		sess.mu.Unlock()
		snaps = append(snaps, snapshot{agentID: id, lastActive: la})
		agentSet[id] = true
	}
	s.tunEngine.mu.Unlock()
	if len(snaps) == 0 {
		return
	}
	agentIDs := make([]string, 0, len(agentSet))
	for id := range agentSet {
		agentIDs = append(agentIDs, id)
	}
	var agents []db.Implant
	if err := s.db.Where("id IN ?", agentIDs).Limit(len(agentIDs)).Find(&agents).Error; err != nil {
		slog.Error("Failed to batch-load agents for tun cleanup", "error", err)
		return
	}
	byID := make(map[string]db.Implant, len(agents))
	for _, a := range agents {
		byID[a.ID] = a
	}
	now := time.Now()
	threshold := s.offlineThreshold() * 2
	for _, sn := range snaps {
		a, ok := byID[sn.agentID]
		if !ok || now.Sub(a.LastSeen) > threshold {
			slog.Info("tun helper: reaping session for dead agent", "agent_id", sn.agentID)
			_ = s.tunEngine.stop(sn.agentID)
			continue
		}
		if !sn.lastActive.IsZero() && now.Sub(sn.lastActive) > tunSessionIdleTimeout {
			slog.Info("tun helper: reaping idle session", "agent_id", sn.agentID)
			_ = s.tunEngine.stop(sn.agentID)
		}
	}
}

func (e *tunEngine) active(agentID string) bool {
	sess := e.get(agentID)
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.status == "up" || sess.udpConn != nil
}

func (s *Server) handleTunStart(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	cidr := strings.TrimSpace(c.PostForm("cidr"))
	if cidr == "" {
		cidr = "10.66.0.2/24"
	}
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		respondError(c, http.StatusBadRequest, "invalid cidr, want e.g. 10.66.0.2/24")
		return
	}
	port := 0
	if v := c.PostForm("udp_port"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 65535 {
			respondError(c, http.StatusBadRequest, "invalid udp_port")
			return
		}
		port = n
	}
	if s.tunEngine == nil {
		s.tunEngine = newTunEngine()
	}
	actual, err := s.tunEngine.startUDP(s, id, cidr, port)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "tun helper"))
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "tun_start", Command: cidr})
	if task == nil {
		// Roll back the local UDP listener; the error response is already written.
		_ = s.tunEngine.stop(id)
		return
	}
	s.LogAuditRecord(c, "tun_start", "agent", id, fmt.Sprintf("cidr=%s udp=127.0.0.1:%d", cidr, actual), true, nil)
	s.broadcastTaskUpdate(task.AgentID, *task)
	respond(c, gin.H{

		"task_id":  task.ID,
		"cidr":     cidr,
		"udp_host": "127.0.0.1",
		"udp_port": actual,
		"note":     "Linux implant opens /dev/net/tun; Windows implants use the bundled signed Wintun runtime (adapter creation needs an elevated implant). Each UDP datagram on 127.0.0.1 is one IP packet. macOS implants return an honest error.",
	})
}

func (s *Server) handleTunStop(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if s.tunEngine != nil {
		_ = s.tunEngine.stop(id)
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "tun_stop"})
	if task == nil {
		return
	}
	s.dispatchTask(c, task, "tun_stop", "")
}

func (s *Server) handleTunStatus(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	out := gin.H{"success": true, "running": false, "host": "127.0.0.1"}
	if s.tunEngine != nil {
		if sess := s.tunEngine.get(id); sess != nil {
			sess.mu.Lock()
			out["running"] = sess.status == "up" || sess.udpConn != nil
			out["status"] = sess.status
			out["cidr"] = sess.cidr
			out["iface"] = sess.iface
			out["udp_port"] = sess.port
			sess.mu.Unlock()
		}
	}
	respond(c, out)
}
