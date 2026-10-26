package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
)

func TestAPISettingsStats(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	now := time.Now()
	// Two agents: one seen just now, one well past the offline threshold.
	if err := s.db.Create(&db.Implant{ID: "stats-online", TenantID: 1, Hostname: "on", Status: "online",
		LastSeen: now, ProtocolVersion: protocol.CurrentProtocolVersion}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "stats-stale", TenantID: 1, Hostname: "off", Status: "offline",
		LastSeen: now.Add(-6 * time.Hour), ProtocolVersion: protocol.CurrentProtocolVersion}).Error; err != nil {
		t.Fatal(err)
	}
	// An agent that never checks in because its protocol is too old.
	if err := s.db.Create(&db.Implant{ID: "stats-old-proto", TenantID: 1, Hostname: "old",
		Status: "offline", ProtocolVersion: protocol.CurrentProtocolVersion + 9}).Error; err != nil {
		t.Fatal(err)
	}
	s.db.Create(&db.Listener{Name: "stats-http", Scheme: "http", Host: "0.0.0.0", Port: 8080})
	s.db.Create(&db.CredentialEntry{AgentID: "stats-online", TenantID: 1, Username: "alice"})
	s.db.Create(&db.TokenEntry{AgentID: "stats-online", Username: "svc"})
	s.db.Create(&db.AuditLog{User: "admin", Action: "login", Success: true})

	s.cfg.Server.Port = 8080
	s.cfg.Server.OfflineThreshold = 300
	s.cfg.Implant.DefaultInterval = 15
	s.cfg.Implant.DefaultJitter = 20
	s.startTime = time.Now().Add(-time.Minute)

	c, w := missingAPICtx(http.MethodGet, "/api/settings", "", nil)
	s.handleAPISettings(c)
	if w.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Server struct {
			Port                   int    `json:"port"`
			OfflineThreshold       int    `json:"offline_threshold"`
			TotalAgents            int64  `json:"total_agents"`
			OnlineAgents           int64  `json:"online_agents"`
			TotalListeners         int64  `json:"total_listeners"`
			TotalCredentials       int64  `json:"total_credentials"`
			TotalTokens            int64  `json:"total_tokens"`
			TotalAudits            int64  `json:"total_audits"`
			ProtocolOutdatedAgents int64  `json:"protocol_outdated_agents"`
			DatabasePath           string `json:"database_path"`
			DataDir                string `json:"data_dir"`
			Uptime                 string `json:"uptime"`
			GoVersion              string `json:"go_version"`
		} `json:"server"`
		Agent struct {
			DefaultInterval int `json:"default_interval"`
			DefaultJitter   int `json:"default_jitter"`
		} `json:"agent"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Server.TotalAgents != 3 || out.Server.TotalListeners != 1 ||
		out.Server.TotalCredentials != 1 || out.Server.TotalTokens != 1 || out.Server.TotalAudits != 1 {
		t.Fatalf("counts = %+v", out.Server)
	}
	// Only the agent seen just now is inside the 300s threshold; the stale one
	// is 6h old and the protocol-outdated one never checked in at all.
	if out.Server.OnlineAgents != 1 {
		t.Fatalf("online_agents = %d, want 1", out.Server.OnlineAgents)
	}
	if out.Server.ProtocolOutdatedAgents != 1 {
		t.Fatalf("protocol_outdated_agents = %d, want 1", out.Server.ProtocolOutdatedAgents)
	}
	// Paths must never leak through the stats endpoint.
	if out.Server.DatabasePath != "***" || out.Server.DataDir != "***" {
		t.Fatalf("paths exposed: %q / %q", out.Server.DatabasePath, out.Server.DataDir)
	}
	if out.Agent.DefaultInterval != 15 || out.Agent.DefaultJitter != 20 {
		t.Fatalf("agent defaults = %+v", out.Agent)
	}
	if out.Server.Uptime == "" || out.Server.GoVersion == "" {
		t.Fatalf("missing runtime info: %+v", out.Server)
	}
}

func TestAPIMeshTopology(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	now := time.Now()
	for _, p := range []db.MeshPeer{
		{AgentID: "mesh-a", PeerID: "mesh-b", PeerAddr: "10.0.0.2:4444", Latency: 12, LastSeen: now},
		{AgentID: "mesh-b", PeerID: "mesh-c", PeerAddr: "10.0.0.3:4444", Latency: 30, LastSeen: now},
	} {
		if err := s.db.Create(&p).Error; err != nil {
			t.Fatalf("seed mesh peer: %v", err)
		}
	}

	c, w := missingAPICtx(http.MethodGet, "/api/mesh/topology", "", nil)
	s.handleAPIMeshTopology(c)
	if w.Code != http.StatusOK {
		t.Fatalf("mesh topology = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Nodes []struct {
			ID        string `json:"id"`
			PeerCount int    `json:"peer_count"`
			P2PMode   string `json:"p2p_mode"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if len(out.Nodes) != 3 {
		t.Fatalf("nodes = %+v, want the 3 distinct agents", out.Nodes)
	}
	if len(out.Edges) != 2 {
		t.Fatalf("edges = %+v, want 2", out.Edges)
	}
	seen := map[string]bool{}
	for _, n := range out.Nodes {
		seen[n.ID] = true
		if n.P2PMode != "relay" {
			t.Fatalf("node mode = %q", n.P2PMode)
		}
	}
	for _, id := range []string{"mesh-a", "mesh-b", "mesh-c"} {
		if !seen[id] {
			t.Fatalf("node %q missing from %+v", id, out.Nodes)
		}
	}
	pairs := map[string]bool{}
	for _, e := range out.Edges {
		pairs[e.From+"->"+e.To] = true
	}
	if !pairs["mesh-a->mesh-b"] || !pairs["mesh-b->mesh-c"] {
		t.Fatalf("edges = %+v", out.Edges)
	}
}

func TestAPITranslationsStats(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	c, w := missingAPICtx(http.MethodGet, "/api/translations/stats", "", nil)
	s.handleAPITranslationsStats(c)
	if w.Code != http.StatusOK {
		t.Fatalf("translations stats = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Stats     map[string]any      `json:"stats"`
		TotalKeys int                 `json:"total_keys"`
		Missing   map[string][]string `json:"missing"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.TotalKeys == 0 || len(out.Stats) == 0 {
		t.Fatalf("translation stats empty: keys=%d stats=%+v", out.TotalKeys, out.Stats)
	}
	if len(out.Missing) == 0 {
		t.Fatal("no per-language missing map")
	}
	for lang, keys := range out.Missing {
		if strings.TrimSpace(lang) == "" {
			t.Fatal("empty language key in missing map")
		}
		_ = keys
	}
}

var _ = gin.Params{}
