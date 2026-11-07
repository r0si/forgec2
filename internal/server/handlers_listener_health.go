package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/miekg/dns"
)

// Listener self-check heartbeat: periodically probes every enabled listener
// with a synthetic connection so a dead transport is detected within minutes
// instead of being discovered by missing beacons. Complements the QUIC/DNS
// accept-loop fixes by keeping Status honest.

type listenerHealth struct {
	ListenerID uint      `json:"listener_id"`
	Name       string    `json:"name"`
	Scheme     string    `json:"scheme"`
	Addr       string    `json:"addr"`
	LastCheck  time.Time `json:"last_check"`
	OK         bool      `json:"ok"`
	Failures   int       `json:"consecutive_failures"`
	Error      string    `json:"error,omitempty"`
	Skipped    bool      `json:"skipped"` // transports without a synthetic probe yet
}

type listenerHealthTracker struct {
	mu    sync.Mutex
	state map[uint]*listenerHealth
}

const (
	listenerHealthInterval = 5 * time.Minute
	listenerProbeTimeout   = 5 * time.Second
	listenerFailThreshold  = 2 // consecutive failures before status flips
)

var lhTracker = &listenerHealthTracker{state: map[uint]*listenerHealth{}}

func (t *listenerHealthTracker) snapshot() []listenerHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]listenerHealth, 0, len(t.state))
	for _, h := range t.state {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ListenerID < out[j].ListenerID
	})
	return out
}

// observe records a probe and carries forward the consecutive failure count.
// A successful or unsupported probe starts a fresh sequence.
func (t *listenerHealthTracker) observe(h *listenerHealth) {
	t.mu.Lock()
	defer t.mu.Unlock()

	previous := t.state[h.ListenerID]
	if h.OK || h.Skipped {
		h.Failures = 0
	} else {
		h.Failures = 1
		if previous != nil && !previous.OK && !previous.Skipped {
			h.Failures = previous.Failures + 1
		}
	}
	t.state[h.ListenerID] = h
}

// prune removes disabled or deleted listeners from the API snapshot.
func (t *listenerHealthTracker) prune(active map[uint]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.state {
		if _, ok := active[id]; !ok {
			delete(t.state, id)
		}
	}
}

func (s *Server) startListenerHealthLoop() {
	slog.Info("Listener health heartbeat starting", "interval", listenerHealthInterval)
	ticker := time.NewTicker(listenerHealthInterval)
	initial := time.NewTimer(15 * time.Second)
	defer ticker.Stop()
	defer initial.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-initial.C:
			// First pass shortly after boot so status is populated fast.
			s.checkAllListeners()
		case <-ticker.C:
			s.checkAllListeners()
		}
	}
}

func (s *Server) checkAllListeners() {
	var listeners []db.Listener
	if err := s.db.Where("enabled = ?", true).Find(&listeners).Error; err != nil {
		slog.Error("Listener health: query failed", "err", err)
		return
	}
	active := make(map[uint]struct{}, len(listeners))
	for i := range listeners {
		active[listeners[i].ID] = struct{}{}
	}
	lhTracker.prune(active)

	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, ln := range listeners {
		if err := ctx.Err(); err != nil {
			return
		}
		h := s.probeListener(ctx, &ln)
		lhTracker.observe(h)

		// Use a conditional update as the transition gate. It both retries a
		// failed persistence on the next pass and prevents duplicate alerts if
		// two health sweeps overlap.
		if !h.OK && !h.Skipped && h.Failures >= listenerFailThreshold {
			res := s.db.Model(&db.Listener{}).
				Where("id = ? AND status <> ?", ln.ID, "error").
				Update("status", "error")
			if res.Error != nil {
				slog.Error("Listener health: failed to persist failure", "listener_id", ln.ID, "err", res.Error)
				continue
			}
			if res.RowsAffected == 0 {
				continue
			}
			s.DispatchNotification(&db.Notification{
				Type:     "listener_health",
				Title:    "Listener unreachable: " + ln.Name,
				Message:  fmt.Sprintf("%s listener %s:%d failed %d consecutive health checks (%s)", ln.Scheme, ln.Host, ln.Port, h.Failures, h.Error),
				Severity: "critical",
			})
			s.broadcastOperatorEvent(map[string]interface{}{
				"type": "listener_health", "listener_id": ln.ID,
				"name": ln.Name, "status": "failed", "error": h.Error,
			})
			slog.Warn("Listener health FAILED", "name", ln.Name, "addr", fmt.Sprintf("%s:%d", ln.Host, ln.Port), "err", h.Error)
		} else if h.OK && !h.Skipped {
			// Recovery: restore honest running state.
			res := s.db.Model(&db.Listener{}).
				Where("id = ? AND status = ?", ln.ID, "error").
				Update("status", "running")
			if res.Error != nil {
				slog.Error("Listener health: failed to persist recovery", "listener_id", ln.ID, "err", res.Error)
				continue
			}
			if res.RowsAffected == 0 {
				continue
			}
			s.broadcastOperatorEvent(map[string]interface{}{
				"type": "listener_health", "listener_id": ln.ID,
				"name": ln.Name, "status": "recovered",
			})
			slog.Info("Listener health recovered", "name", ln.Name)
		}
	}
}

