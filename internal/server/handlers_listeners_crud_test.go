package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func newListenerCRUDTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.DefaultConfig()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 8000
	cfg.Server.SSHHostKey = "" // never write a real host key file during tests
	return &Server{
		db:                testutil.SetupTestDB(t),
		cfg:               cfg,
		extraListeners:    make(map[string]io.Closer),
		wsClients:         make(map[*websocket.Conn]*wsClientConn),
		rportfwdListeners: make(map[string]*rportfwdRelay),
	}
}

func newJSONRequest(method, path, body string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return w, c
}

func TestHandleCreateListener_Validation(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	t.Run("missing name auto-generates name", func(t *testing.T) {
		w, c := newJSONRequest(http.MethodPost, "/api/listeners", `{"scheme":"http","host":"127.0.0.1","port":8080}`)
		s.handleCreateListener(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Success  bool        `json:"success"`
			Listener db.Listener `json:"listener"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.Listener.Name != "Listener 8080" && resp.Listener.Name != "" {
			t.Errorf("expected auto-generated name, got %q", resp.Listener.Name)
		}
	})

	t.Run("scheme-less listener rejected", func(t *testing.T) {
		w, c := newJSONRequest(http.MethodPost, "/api/listeners", `{"name":"stub","host":"127.0.0.1","port":9090}`)
		s.handleCreateListener(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for scheme-less listener, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("unbindable scheme rejected", func(t *testing.T) {
		for _, scheme := range []string{"wss", "ws", "smb", "grpc", "mtls"} {
			body := `{"name":"bad-` + scheme + `","scheme":"` + scheme + `","host":"127.0.0.1","port":9443}`
			w, c := newJSONRequest(http.MethodPost, "/api/listeners", body)
			s.handleCreateListener(c)
			if w.Code != http.StatusBadRequest {
				t.Errorf("scheme %q: expected 400, got %d; body=%s", scheme, w.Code, w.Body.String())
			}
		}
	})

	t.Run("valid minimal listener created", func(t *testing.T) {
		body := `{"name":"test-http","scheme":"http","host":"127.0.0.1","port":8080}`
		w, c := newJSONRequest(http.MethodPost, "/api/listeners", body)
		s.handleCreateListener(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Listener db.Listener `json:"listener"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.Listener.Name != "test-http" {
			t.Errorf("expected name=test-http, got %q", resp.Listener.Name)
		}
		if resp.Listener.Port != 8080 {
			t.Errorf("expected port=8080, got %d", resp.Listener.Port)
		}
	})
}

func TestHandleCreateListener_SSHandH2C(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	t.Run("ssh listener created and normalized", func(t *testing.T) {
		body := `{"name":"test-ssh","scheme":"ssh","host":"127.0.0.1","port":2222}`
		w, c := newJSONRequest(http.MethodPost, "/api/listeners", body)
		s.handleCreateListener(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Listener db.Listener `json:"listener"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.Listener.Scheme != "ssh" {
			t.Errorf("expected scheme=ssh, got %q", resp.Listener.Scheme)
		}
		if resp.Listener.Type != "ssh" {
			t.Errorf("expected type=ssh, got %q", resp.Listener.Type)
		}
		var inDB db.Listener
		if err := s.db.First(&inDB, resp.Listener.ID).Error; err != nil {
			t.Fatalf("listener not persisted: %v", err)
		}
		if inDB.Protocol != "ssh" {
			t.Errorf("expected protocol=ssh, got %q", inDB.Protocol)
		}
	})

	t.Run("h2c listener created and normalized", func(t *testing.T) {
		body := `{"name":"test-h2c","scheme":"h2c","host":"127.0.0.1","port":8081}`
		w, c := newJSONRequest(http.MethodPost, "/api/listeners", body)
		s.handleCreateListener(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Listener db.Listener `json:"listener"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.Listener.Scheme != "h2c" {
			t.Errorf("expected scheme=h2c, got %q", resp.Listener.Scheme)
		}
		if resp.Listener.Type != "h2c" {
			t.Errorf("expected type=h2c, got %q", resp.Listener.Type)
		}
	})
}

func TestNormalizeListenerProtocol_SSHandH2C(t *testing.T) {
	cases := []struct {
		name      string
		in        db.Listener
		wantType  string
		wantSchem string
	}{
		{"scheme ssh", db.Listener{Scheme: "ssh"}, "ssh", "ssh"},
		{"scheme h2c", db.Listener{Scheme: "h2c"}, "h2c", "h2c"},
		{"protocol ssh", db.Listener{Protocol: "ssh"}, "ssh", "ssh"},
		{"protocol h2c", db.Listener{Protocol: "h2c"}, "h2c", "h2c"},
		{"type ssh", db.Listener{Type: "ssh"}, "ssh", "ssh"},
		{"type h2c", db.Listener{Type: "h2c"}, "h2c", "h2c"},
		{"scheme quic", db.Listener{Scheme: "quic"}, "quic", "quic"},
		{"scheme udp", db.Listener{Scheme: "udp"}, "udp", "udp"},
		{"type quic", db.Listener{Type: "quic"}, "quic", "quic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.in
			normalizeListenerProtocol(&l)
			if l.Type != tc.wantType {
				t.Errorf("type: got %q want %q", l.Type, tc.wantType)
			}
			if l.Scheme != tc.wantSchem {
				t.Errorf("scheme: got %q want %q", l.Scheme, tc.wantSchem)
			}
			if l.Protocol != tc.wantSchem {
				t.Errorf("protocol: got %q want %q", l.Protocol, tc.wantSchem)
			}
		})
	}
}

func TestHandleListenerDetail(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	listener := db.Listener{
		Name:   "detail-test",
		Scheme: "https",
		Host:   "c2.example.com",
		Port:   443,
		Status: "running",
	}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatalf("seed listener: %v", err)
	}

	t.Run("existing listener returns detail", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/listeners/detail", nil)
		c.Params = gin.Params{{Key: "id", Value: itoa(int(listener.ID))}}
		s.handleListenerDetail(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("nonexistent listener returns 404", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/listeners/detail", nil)
		c.Params = gin.Params{{Key: "id", Value: "99999"}}
		s.handleListenerDetail(c)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d; body=%s", w.Code, w.Body.String())
		}
	})
}

