package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// socksFormCtx builds a form-encoded POST (the relay handlers read c.PostForm).
func socksFormCtx(port string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/x/socks/start", strings.NewReader("port="+port))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("tenant_id", uint(1))
	c.Params = params
	return c, w
}
func TestSocksRelayStartStatusStop(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "socks-agent", 1)

	// Unknown agent and invalid ports are refused before any listener work.
	c, w := missingAPICtx(http.MethodPost, "/api/agents/x/socks/start", "",
		gin.Params{{Key: "id", Value: "ghost"}})
	s.handleStartSocksRelay(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}
	for _, port := range []string{"abc", "0", "70000"} {
		// The port comes from the POST form body (c.PostForm), not a query arg.
		c, w = socksFormCtx(port, gin.Params{{Key: "id", Value: "socks-agent"}})
		s.handleStartSocksRelay(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid port") {
			t.Fatalf("port %q = %d %s", port, w.Code, w.Body.String())
		}
	}

	// Status without a session is a clean "not active".
	c, w = missingAPICtx(http.MethodGet, "/api/agents/x/socks/status", "",
		gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleSocksRelayStatus(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatalf("inactive status = %d %s", w.Code, w.Body.String())
	}

	// Start a relay on a free port.
	port := freeTCPPort(t)
	c, w = socksFormCtx(strconv.Itoa(port), gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleStartSocksRelay(c)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d %s", w.Code, w.Body.String())
	}
	var started struct {
		Port    int    `json:"port"`
		Message string `json:"message"`
	}
	unmarshalData(t, w.Body.Bytes(), &started)
	if started.Port != port {
		t.Fatalf("started port = %d, want %d", started.Port, port)
	}

	// Status now reports the live relay.
	c, w = missingAPICtx(http.MethodGet, "/api/agents/x/socks/status", "",
		gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleSocksRelayStatus(c)
	if !strings.Contains(w.Body.String(), `"active":true`) || !strings.Contains(w.Body.String(), strconv.Itoa(port)) {
		t.Fatalf("active status = %d %s", w.Code, w.Body.String())
	}

	// The historical session appears in the list for this agent.
	var sessions int64
	s.db.Model(&db.SocksSession{}).Where("agent_id = ?", "socks-agent").Count(&sessions)
	if sessions != 1 {
		t.Fatalf("persisted sessions = %d, want 1", sessions)
	}
	c, w = missingAPICtx(http.MethodGet, "/api/socks/sessions", "", nil)
	s.handleGetSocksSessions(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "socks-agent") {
		t.Fatalf("sessions list = %d %s", w.Code, w.Body.String())
	}

	// A second start on the same agent replaces/refuses cleanly rather than
	// leaking a second listener; either way the engine stays consistent.
	c, w = socksFormCtx(strconv.Itoa(freeTCPPort(t)), gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleStartSocksRelay(c)
	if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
		t.Fatalf("second start = %d %s", w.Code, w.Body.String())
	}

	// Stop tears it down; a second stop reports that there is none.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/socks/stop", "",
		gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleStopSocksRelay(c)
	if w.Code != http.StatusOK {
		t.Fatalf("stop = %d %s", w.Code, w.Body.String())
	}
	c, w = missingAPICtx(http.MethodGet, "/api/agents/x/socks/status", "",
		gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleSocksRelayStatus(c)
	if !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatalf("status after stop = %s", w.Body.String())
	}
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/socks/stop", "",
		gin.Params{{Key: "id", Value: "socks-agent"}})
	s.handleStopSocksRelay(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("second stop = %d %s", w.Code, w.Body.String())
	}
}

func TestSocksRelayTenantScopedSessionList(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "own-agent", 1)
	seedTenantAgent(t, s, "foreign-agent", 2)
	s.db.Create(&db.SocksSession{AgentID: "own-agent", ListenPort: 1080, Status: "stopped"})
	s.db.Create(&db.SocksSession{AgentID: "foreign-agent", ListenPort: 1081, Status: "stopped"})

	// The list is scoped through the owning agent, so the foreign session is
	// invisible even though SocksSession has no tenant column.
	c, w := missingAPICtx(http.MethodGet, "/api/socks/sessions", "", nil)
	s.handleGetSocksSessions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("sessions = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "foreign-agent") {
		t.Fatalf("cross-tenant session leaked: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "own-agent") {
		t.Fatalf("own session missing: %s", w.Body.String())
	}

	// Filtering by an agent id you cannot see narrows to nothing rather than
	// widening the result set.
	c, w = missingAPICtx(http.MethodGet, "/api/socks/sessions?agent_id=foreign-agent", "", nil)
	s.handleGetSocksSessions(c)
	if strings.Contains(w.Body.String(), "foreign-agent") {
		t.Fatalf("agent_id filter bypassed the tenant scope: %s", w.Body.String())
	}
}
