package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func newCookieProxyTestServer(t *testing.T) *Server {
	t.Helper()
	s := mustTenantServer(t)
	s.cookieProxy = newCookieProxyEngine()
	return s
}

// cookieProxyCtx builds an operator/viewer context pinned to one agent.
func cookieProxyCtx(s *Server, t *testing.T, username, role string, tenantID uint, agentID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, username, tenantID)
	c.Set("user_role", role)
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	return c, w
}

// seedCookieJar stores a completed cookie_export result for an agent.
func seedCookieJar(t *testing.T, s *Server, agentID string, output string) {
	t.Helper()
	if err := s.db.Create(&db.Task{
		TenantID: 1, AgentID: agentID, Type: "cookie_export", Command: "export",
		Status: "completed", Result: output,
	}).Error; err != nil {
		t.Fatalf("seed cookie export: %v", err)
	}
}

const sampleJarOutput = `=== JSON ===
[{"domain":"example.com","name":"sid","path":"/","value":"abc123","expires":1700000000,"secure":false}]
=== END ===`

// listenSilent accepts connections and holds them open, standing in for a
// reachable-but-unresponsive target.
func listenSilent(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		var held []net.Conn
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// held pins the accepted conns open for the test's lifetime; the
			// append result is intentionally never read.
			held = append(held, c) //nolint:staticcheck // intentional hold-open sink
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func trackedTunnels(s *cookieProxySession) int {
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	return len(s.tunnels)
}

// ── Engine lifecycle ────────────────────────────────────────────────────────

// TestCookieProxyEngineLifecycle proves ingest/start/get/stop behave and that
// start is idempotent on a running session.
func TestCookieProxyEngineLifecycle(t *testing.T) {
	e := newCookieProxyEngine()

	e.ingest("a1", parseCookieExport(sampleJarOutput))
	sess := e.get("a1")
	if sess == nil || len(sess.jars) != 1 {
		t.Fatalf("ingest did not register a jar: %+v", sess)
	}

	port, err := e.start("a1", 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if port == 0 {
		t.Fatal("start returned port 0")
	}
	if got := e.get("a1"); got == nil || got.port != port {
		t.Fatalf("session = %+v, want port %d", got, port)
	}
	// A restart must not bind a second listener or lose the jar.
	again, err := e.start("a1", 0)
	if err != nil || again != port {
		t.Fatalf("restart = %d, %v; want the running %d", again, err, port)
	}
	if got := e.get("a1"); len(got.jars) != 1 {
		t.Fatalf("restart dropped the cookie jar: %+v", got.jars)
	}

	if err := e.stop("a1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if e.get("a1") != nil {
		t.Fatal("session survived stop")
	}
	if err := e.stop("a1"); err == nil {
		t.Fatal("stopping an unknown session reported success")
	}

	// The listener must actually be closed.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("listener still accepting after stop")
	}
}

// TestCookieProxyIngestIgnoresEmptyJar proves an empty export does not create a
// session that would later report "running" with no cookies.
func TestCookieProxyIngestIgnoresEmptyJar(t *testing.T) {
	e := newCookieProxyEngine()
	e.ingest("a1", nil)
	e.ingest("a1", []cookieJarEntry{})
	if e.get("a1") != nil {
		t.Fatal("an empty jar created a session")
	}
}

// TestCookieProxyStopClosesHijackedTunnels is the regression for the leak:
// http.Server.Close does not touch a hijacked CONNECT connection, so a stopped
// proxy used to keep relaying until the remote peer gave up.
func TestCookieProxyStopClosesHijackedTunnels(t *testing.T) {
	e := newCookieProxyEngine()
	port, err := e.start("tun-agent", 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	backend := listenSilent(t)

	proxyConn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer proxyConn.Close()

	line := "CONNECT " + backend.Addr().String() + " HTTP/1.1\r\nHost: " + backend.Addr().String() + "\r\n\r\n"
	if _, err := proxyConn.Write([]byte(line)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	buf := make([]byte, 64)
	_ = proxyConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := proxyConn.Read(buf)
	if err != nil {
		t.Fatalf("read CONNECT reply: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("CONNECT reply = %q, want 200", buf[:n])
	}

	// The tunnel must be registered before stop runs.
	sess := e.get("tun-agent")
	deadline := time.Now().Add(3 * time.Second)
	for trackedTunnels(sess) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if trackedTunnels(sess) == 0 {
		t.Fatal("an established CONNECT tunnel was never tracked")
	}

	if err := e.stop("tun-agent"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The client side of a torn-down tunnel must see EOF promptly.
	_ = proxyConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := proxyConn.Read(buf); err == nil {
		t.Fatal("tunnel still delivering data after the proxy was stopped")
	}
	if got := trackedTunnels(sess); got != 0 {
		t.Fatalf("%d tunnels still registered after stop", got)
	}
}

// ── Request proxying ────────────────────────────────────────────────────────

// TestCookieProxyServeHTTPInjectsAndStripsHeaders proves the proxy injects the
// harvested cookie for the target host, keeps a caller-supplied cookie, and
// does not forward hop-by-hop headers in either direction (RFC 7230 §6.1).
func TestCookieProxyServeHTTPInjectsAndStripsHeaders(t *testing.T) {
	var gotCookie, gotProxyAuth, gotConnection, gotKeepAlive string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotProxyAuth = r.Header.Get("Proxy-Authorization")
		gotConnection = r.Header.Get("Connection")
		gotKeepAlive = r.Header.Get("Keep-Alive")
		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("Proxy-Authenticate", "Basic realm=upstream")
		w.Header().Set("Keep-Alive", "timeout=5")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	// The upstream stands in for the victim site, so the jar is scoped to its
	// host and the proxy dials it directly (loopback is never env-proxied).
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	upstreamName, _, err := net.SplitHostPort(upstreamHost)
	if err != nil {
		t.Fatalf("split upstream host: %v", err)
	}

	sess := &cookieProxySession{
		agentID: "a1",
		jars:    []cookieJarEntry{{Domain: upstreamName, Name: "sid", Value: "abc123"}},
		tunnels: make(map[net.Conn]struct{}),
	}

	proxy := httptest.NewServer(&cookieProxyHandler{sess: sess})
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, err := http.NewRequest(http.MethodGet, "http://"+upstreamHost+"/dashboard", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Cookie", "own=1")
	req.Header.Set("Proxy-Authorization", "Basic leaked")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=9")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}

	if gotCookie != "own=1; sid=abc123" {
		t.Fatalf("Cookie = %q, want the caller's cookie plus the injected one", gotCookie)
	}
	if gotProxyAuth != "" {
		t.Fatalf("Proxy-Authorization leaked upstream: %q", gotProxyAuth)
	}
	if gotConnection != "" {
		t.Fatalf("hop-by-hop Connection forwarded upstream: %q", gotConnection)
	}
	if gotKeepAlive != "" {
		t.Fatalf("hop-by-hop Keep-Alive forwarded upstream: %q", gotKeepAlive)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Fatalf("end-to-end header lost: %v", resp.Header)
	}
	if resp.Header.Get("Proxy-Authenticate") != "" {
		t.Fatalf("upstream Proxy-Authenticate relayed to the client: %q", resp.Header.Get("Proxy-Authenticate"))
	}
	if resp.Header.Get("Keep-Alive") != "" {
		t.Fatalf("upstream Keep-Alive relayed to the client: %q", resp.Header.Get("Keep-Alive"))
	}
}

// TestCookieProxyServeHTTPGuards proves the malformed and unreachable request
// paths answer with a status instead of a hung or opaque failure. Port 0 is
// never a valid destination, so the dial fails immediately.
func TestCookieProxyServeHTTPGuards(t *testing.T) {
	sess := &cookieProxySession{agentID: "a1", tunnels: make(map[net.Conn]struct{})}
	handler := &cookieProxyHandler{sess: sess}

	t.Run("missing host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
		req.Host = ""
		req.URL = &url.URL{Path: "/"}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("unreachable upstream", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
		req.Host = "127.0.0.1:0"
		req.URL = &url.URL{Path: "/"}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", w.Code)
		}
	})

	t.Run("connect without host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodConnect, "/", nil)
		req.Host = ""
		w := httptest.NewRecorder()
		handler.handleConnect(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}

// TestCookieProxyHandleConnectRefusesUnreachableTarget proves a CONNECT to a
// dead destination answers 502 without leaving a half-open tunnel registered.
func TestCookieProxyHandleConnectRefusesUnreachableTarget(t *testing.T) {
	sess := &cookieProxySession{agentID: "a1", tunnels: make(map[net.Conn]struct{})}
	handler := &cookieProxyHandler{sess: sess}

	req := httptest.NewRequest(http.MethodConnect, "/", nil)
	req.Host = "127.0.0.1:0"
	w := httptest.NewRecorder()
	handler.handleConnect(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if trackedTunnels(sess) != 0 {
		t.Fatalf("a failed CONNECT left %d tunnels registered", trackedTunnels(sess))
	}
}

// ── Handler tenant gates ────────────────────────────────────────────────────

// TestCookieProxyHandlersAreTenantScoped proves every cookie-proxy endpoint
// resolves the agent within the caller's tenant. The jar is stolen session
// material and stop tears down a live pivot, so a foreign agent must be
// invisible — start already gated this, the rest did not.
func TestCookieProxyHandlersAreTenantScoped(t *testing.T) {
	cases := []struct {
		name   string
		invoke func(s *Server, c *gin.Context)
	}{
		{"stop", func(s *Server, c *gin.Context) { s.handleCookieProxyStop(c) }},
		{"status", func(s *Server, c *gin.Context) { s.handleCookieProxyStatus(c) }},
		{"jar", func(s *Server, c *gin.Context) { s.handleCookieProxyJar(c) }},
		{"netscape", func(s *Server, c *gin.Context) { s.handleCookieProxyNetscape(c) }},
		{"start", func(s *Server, c *gin.Context) { s.handleCookieProxyStart(c) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCookieProxyTestServer(t)
			seedTenantAgent(t, s, "cp-foreign", 2)
			seedCookieJar(t, s, "cp-foreign", sampleJarOutput)

			c, w := cookieProxyCtx(s, t, "cp-op", "operator", 1, "cp-foreign")
			tc.invoke(s, c)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d body = %s, want 404 for another tenant's agent", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "sid") || strings.Contains(w.Body.String(), "abc123") {
				t.Fatalf("cookie material leaked in the response: %s", w.Body.String())
			}
			if s.cookieProxy.get("cp-foreign") != nil {
				t.Fatal("a refused start still created a proxy session")
			}
		})
	}
}

// TestCookieProxyHandlersRefuseViewer proves the operator gate holds on every
// cookie-proxy endpoint.
func TestCookieProxyHandlersRefuseViewer(t *testing.T) {
	s := newCookieProxyTestServer(t)
	seedTenantAgent(t, s, "cp-own", 1)

	for _, invoke := range []func(s *Server, c *gin.Context){
		func(s *Server, c *gin.Context) { s.handleCookieProxyStart(c) },
		func(s *Server, c *gin.Context) { s.handleCookieProxyStop(c) },
		func(s *Server, c *gin.Context) { s.handleCookieProxyStatus(c) },
		func(s *Server, c *gin.Context) { s.handleCookieProxyJar(c) },
		func(s *Server, c *gin.Context) { s.handleCookieProxyNetscape(c) },
	} {
		c, w := cookieProxyCtx(s, t, "cp-viewer", "viewer", 1, "cp-own")
		invoke(s, c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("viewer: status = %d, want 403", w.Code)
		}
	}
}

// TestCookieProxyStatusAndJarForOwnAgent proves the own-tenant paths still work
// and report the jar the operator is entitled to.
func TestCookieProxyStatusAndJarForOwnAgent(t *testing.T) {
	s := newCookieProxyTestServer(t)
	seedTenantAgent(t, s, "cp-own", 1)
	seedCookieJar(t, s, "cp-own", sampleJarOutput)

	c, w := cookieProxyCtx(s, t, "cp-op", "operator", 1, "cp-own")
	s.handleCookieProxyStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status endpoint: %d %s", w.Code, w.Body.String())
	}
	var statusBody struct {
		Running bool   `json:"running"`
		Cookies int    `json:"cookies"`
		Host    string `json:"host"`
	}
	unmarshalData(t, w.Body.Bytes(), &statusBody)
	if statusBody.Running || statusBody.Host != "127.0.0.1" {
		t.Fatalf("status body = %+v", statusBody)
	}
	if statusBody.Cookies != 1 {
		t.Fatalf("cookies = %d, want the seeded jar's single entry", statusBody.Cookies)
	}

	c2, w2 := cookieProxyCtx(s, t, "cp-op", "operator", 1, "cp-own")
	s.handleCookieProxyJar(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("jar endpoint: %d %s", w2.Code, w2.Body.String())
	}
	var jarBody struct {
		Cookies []cookieJarEntry `json:"cookies"`
		Count   int              `json:"count"`
	}
	unmarshalData(t, w2.Body.Bytes(), &jarBody)
	if jarBody.Count != 1 || jarBody.Cookies[0].Name != "sid" {
		t.Fatalf("jar body = %+v", jarBody)
	}

	// The Netscape export is a file download of the same material.
	c3, w3 := cookieProxyCtx(s, t, "cp-op", "operator", 1, "cp-own")
	s.handleCookieProxyNetscape(c3)
	if w3.Code != http.StatusOK {
		t.Fatalf("netscape endpoint: %d", w3.Code)
	}
	if !strings.Contains(w3.Body.String(), "sid") {
		t.Fatalf("netscape export missing the cookie: %s", w3.Body.String())
	}
}

// TestCookieProxyStopWithoutProxyEngine proves the stop endpoint reports the
// missing engine instead of panicking on a nil engine.
func TestCookieProxyStopWithoutProxyEngine(t *testing.T) {
	s := newCookieProxyTestServer(t)
	seedTenantAgent(t, s, "cp-none", 1)
	s.cookieProxy = nil

	c, w := cookieProxyCtx(s, t, "cp-op", "operator", 1, "cp-none")
	s.handleCookieProxyStop(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestCookieProxyStartRejectsInvalidPort proves the port bound is validated
// before a listener is opened.
func TestCookieProxyStartRejectsInvalidPort(t *testing.T) {
	s := newCookieProxyTestServer(t)
	seedTenantAgent(t, s, "cp-port", 1)

	for _, form := range []string{"port=notanumber", "port=-1", "port=70000"} {
		c, w := cookieProxyCtx(s, t, "cp-op", "operator", 1, "cp-port")
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.handleCookieProxyStart(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", form, w.Code)
		}
	}
	if s.cookieProxy.get("cp-port") != nil {
		t.Fatal("a rejected port still created a session")
	}
}

// ── Concurrency ─────────────────────────────────────────────────────────────

// TestConcurrentCookieProxyLifecycle exercises ingest/start/get/stop under
// -race to pin the engine's locking.
func TestConcurrentCookieProxyLifecycle(t *testing.T) {
	e := newCookieProxyEngine()
	if _, err := e.start("race-agent", 0); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = e.stop("race-agent") }()

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			agent := "race-agent-" + string(rune('a'+worker))
			for i := 0; i < 10; i++ {
				e.ingest(agent, parseCookieExport(sampleJarOutput))
				_, _ = e.start(agent, 0)
				if sess := e.get(agent); sess != nil {
					sess.closeTunnels()
				}
			}
		}(w)
	}
	wg.Wait()
	for _, agent := range []string{"race-agent-b", "race-agent-c", "race-agent-d"} {
		if err := e.stop(agent); err != nil {
			t.Fatalf("stop %s: %v", agent, err)
		}
	}
}
