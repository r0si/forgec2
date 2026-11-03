package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func newTopologyTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return &Server{db: newContractDB(t), cfg: config.DefaultConfig()}
}

// Regression: the topology query previously selected a non-existent "user"
// column (model field is "username"), which made every request fail and the
// graph render empty.
func TestHandleTopologyData_IncludesAgents(t *testing.T) {
	s := newTopologyTestServer(t)

	agents := []db.Implant{
		{ID: "agent-topo-1", Hostname: "workstation-1", Username: "corp\\alice", OS: "windows", IP: "10.0.0.5", Arch: "x64"},
		{ID: "agent-topo-2", Hostname: "linux-server", Username: "root", OS: "linux", IP: "10.0.0.6", Arch: "x64"},
	}
	if err := s.db.Create(&agents).Error; err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/topology/data", nil)

	s.handleTopologyData(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	agentNodes := 0
	for _, n := range resp.Nodes {
		if id, _ := n["id"].(string); len(id) > 6 && id[:6] == "agent-" {
			agentNodes++
		}
	}
	if agentNodes != 2 {
		t.Fatalf("expected 2 agent nodes, got %d (nodes=%d); body=%s", agentNodes, len(resp.Nodes), w.Body.String())
	}
}

func TestHandleTopologyData_TenantIsolation(t *testing.T) {
	s := newTopologyTestServer(t)
	listeners := []db.Listener{
		{Name: "own-listener", TenantID: 1, Scheme: "http", Host: "10.0.0.1", Port: 8080, Enabled: true},
		{Name: "foreign-listener", TenantID: 2, Scheme: "https", Host: "10.9.9.9", Port: 8443, Enabled: true},
	}
	if err := s.db.Create(&listeners).Error; err != nil {
		t.Fatalf("seed listeners: %v", err)
	}
	agents := []db.Implant{
		{ID: "own-agent", TenantID: 1, Hostname: "own-host", ListenerID: listeners[0].ID},
		{ID: "foreign-agent", TenantID: 2, Hostname: "foreign-host", ListenerID: listeners[1].ID},
	}
	if err := s.db.Create(&agents).Error; err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/topology/data", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleTopologyData(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"own-listener", "own-host"} {
		if !strings.Contains(body, want) {
			t.Fatalf("topology lost %q: %s", want, body)
		}
	}
	for _, leak := range []string{"foreign-listener", "10.9.9.9", "foreign-host", "foreign-agent"} {
		if strings.Contains(body, leak) {
			t.Fatalf("topology leaked %q: %s", leak, body)
		}
	}
}

func TestHandleContainerAgents_MatchesHints(t *testing.T) {
	s := newTopologyTestServer(t)

	agents := []db.Implant{
		{ID: "agent-ctr-1", Hostname: "k8s-worker", OS: "linux container", IP: "10.0.0.7"},
		{ID: "agent-ctr-2", Hostname: "docker-host", Notes: "docker environment", OS: "linux", IP: "10.0.0.8"},
		{ID: "agent-ctr-3", Hostname: "plain", OS: "windows", IP: "10.0.0.9"},
	}
	if err := s.db.Create(&agents).Error; err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/container/agents", nil)

	s.handleContainerAgents(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Count  int          `json:"count"`
		Agents []db.Implant `json:"agents"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Count != 2 {
		t.Fatalf("expected 2 container agents, got %d; body=%s", resp.Count, w.Body.String())
	}
	ids := map[string]bool{}
	for _, a := range resp.Agents {
		ids[a.ID] = true
	}
	if !ids["agent-ctr-1"] || !ids["agent-ctr-2"] || ids["agent-ctr-3"] {
		t.Fatalf("unexpected agent set: %v", ids)
	}
}