// handleListenerHealth was removed. The UI reads listener health from
// /circuit-breaker/detail; nothing called GET /api/listeners/health. The
// in-memory tracker and prober that feed it are still used internally.

func (s *Server) probeListener(ctx context.Context, ln *db.Listener) *listenerHealth {
	h := &listenerHealth{
		ListenerID: ln.ID,
		Name:       ln.Name,
		Scheme:     ln.Scheme,
		Addr:       net.JoinHostPort(ln.Host, strconv.Itoa(ln.Port)),
		LastCheck:  time.Now(),
	}

	switch ln.Scheme {
	case "http", "tcp":
		h.OK = probeTCP(ctx, h.Addr)
		if !h.OK {
			h.Error = "tcp connect failed"
		}
	case "https", "tls":
		if s.listenerRequiresClientCert(ln) {
			// The probe presents no client certificate, so a real handshake
			// would fail even though the listener is healthy. Probe TCP
			// reachability and report the shallower check explicitly instead
			// of turning a healthy mTLS listener into a false "error" row (or
			// a silent green).
			h.OK = probeTCP(ctx, h.Addr)
			if !h.OK {
				h.Error = "tcp connect failed"
				break
			}
			h.Skipped = true
			h.Error = "client certificates required: probed TCP reachability only"
			break
		}
		ok, err := probeTLS(ctx, h.Addr)
		h.OK = ok
		if !ok {
			h.Error = err.Error()
		}
	case "dns":
		ok, err := probeDNS(ctx, ln.Host, ln.Port, ln.DNSDomain)
		h.OK = ok
		if !ok {
			h.Error = err.Error()
		}
	case "ssh":
		ok, err := probeSSH(ctx, h.Addr)
		h.OK = ok
		if !ok {
			h.Error = err.Error()
		}
	case "h2c":
		ok, err := probeH2C(ctx, h.Addr)
		h.OK = ok
		if !ok {
			h.Error = err.Error()
		}
	case "udp":
		// UDP has no handshake: liveness = our own bound handle. A missing
		// handle with an enabled row means the socket died unnoticed.
		h.OK, h.Error = s.udpListenerLive(ln), ""
		if !h.OK {
			h.Error = "no live local UDP handle for enabled row"
		}
	case "quic":
		// Same: QUIC flips running=false permanently on repeated accept
		// errors, which this surfaces instead of a forever-green row.
		h.OK, h.Error = s.quicListenerLive(), ""
		if !h.OK {
			h.Error = "QUIC listener not running"
		}
	default:
		// icmp/grpc/smb and anything else: no cheap synthetic probe. Report
		// the gap explicitly instead of a silent green.
		h.Skipped = true
		h.OK = true // do not count unknown transports as failures
		h.Error = "no synthetic probe for scheme " + ln.Scheme + " (process state only)"
	}
	return h
}

