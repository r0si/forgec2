package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// topologyNetNode is one graph node in the auto-discovered network view.
type topologyNetNode struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Group        string `json:"group"` // agent-online | agent-offline | host-lateral | host-discovered
	Title        string `json:"title,omitempty"`
	IP           string `json:"ip,omitempty"`
	OS           string `json:"os,omitempty"`
	Status       string `json:"status,omitempty"`
	P2PMode      string `json:"p2p_mode,omitempty"`
	PeerCount    int    `json:"peer_count,omitempty"`
	ServiceCount int    `json:"service_count,omitempty"`
}

// topologyNetEdge is one directed relation; kind drives frontend styling.
type topologyNetEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // p2p | proxy | discovered
}

// topologyServiceCount parses the Services JSON column defensively and
// returns how many entries it holds.
func topologyServiceCount(servicesJSON string) int {
	raw := strings.TrimSpace(servicesJSON)
	if raw == "" || raw == "null" {
		return 0
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return 0
	}
	return len(entries)
}

// topologyLateralTouched reports whether a NetworkHost row's Services payload
// carries a "method" key — the marker written by handleProcessLateralResult,
// distinguishing "touched via lateral movement" from plain discovery.
func topologyLateralTouched(servicesJSON string) bool {
	raw := strings.TrimSpace(servicesJSON)
	if raw == "" || raw == "null" {
		return false
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return false
	}
	for _, e := range entries {
		if _, ok := e["method"]; ok {
			return true
		}
	}
	return false
}

// handleAPINetworkTopology merges controlled agents, their p2p/proxy chains
// and auto-discovered hosts (lateral-movement targets, recon findings) into
// one graph consumable by the vendored vis-network viewer.
// GET /api/topology/network
func (s *Server) handleAPINetworkTopology(c *gin.Context) {
	var agents []db.Implant
	// NOTE: P2PMode's physical column is "p2p_mode" (explicit column tag;
	// pre-tag servers used GORM's derived "p2_p_mode"). Select it bare — an
	// alias cannot be matched back to the struct field by GORM's scan mapper.
	if err := s.db.
		Select("id, hostname, ip, os, status, p2p_mode, peer_count, parent_id, parent_agent_id").
		Order("last_seen desc").Limit(300).Find(&agents).Error; err != nil {
		slog.Error("topology/net: agent query failed", "err", err)
	}
	var hosts []db.NetworkHost
	if err := s.db.Order("last_seen desc").Limit(500).Find(&hosts).Error; err != nil {
		slog.Error("topology/net: host query failed", "err", err)
	}

	agentByID := make(map[string]db.Implant, len(agents))
	agentIPSet := make(map[string]string, len(agents)) // ip -> agent id
	for _, a := range agents {
		agentByID[a.ID] = a
		if ip := strings.TrimSpace(a.IP); ip != "" {
			agentIPSet[ip] = a.ID
		}
	}

	labelFor := func(a db.Implant) string {
		if h := strings.TrimSpace(a.Hostname); h != "" {
			return h
		}
		id := a.ID
		if len(id) > 8 {
			id = id[:8]
		}
		return id
	}

	nodes := make([]topologyNetNode, 0, len(agents)+len(hosts))
	for _, a := range agents {
		group := "agent-offline"
		if strings.EqualFold(a.Status, "online") {
			group = "agent-online"
		}
		nodes = append(nodes, topologyNetNode{
			ID:        a.ID,
			Label:     labelFor(a),
			Group:     group,
			Title:     truncateStr(a.IP+" · "+a.OS+" · "+a.Status, 160),
			IP:        a.IP,
			OS:        a.OS,
			Status:    a.Status,
			P2PMode:   a.P2PMode,
			PeerCount: a.PeerCount,
		})
	}

	edges := make([]topologyNetEdge, 0, len(agents)+len(hosts))
	edgeSeen := make(map[string]bool, len(agents)*2)
	addEdge := func(from, to, kind string) {
		key := kind + "|" + from + "->" + to
		if from == "" || to == "" || from == to || edgeSeen[key] {
			return
		}
		edgeSeen[key] = true
		edges = append(edges, topologyNetEdge{From: from, To: to, Kind: kind})
	}

	mergedIntoAgent := 0
	hostSeenIP := make(map[string]bool, len(hosts))
	lateralCount, discoveredCount := 0, 0
	for _, h := range hosts {
		ip := strings.TrimSpace(h.IP)
		if ip == "" || hostSeenIP[ip] {
			continue // dedupe repeated discoveries of the same address
		}
		hostSeenIP[ip] = true

		// A discovered IP belonging to an existing implant is the same
		// machine — fold its intel into the agent node instead of drawing
		// a confusing duplicate.
		if aid, ok := agentIPSet[ip]; ok {
			mergedIntoAgent++
			extra := "lateral"
			if !topologyLateralTouched(h.Services) {
				extra = "recon"
			}
			for i := range nodes {
				if nodes[i].ID == aid {
					nodes[i].Title = truncateStr(nodes[i].Title+fmt.Sprintf("\n+%s: %s", extra, hostnameOr(h.Hostname)), 240)
					break
				}
			}
			continue
		}

		group := "host-discovered"
		if topologyLateralTouched(h.Services) {
			group = "host-lateral"
			lateralCount++
		} else {
			discoveredCount++
		}
		nodeID := "host-" + ip
		svc := topologyServiceCount(h.Services)
		title := h.Hostname
		if title == "" {
			title = ip
		}
		title += "\n" + h.OS
		if svc > 0 {
			title = fmt.Sprintf("%s (%d svc)", title, svc)
		}
		nodes = append(nodes, topologyNetNode{
			ID:           nodeID,
			Label:        ip,
			Group:        group,
			Title:        truncateStr(title, 200),
			IP:           ip,
			OS:           h.OS,
			ServiceCount: svc,
		})
		// Discovery provenance edge: which agent found/touched this host.
		if _, ok := agentByID[h.AgentID]; ok {
			addEdge(h.AgentID, nodeID, "discovered")
		}
	}

	// P2P beacon chaining (ParentID) and multi-hop proxy chain
	// (ParentAgentID), drawn only between known endpoints.
	for _, a := range agents {
		if a.ParentID != "" {
			if _, ok := agentByID[a.ParentID]; ok {
				addEdge(a.ParentID, a.ID, "p2p")
			}
		}
		if a.ParentAgentID != "" {
			if _, ok := agentByID[a.ParentAgentID]; ok {
				addEdge(a.ParentAgentID, a.ID, "proxy")
			}
		}
	}

	online := 0
	for _, n := range nodes {
		if n.Group == "agent-online" {
			online++
		}
	}
	respond(c, gin.H{

		"nodes": nodes,
		"edges": edges,
		"stats": gin.H{
			"agents":            len(agents),
			"online":            online,
			"hosts":             lateralCount + discoveredCount + mergedIntoAgent,
			"hosts_lateral":     lateralCount,
			"hosts_discovered":  discoveredCount,
			"merged_into_agent": mergedIntoAgent,
		},
	})
}

func hostnameOr(h string) string {
	if s := strings.TrimSpace(h); s != "" {
		return s
	}
	return "?"
}
