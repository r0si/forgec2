package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func listenerCtx(t *testing.T, method, body string, uid uint, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/listeners", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uid)
	c.Params = params
	return c, w
}

// TestCreateListenerValidation covers the pre-bind refusals and the truthful
// status contract: a row is only "running" when its bind actually happened.
func TestCreateListenerValidation(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)

	// A scheme the server never binds is rejected outright.
	c, w := listenerCtx(t, http.MethodPost, `{"scheme":"wss","host":"127.0.0.1","port":9001}`, 1, nil)
	s.handleCreateListener(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wss scheme = %d %s", w.Code, w.Body.String())
	}

	// Port bounds.
	c, w = listenerCtx(t, http.MethodPost, `{"scheme":"http","host":"127.0.0.1","port":70000}`, 1, nil)
	s.handleCreateListener(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "port must be between") {
		t.Fatalf("bad port = %d %s", w.Code, w.Body.String())
	}

	// Host validation.
	c, w = listenerCtx(t, http.MethodPost, `{"scheme":"http","host":"not a host!","port":9001}`, 1, nil)
	s.handleCreateListener(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid host") {
		t.Fatalf("bad host = %d %s", w.Code, w.Body.String())
	}

	// A DNS listener takes the domain from Host so it is not born with an
	// empty DNSDomain (which would key as "dns://" and never bind).
	c, w = listenerCtx(t, http.MethodPost, `{"scheme":"dns","host":"c2.example.test"}`, 1, nil)
	s.handleCreateListener(c)
	if w.Code != http.StatusOK {
		t.Fatalf("dns create = %d %s", w.Code, w.Body.String())
	}
	var dnsListener db.Listener
	if err := s.db.Where("scheme = ?", "dns").First(&dnsListener).Error; err != nil {
		t.Fatal(err)
	}
	if dnsListener.DNSDomain != "c2.example.test" {
		t.Fatalf("dns listener domain = %q", dnsListener.DNSDomain)
	}
	if dnsListener.TenantID != 1 {
		t.Fatalf("dns listener tenant = %d", dnsListener.TenantID)
	}

	// A bind that cannot happen must leave the row "stopped", never "running".
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	busyPort := held.Addr().(*net.TCPAddr).Port
	c, w = listenerCtx(t, http.MethodPost,
		`{"scheme":"http","host":"127.0.0.1","port":`+strconv.Itoa(busyPort)+`}`, 1, nil)
	s.handleCreateListener(c)
	if w.Code != http.StatusOK {
		t.Fatalf("busy port create = %d %s", w.Code, w.Body.String())
	}
	var busy db.Listener
	if err := s.db.Where("port = ?", busyPort).First(&busy).Error; err != nil {
		t.Fatal(err)
	}
	if busy.Status != "stopped" {
		t.Fatalf("a listener whose bind failed reports status %q, want stopped", busy.Status)
	}
}

// TestListenerTenantIsolation pins the CRUD contract: creation stamps the
// caller's tenant, the list hides foreign rows, and update/delete refuse them.
func TestListenerTenantIsolation(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)
	seedAIUser(t, s, "admin2", 2)
	foreign := db.Listener{Name: "foreign", TenantID: 2, Scheme: "http", Type: "http", Host: "10.9.9.9", Port: 9101, Enabled: true, Status: "stopped"}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	// Create as tenant 1: the row carries tenant 1 even though the body tries
	// to claim tenant 2.
	c, w := listenerCtx(t, http.MethodPost,
		`{"scheme":"http","host":"127.0.0.1","port":`+strconv.Itoa(freeTCPPort(t))+`,"tenant_id":2}`, 1, nil)
	s.handleCreateListener(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var mine db.Listener
	if err := s.db.Where("port <> ? AND scheme = ? AND host = ?", 9101, "http", "127.0.0.1").First(&mine).Error; err != nil {
		t.Fatal(err)
	}
	if mine.TenantID != 1 {
		t.Fatalf("created listener tenant = %d, want the caller's tenant (the body must not set it)", mine.TenantID)
	}

	// List as tenant 1: no foreign rows.
	c, w = listenerCtx(t, http.MethodGet, "", 1, nil)
	s.handleListListeners(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "foreign") {
		t.Fatalf("list leaked another tenant's listener: %.300s", w.Body.String())
	}

	// Update and delete a foreign listener: 404 and untouched.
	c, w = listenerCtx(t, http.MethodPut, `{"name":"hijacked"}`, 1, gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}})
	s.handleUpdateListener(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign update = %d %s", w.Code, w.Body.String())
	}
	c, w = listenerCtx(t, http.MethodDelete, "", 1, gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}})
	s.handleDeleteListener(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d %s", w.Code, w.Body.String())
	}
	var still db.Listener
	if err := s.db.Where("id = ?", foreign.ID).First(&still).Error; err != nil {
		t.Fatal("foreign listener was deleted")
	}
	if still.Name != "foreign" {
		t.Fatalf("foreign listener renamed to %q", still.Name)
	}
}

// TestUpdateListenerNormalizes checks that an update re-derives the protocol
// fields and rejects a scheme the server cannot bind.
func TestUpdateListenerNormalizes(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)
	l := db.Listener{Name: "l1", TenantID: 1, Scheme: "http", Type: "http", Host: "127.0.0.1", Port: freeTCPPort(t), Enabled: true, Status: "stopped"}
	if err := s.db.Create(&l).Error; err != nil {
		t.Fatal(err)
	}

	c, w := listenerCtx(t, http.MethodPut, `{"scheme":"nope"}`, 1, gin.Params{{Key: "id", Value: jsonNumber(l.ID)}})
	s.handleUpdateListener(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad scheme update = %d %s", w.Code, w.Body.String())
	}

	c, w = listenerCtx(t, http.MethodPut, `{"name":"renamed","scheme":"https"}`, 1, gin.Params{{Key: "id", Value: jsonNumber(l.ID)}})
	s.handleUpdateListener(c)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.Listener
	if err := s.db.Where("id = ?", l.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != "renamed" || reloaded.Scheme != "https" || reloaded.Protocol != "https" || reloaded.Type != "http" {
		t.Fatalf("updated listener = %+v", reloaded)
	}
}