// udpListenerLive reports whether this server holds a live UDP socket for
// the row: an extra-listener handle, or the main server UDP socket when the
// row matches the configured main address.
func (s *Server) udpListenerLive(ln *db.Listener) bool {
	key := "udp://" + net.JoinHostPort(ln.Host, strconv.Itoa(ln.Port))
	s.extraListenersMu.Lock()
	handle := s.extraListeners[key]
	s.extraListenersMu.Unlock()
	if handle != nil {
		return true
	}
	return s.udpConn != nil
}

// quicListenerLive mirrors udpListenerLive for QUIC (main listener only;
// extra QUIC listeners register in extraListeners when started via UI).
func (s *Server) quicListenerLive() bool {
	if s.quicListener != nil && s.quicListener.IsRunning() {
		return true
	}
	s.extraListenersMu.Lock()
	defer s.extraListenersMu.Unlock()
	for key, handle := range s.extraListeners {
		if len(key) >= 7 && key[:7] == "quic://" && handle != nil {
			return true
		}
	}
	return false
}

func probeTCP(ctx context.Context, addr string) bool {
	dialer := net.Dialer{Timeout: listenerProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func probeTLS(ctx context.Context, addr string) (bool, error) {
	dialer := net.Dialer{Timeout: listenerProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// InsecureVerify here is intentional: we only need the handshake to
	// complete to prove a TLS listener is alive; self-signed C2 certs are
	// expected to fail chain validation. VerifyConnection still rejects an
	// EXPIRED leaf so a dead-cert listener cannot report healthy.
	tlsConn := tls.Client(conn, &tls.Config{
		// #nosec G402 -- liveness probe only; VerifyConnection below rejects expired leaves.
		InsecureSkipVerify: true, // #nosec G402
		ServerName:         "probe",
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("peer presented no certificate")
			}
			if time.Now().After(cs.PeerCertificates[0].NotAfter) {
				return fmt.Errorf("peer certificate expired")
			}
			return nil
		},
	})
	if err := tlsConn.SetDeadline(time.Now().Add(listenerProbeTimeout)); err != nil {
		return false, err
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return false, err
	}
	_ = tlsConn.Close()
	return true, nil
}

// probeSSH dials and reads the server version banner ("SSH-2.0-...").
// Any banner proves the SSH listener is accepting; the handshake itself is
// never attempted.
func probeSSH(ctx context.Context, addr string) (bool, error) {
	dialer := net.Dialer{Timeout: listenerProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(listenerProbeTimeout)); err != nil {
		return false, err
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return false, err
	}
	if n < 4 || string(buf[:4]) != "SSH-" {
		return false, fmt.Errorf("no SSH banner (got %q)", strings.TrimSpace(string(buf[:n])))
	}
	return true, nil
}

// probeH2C speaks minimal prior-knowledge HTTP/2: connection preface plus an
// empty SETTINGS frame, then expects the server's SETTINGS frame back. Any
// frame bytes prove the cleartext HTTP/2 listener is serving.
func probeH2C(ctx context.Context, addr string) (bool, error) {
	dialer := net.Dialer{Timeout: listenerProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(listenerProbeTimeout)); err != nil {
		return false, err
	}
	preface := []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	emptySettings := []byte{0, 0, 0, 0x4, 0, 0, 0, 0, 0}
	if _, err := conn.Write(append(preface, emptySettings...)); err != nil {
		return false, err
	}
	header := make([]byte, 9)
	if _, err := io.ReadFull(conn, header); err != nil {
		return false, err
	}
	return true, nil
}

func probeDNS(ctx context.Context, host string, port int, zone string) (bool, error) {
	if zone == "" {
		zone = "health-check.probe"
	}
	target := fmt.Sprintf("%s:%d", host, port)
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn("probe."+zone), dns.TypeTXT)
	client := &dns.Client{Net: "udp", Timeout: listenerProbeTimeout}
	_, _, err := client.ExchangeContext(ctx, m, target)
	if err != nil {
		return false, err
	}
	// The ForgeC2 handler answers every query (even unknown UUIDs) — any
	// response proves the UDP socket is being served.
	return true, nil
}
