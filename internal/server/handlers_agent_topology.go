package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handlePivoting shows SOCKS / proxy status and agents useful for pivoting
func (s *Server) handlePivoting(c *gin.Context) {
	var recentAgents []db.Implant
	q := s.db.Select("id", "hostname", "ip", "os", "arch", "last_seen").
		Where("last_seen > ?", time.Now().Add(-30*time.Minute))
	q = s.tenantScope(q, c)
	if err := q.Limit(30).Find(&recentAgents).Error; err != nil {
		slog.Error("Failed to query recent agents for topology", "err", err)
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - Tunnels & Proxies (Pivoting)",
		"ActiveNav": "pivoting",
		"Agents":    recentAgents,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// handleTopologyPage renders the network topology visualization
func (s *Server) handleTopologyPage(c *gin.Context) {
	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - Network Topology",
		"ActiveNav": "topology",
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// handleTopologyData returns JSON nodes and edges for the topology graph
func (s *Server) handleTopologyData(c *gin.Context) {
	var listeners []db.Listener
	if err := s.db.Where("enabled = ?", true).Limit(500).Find(&listeners).Error; err != nil {
		slog.Error("Failed to query listeners for topology", "err", err)
	}

	var agents []db.Implant
	agentQ := s.db.Select("id, hostname, os, ip, username, status, last_seen, listener_id, parent_id").Order("last_seen desc")
	agentQ = s.tenantScope(agentQ, c)
	if err := agentQ.Limit(TopologyAgentLimit).Find(&agents).Error; err != nil {
		slog.Error("Failed to query agents for topology", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to query agents for topology")
		return
	}

	onlineCutoff := time.Now().Add(-s.offlineThreshold())

	nodes := make([]map[string]interface{}, 0)
	edges := make([]map[string]interface{}, 0)

	// Listener nodes
	for _, l := range listeners {
		label := l.Name
		if label == "" {
			label = fmt.Sprintf("%s:%d", l.Host, l.Port)
		}
		nodes = append(nodes, map[string]interface{}{
			"id":    fmt.Sprintf("listener-%d", l.ID),
			"label": label,
			"title": fmt.Sprintf("Listener: %s://%s:%d", l.Scheme, l.Host, l.Port),
			"group": "listener",
		})
	}

	// Agent nodes + listener→agent edges
	for _, a := range agents {
		online := a.LastSeen.After(onlineCutoff)
		label := a.Hostname
		if label == "" {
			label = a.ID[:8]
		}
		group := "agent-offline"
		if online {
			group = "agent-online"
		}
		title := fmt.Sprintf("Agent: %s<br>User: %s<br>IP: %s<br>OS: %s<br>Last: %s",
			a.Hostname, a.Username, a.IP, a.OS, a.LastSeen.Format("15:04:05"))
		nodes = append(nodes, map[string]interface{}{
			"id":    fmt.Sprintf("agent-%s", a.ID),
			"label": label,
			"title": title,
			"group": group,
		})

		// Edge from listener to agent
		if a.ListenerID > 0 {
			edges = append(edges, map[string]interface{}{
				"from": fmt.Sprintf("listener-%d", a.ListenerID),
				"to":   fmt.Sprintf("agent-%s", a.ID),
			})
		}

		// P2P edge: parent→child
		if a.ParentID != "" {
			edges = append(edges, map[string]interface{}{
				"from":   fmt.Sprintf("agent-%s", a.ParentID),
				"to":     fmt.Sprintf("agent-%s", a.ID),
				"dashes": true,
				"color":  map[string]interface{}{"color": "#f59e0b"},
				"title":  fmt.Sprintf("P2P: %s", a.P2PMode),
				"width":  2,
				"length": 200,
			})
		}
	}

	respond(c, gin.H{"nodes": nodes, "edges": edges})
}

// handleLinkAgent links a child agent to a parent agent for P2P relay
func (s *Server) handleLinkAgent(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	parentID := c.Param("id")
	childID := c.PostForm("child_id")
	mode := c.PostForm("p2p_mode") // "smb" or "tcp"
	listenAddr := c.PostForm("p2p_listen_addr")

	if childID == "" {
		respondError(c, http.StatusBadRequest, "child_id is required")
		return
	}

	var parent, child db.Implant
	pq := s.db.Where("id = ?", parentID)
	pq = s.tenantScope(pq, c)
	if err := pq.First(&parent).Error; err != nil {
		respondError(c, http.StatusNotFound, "parent agent not found")
		return
	}
	cq := s.db.Where("id = ?", childID)
	cq = s.tenantScope(cq, c)
	if err := cq.First(&child).Error; err != nil {
		respondError(c, http.StatusNotFound, "child agent not found")
		return
	}

	// Update child's ParentID
	s.db.Model(&child).Updates(map[string]interface{}{
		"parent_id":       parentID,
		"p2p_mode":        mode,
		"p2p_listen_addr": listenAddr,
	})
	slog.Info("P2P link created", "parent", parentID, "child", childID, "mode", mode)
	s.LogAuditRecord(c, "link_agent", "agent", childID, fmt.Sprintf("linked to parent %s (mode=%s)", parentID, mode), true, nil)
	c.Redirect(http.StatusSeeOther, "/agents/"+parentID)
}

// handleUnlinkAgent removes the P2P parent link from a child agent
func (s *Server) handleUnlinkAgent(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	childID := c.Param("id")

	var child db.Implant
	q := s.db.Where("id = ?", childID)
	q = s.tenantScope(q, c)
	if err := q.First(&child).Error; err != nil {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}

	parentID := child.ParentID
	s.db.Model(&child).Updates(map[string]interface{}{
		"parent_id":       "",
		"p2p_mode":        "",
		"p2p_listen_addr": "",
	})
	slog.Info("P2P link removed", "parent", parentID, "child", childID)
	s.LogAuditRecord(c, "unlink_agent", "agent", childID, fmt.Sprintf("unlinked from parent %s", parentID), true, nil)
	c.Redirect(http.StatusSeeOther, "/agents/"+childID)
}