func TestHandleAPIGetListener(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	listener := db.Listener{
		Name:    "api-detail-test",
		Scheme:  "http",
		Host:    "127.0.0.1",
		Port:    8443,
		Status:  "running",
		Enabled: true,
	}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	agent := db.Implant{
		ID:         "api-detail-agent-1",
		Hostname:   "win-host",
		IP:         "10.0.0.5",
		OS:         "Windows 11 Pro",
		Arch:       "amd64",
		Status:     "online",
		ListenerID: listener.ID,
		LastSeen:   time.Now(),
	}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	t.Run("returns listener with agents", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/listeners/detail", nil)
		c.Params = gin.Params{{Key: "id", Value: itoa(int(listener.ID))}}
		s.handleAPIGetListener(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Listener db.Listener  `json:"listener"`
			Agents   []db.Implant `json:"agents"`
			Total    int          `json:"total"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.Listener.ID != listener.ID {
			t.Errorf("expected listener id %d, got %d", listener.ID, resp.Listener.ID)
		}
		if resp.Total != 1 || len(resp.Agents) != 1 {
			t.Fatalf("expected 1 agent, got total=%d len=%d", resp.Total, len(resp.Agents))
		}
		if resp.Agents[0].Hostname != "win-host" {
			t.Errorf("expected hostname win-host, got %q", resp.Agents[0].Hostname)
		}
	})

	t.Run("returns 404 for missing listener", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/listeners/detail", nil)
		c.Params = gin.Params{{Key: "id", Value: "99999"}}
		s.handleAPIGetListener(c)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d; body=%s", w.Code, w.Body.String())
		}
	})
}

func TestHandleDeleteListener(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	listener := db.Listener{
		Name:    "delete-me",
		Scheme:  "http",
		Host:    "127.0.0.1",
		Port:    9090,
		Status:  "stopped",
		Enabled: false,
	}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatalf("seed listener: %v", err)
	}

	t.Run("delete existing listener", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodDelete, "/api/listeners", nil)
		c.Params = gin.Params{{Key: "id", Value: itoa(int(listener.ID))}}
		s.handleDeleteListener(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("delete nonexistent listener", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodDelete, "/api/listeners", nil)
		c.Params = gin.Params{{Key: "id", Value: "99999"}}
		s.handleDeleteListener(c)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d; body=%s", w.Code, w.Body.String())
		}
	})
}

func TestHandleEnableDisableListener(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	listener := db.Listener{
		Name:    "toggle-me",
		Scheme:  "http",
		Host:    "127.0.0.1",
		Port:    7070,
		Status:  "stopped",
		Enabled: false,
	}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatalf("seed listener: %v", err)
	}

	enableDisable := func(t *testing.T, id uint, enable bool) int {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPost, "/api/listeners/toggle", nil)
		c.Params = gin.Params{{Key: "id", Value: itoa(int(id))}}
		if enable {
			s.handleEnableListener(c)
		} else {
			s.handleDisableListener(c)
		}
		return w.Code
	}

	if code := enableDisable(t, listener.ID, true); code != http.StatusOK {
		t.Errorf("enable: expected 200, got %d", code)
	}
	var enabled db.Listener
	s.db.First(&enabled, listener.ID)
	if !enabled.Enabled {
		t.Error("listener should be enabled after handleEnableListener")
	}

	if code := enableDisable(t, listener.ID, false); code != http.StatusOK {
		t.Errorf("disable: expected 200, got %d", code)
	}
	var disabled db.Listener
	s.db.First(&disabled, listener.ID)
	if disabled.Enabled {
		t.Error("listener should be disabled after handleDisableListener")
	}
}

func TestHandleUpdateListener(t *testing.T) {
	s := newListenerCRUDTestServer(t)

	listener := db.Listener{
		Name:    "update-me",
		Scheme:  "http",
		Host:    "127.0.0.1",
		Port:    6060,
		Status:  "running",
		Enabled: true,
	}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatalf("seed listener: %v", err)
	}

	t.Run("update name and port", func(t *testing.T) {
		body := `{"name":"updated-name","port":9090}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPut, "/api/listeners", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: itoa(int(listener.ID))}}
		s.handleUpdateListener(c)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("verify update persisted", func(t *testing.T) {
		var updated db.Listener
		s.db.First(&updated, listener.ID)
		if updated.Name != "updated-name" {
			t.Errorf("expected name=updated-name, got %q", updated.Name)
		}
		if updated.Port != 9090 {
			t.Errorf("expected port=9090, got %d", updated.Port)
		}
	})
}

func TestEnsureDefaultListener(t *testing.T) {
	s := newListenerCRUDTestServer(t)
	s.EnsureDefaultListener("127.0.0.1", 8001)
	var count int64
	s.db.Model(&db.Listener{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 seeded listener, got %d", count)
	}
	var l db.Listener
	if err := s.db.First(&l, 1).Error; err != nil {
		t.Fatalf("seeded listener missing: %v", err)
	}
	if !l.Enabled || l.Host != "127.0.0.1" || l.Port != 8001 || l.Scheme != "http" {
		t.Errorf("bad seed row: %+v", l)
	}
	if _, err := s.resolveListener(l.ID); err != nil {
		t.Errorf("seeded listener must resolve: %v", err)
	}
	// Second call is a no-op; pre-existing rows suppress seeding.
	s.EnsureDefaultListener("127.0.0.1", 8001)
	s.db.Model(&db.Listener{}).Count(&count)
	if count != 1 {
		t.Errorf("reseed must be a no-op, got %d rows", count)
	}
}
