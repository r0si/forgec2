package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupNetTopoTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(&db.Implant{}, &db.NetworkHost{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := &Server{db: database, ctx: context.Background()}
	r := gin.New()
	r.GET("/api/topology/network", s.handleAPINetworkTopology)
	return r, database
}

func TestHandleAPINetworkTopology_GraphComposition(t *testing.T) {
	r, database := setupNetTopoTest(t)

	// Two chained agents (p2p) + one proxy-chain agent.
	database.Create(&db.Implant{ID: "ag-parent", Hostname: "DC01", IP: "10.0.0.10", OS: "Windows Server 2022", Status: "online"})
	database.Create(&db.Implant{ID: "ag-child", Hostname: "WK01", IP: "10.0.0.20", OS: "Windows 11", Status: "offline", ParentID: "ag-parent", P2PMode: "smb"})
	database.Create(&db.Implant{ID: "ag-proxychild", Hostname: "WK02", IP: "10.0.0.30", Status: "online", ParentAgentID: "ag-parent"})

	// Lateral-touched host (Services carries "method") + plain discovery +
	// duplicate row for the same IP (must dedupe) + agent's own IP (must merge).
	database.Create(&db.NetworkHost{AgentID: "ag-parent", IP: "10.0.0.99", Hostname: "FILESVR", OS: "Windows", Services: `[{"method":"wmi","port":0}]`})
	database.Create(&db.NetworkHost{AgentID: "ag-parent", IP: "10.0.1.55", Services: `[]`})
	database.Create(&db.NetworkHost{AgentID: "ag-parent", IP: "10.0.1.55", Services: `[]`}) // dup
	database.Create(&db.NetworkHost{AgentID: "ag-parent", IP: "10.0.0.10", Hostname: "DC01", Services: `[]`})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/topology/network", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Nodes []topologyNetNode `json:"nodes"`
		Edges []topologyNetEdge `json:"edges"`
		Stats map[string]int    `json:"stats"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)

	nodeByID := map[string]topologyNetNode{}
	for _, n := range resp.Nodes {
		nodeByID[n.ID] = n
	}

	// Nodes: 3 agents + 2 unique external hosts; the agent-owned IP merged away.
	if len(resp.Nodes) != 5 {
		t.Fatalf("expected 5 nodes, got %d: %+v", len(resp.Nodes), nodeIDsOf(resp.Nodes))
	}
	if _, ok := nodeByID["host-10.0.1.55"]; !ok {
		t.Fatal("plain discovered host missing")
	}
	if _, ok := nodeByID["host-10.0.0.99"]; !ok {
		t.Fatal("lateral host missing")
	}
	if _, ok := nodeByID["host-10.0.0.10"]; ok {
		t.Fatal("agent-owned IP must be merged into the agent node, not duplicated")
	}
	if got := nodeByID["host-10.0.0.99"].Group; got != "host-lateral" {
		t.Fatalf("method-keyed services should mark host-lateral, got %q", got)
	}
	if got := nodeByID["host-10.0.1.55"].Group; got != "host-discovered" {
		t.Fatalf("plain host should be host-discovered, got %q", got)
	}
	// Regression guard: the p2p_mode column must populate P2PMode.
	if got := nodeByID["ag-child"].P2PMode; got != "smb" {
		t.Fatalf("expected child p2p_mode=smb, got %q", got)
	}

	// Edges: p2p parent->child, proxy chain, and one discovered edge per
	// external host from its discovering agent.
	kinds := map[string]int{}
	for _, e := range resp.Edges {
		kinds[e.Kind+"|"+e.From+"->"+e.To]++
	}
	if kinds["p2p|ag-parent->ag-child"] != 1 {
		t.Fatalf("missing p2p edge, edges=%+v", resp.Edges)
	}
	if kinds["proxy|ag-parent->ag-proxychild"] != 1 {
		t.Fatalf("missing proxy edge, edges=%+v", resp.Edges)
	}
	if kinds["discovered|ag-parent->host-10.0.0.99"] != 1 || kinds["discovered|ag-parent->host-10.0.1.55"] != 1 {
		t.Fatalf("missing discovered edges, edges=%+v", resp.Edges)
	}
	if len(resp.Edges) != 4 {
		t.Fatalf("unexpected extra edges: %+v", resp.Edges)
	}

	if resp.Stats["hosts_lateral"] != 1 || resp.Stats["hosts_discovered"] != 1 || resp.Stats["merged_into_agent"] != 1 {
		t.Fatalf("unexpected stats: %+v", resp.Stats)
	}
}

func nodeIDsOf(nodes []topologyNetNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

func TestTopologyServiceHelpers(t *testing.T) {
	if topologyServiceCount("") != 0 || topologyServiceCount(`[]`) != 0 {
		t.Fatal("empty services should count 0")
	}
	if topologyServiceCount(`[{"port":445,"service":"smb"},{"port":80}]`) != 2 {
		t.Fatal("service count mismatch")
	}
	if !topologyLateralTouched(`[{"method":"psexec","port":0}]`) {
		t.Fatal("method key should indicate lateral touch")
	}
	if topologyLateralTouched(`[{"port":445}]`) {
		t.Fatal("no method key must not be lateral")
	}
	if topologyLateralTouched("not-json") {
		t.Fatal("malformed JSON must not panic nor be lateral")
	}
}
