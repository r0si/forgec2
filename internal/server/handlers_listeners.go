package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// broadcastListenerUpdate notifies operators that the listener registry
// changed (created/updated/deleted/enabled/disabled) so open Listener tables
// can refresh without polling.
func (s *Server) broadcastListenerUpdate(action string, l *db.Listener) {
	// A listener endpoint/enabled change alters the network config delivered
	// to every agent bound to it; refresh it on their next beacon instead of
	// waiting for the periodic window.
	s.markNetworkConfigChanged()
	payload := map[string]interface{}{
		"type":          "listener_update",
		"action":        action,
		"listener_id":   strconv.FormatUint(uint64(l.ID), 10),
		"name":          l.Name,
		"listener_type": l.Type,
		"scheme":        l.Scheme,
		"host":          l.Host,
		"port":          l.Port,
		"enabled":       l.Enabled,
	}
	s.broadcastOperatorEvent(payload)
}

func (s *Server) handleListListeners(c *gin.Context) {
	p := parsePagination(c, 20, 100)

	query := s.tenantScope(s.db.Model(&db.Listener{}), c)

	if tag := c.Query("tag"); tag != "" {
		query = query.Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(tag)+"%")
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to count listeners")
		return
	}

	var listeners []db.Listener
	if err := query.Order("created_at desc").Offset(p.Offset).Limit(p.PageSize).Find(&listeners).Error; err != nil {
		slog.Error("Failed to list listeners", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to list listeners")
		return
	}
	respond(c, gin.H{
		"listeners": listeners,
		"total":     total,
		"page":      p.Page,
		"page_size": p.PageSize,
	})
}

func (s *Server) handleListenerDetail(c *gin.Context) {
	id := c.Param("id")
	listener, ok := s.loadListenerScoped(c, id)
	if !ok {
		c.String(http.StatusNotFound, "Listener not found")
		return
	}

	// Tenant gate: listeners are global, but the attached agents (hostnames,
	// IPs, users) are tenant assets — only expose caller-visible ones.
	agents := make([]db.Implant, 0)
	if err := s.tenantScope(s.db, c).Where("listener_id = ?", listener.ID).Order("last_seen desc").Limit(5000).Find(&agents).Error; err != nil {
		slog.Error("Failed to query listener agents", "err", err)
	}

	var activeCount int64
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("listener_id = ? AND last_seen > ?", listener.ID, time.Now().Add(-ListenerActiveThreshold)).Count(&activeCount).Error; err != nil {
		slog.Error("Failed to count active agents", "err", err)
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":        fmt.Sprintf("ForgeC2 - Listener %s", listener.Name),
		"ActiveNav":    "listeners",
		"Listener":     listener,
		"Agents":       agents,
		"TotalAgents":  len(agents),
		"ActiveAgents": activeCount,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// handleAPIGetListener returns listener detail JSON.
// GET /api/listeners/:id
func (s *Server) handleAPIGetListener(c *gin.Context) {
	id := c.Param("id")
	listener, ok := s.loadListenerScoped(c, id)
	if !ok {
		respondError(c, http.StatusNotFound, "listener not found")
		return
	}

	// Tenant gate: see handleListenerDetail — listeners are global, agents are not.
	agents := make([]db.Implant, 0)
	if err := s.tenantScope(s.db, c).Where("listener_id = ?", listener.ID).Order("last_seen desc").Limit(5000).Find(&agents).Error; err != nil {
		slog.Error("Failed to query listener agents", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to query listener agents")
		return
	}

	var activeCount int64
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("listener_id = ? AND last_seen > ?", listener.ID, time.Now().Add(-ListenerActiveThreshold)).Count(&activeCount).Error; err != nil {
		slog.Error("Failed to count active agents", "err", err)
	}

	respond(c, gin.H{
		"listener": listener,
		"agents":   agents,
		"total":    len(agents),
		"active":   activeCount,
	})
}

// listenerKey returns the key used to identify this listener in the extraListeners map.
func listenerKey(l *db.Listener) string {
	scheme := l.Scheme
	if scheme == "" {
		scheme = l.Type
	}
	switch scheme {
	case "dns":
		return "dns://" + l.DNSDomain
	case "icmp":
		return "icmp://" + l.ICMPAddr
	default:
		return scheme + "://" + l.Host + ":" + itoa(l.Port)
	}
}

// startListenerForRecord creates a real network listener for a DB listener record.
// Handles HTTP/HTTPS/TCP/TLS/DNS/ICMP schemes, port conflict detection, and main server port skip.
// It reports whether the listener is actually being served (bound, or already
// served by the main server) so callers never assert a false "running" status.
func (s *Server) startListenerForRecord(l *db.Listener, context string) bool {
	scheme := l.Scheme
	if scheme == "" {
		scheme = l.Type
	}
	if scheme != "http" && scheme != "https" && scheme != "tcp" && scheme != "tls" && scheme != "dns" && scheme != "icmp" && scheme != "ssh" && scheme != "h2c" && scheme != "udp" && scheme != "quic" {
		return false
	}

	// DNS/ICMP listeners use port-less keys
	var key string
	if scheme == "dns" || scheme == "icmp" {
		if scheme == "dns" {
			key = "dns://" + l.DNSDomain
		} else {
			key = "icmp://" + l.ICMPAddr
		}
	} else {
		addr := l.Host + ":" + itoa(l.Port)
		key = scheme + "://" + addr

		// Skip if this is the main server address (already served)
		s.configMu.RLock()
		mainHost, mainPort := s.cfg.Server.Host, s.cfg.Server.Port
		s.configMu.RUnlock()
		mainAddr := mainHost + ":" + itoa(mainPort)
		if addr == mainAddr && (scheme == "http" || scheme == "https") {
			slog.Debug("Listener matches main server address, no extra listener needed",
				"key", key, "context", context)
			return true
		}

		// Check port availability
		if !isPortAvailable(l.Host, l.Port) {
			slog.Warn("Port not available for listener, skipping",
				"key", key, "context", context)
			return false
		}
	}

	if err := s.startExtraListener(l, key, scheme); err != nil {
		slog.Error("Failed to start listener",
			"key", key, "context", context, "err", err)
		return false
	}
	return true
}

// supportedListenerSchemes is the authoritative set of listener schemes the
// server actually binds. Anything outside it fails validation at the API
// boundary instead of silently creating a listener row that never binds
// (previously "wss"/"grpc"/"mtls"/"smb" etc. were coerced to TCP or skipped by
// startListenerForRecord, leaving the operator with a dead "running" listener).
var supportedListenerSchemes = map[string]bool{
	"http": true, "https": true, "tcp": true, "tls": true,
	"dns": true, "icmp": true, "ssh": true, "h2c": true,
	"udp": true, "quic": true,
}

// validateListenerScheme reports whether the scheme is one the server can bind.
func validateListenerScheme(scheme string) bool {
	if scheme == "" {
		return false
	}
	return supportedListenerSchemes[strings.ToLower(scheme)]
}

// listenerSchemeHint returns an actionable error message for a scheme the
// server does not bind, so the operator learns where the feature actually
// lives instead of staring at a dead listener row.
func listenerSchemeHint(scheme string) string {
	switch strings.ToLower(scheme) {
	case "wss", "ws":
		return "listener scheme \"" + scheme + "\" is not a bindable listener type: WebSocket beacons ride an HTTPS listener — create an https listener and pick the wss transport in the payload builder"
	case "smb":
		return "listener scheme \"smb\" is not a bindable listener type: SMB links are configured via server.smb_enabled/smb_pipe for p2p parents"
	case "grpc", "grpcs":
		return "listener scheme \"" + scheme + "\" is not a bindable listener type: gRPC beacons are configured via server.grpc_addr"
	default:
		return "unsupported listener scheme \"" + scheme + "\" (supported: http, https, tcp, tls, dns, icmp, ssh, h2c, udp, quic)"
	}
}

// normalizeListenerProtocol derives all protocol fields from whichever one the user provided.
func normalizeListenerProtocol(l *db.Listener) {
	if l.Scheme != "" {
		l.Protocol = l.Scheme
		switch l.Scheme {
		case "http", "https":
			l.Type = "http"
		case "dns":
			l.Type = "dns"
		case "icmp":
			l.Type = "icmp"
		case "ssh":
			l.Type = "ssh"
		case "h2c":
			l.Type = "h2c"
		case "udp":
			l.Type = "udp"
		case "quic":
			l.Type = "quic"
		default:
			l.Type = "tcp"
		}
	} else if l.Protocol != "" {
		l.Scheme = l.Protocol
		switch l.Protocol {
		case "http", "https":
			l.Type = "http"
		case "dns":
			l.Type = "dns"
		case "icmp":
			l.Type = "icmp"
		case "ssh":
			l.Type = "ssh"
		case "h2c":
			l.Type = "h2c"
		case "udp":
			l.Type = "udp"
		case "quic":
			l.Type = "quic"
		default:
			l.Type = "tcp"
		}
	} else if l.Type != "" {
		switch l.Type {
		case "http":
			l.Scheme = "http"
			l.Protocol = "http"
		case "dns":
			l.Scheme = "dns"
			l.Protocol = "dns"
		case "icmp":
			l.Scheme = "icmp"
			l.Protocol = "icmp"
		case "ssh":
			l.Scheme = "ssh"
			l.Protocol = "ssh"
		case "h2c":
			l.Scheme = "h2c"
			l.Protocol = "h2c"
		case "udp":
			l.Scheme = "udp"
			l.Protocol = "udp"
		case "quic":
			l.Scheme = "quic"
			l.Protocol = "quic"
		default:
			l.Scheme = "tcp"
			l.Protocol = "tcp"
		}
	}
}

func (s *Server) handleCreateListener(c *gin.Context) {
	var l db.Listener
	if err := c.ShouldBindJSON(&l); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	if l.Name == "" {
		l.Name = "Listener " + fmt.Sprintf("%d", l.Port)
	}

	// Reject schemes the server never binds (wss/ws, grpc, mtls, smb as
	// a DB listener, ...) with a clear error instead of silently coercing
	// them to TCP or skipping the bind — a listener row that cannot run must
	// never be reported as "running".
	scheme := l.Scheme
	if scheme == "" {
		scheme = l.Protocol
	}
	if scheme == "" {
		scheme = l.Type
	}
	if !validateListenerScheme(scheme) {
		respondError(c, http.StatusBadRequest, listenerSchemeHint(scheme))
		return
	}

	normalizeListenerProtocol(&l)

	// For port-less listener schemes the operator supplies the domain / bind
	// host through the shared Host field (both the UI and API create path only
	// expose host/port). Persist it into the protocol-specific field that
	// startListenerForRecord and listenerKey actually use, so a DNS/ICMP
	// listener is not born with an empty DNSDomain/ICMPAddr (key "dns://",
	// dead bind, mismatch with resolveListener which reads Host).
	switch l.Scheme {
	case "dns":
		if l.DNSDomain == "" {
			l.DNSDomain = l.Host
		}
	case "icmp":
		if l.ICMPAddr == "" {
			l.ICMPAddr = l.Host
			if l.Host == "" {
				l.ICMPAddr = "0.0.0.0"
			}
		}
	}

	if l.Port != 0 && (l.Port < 1 || l.Port > 65535) {
		respondError(c, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}
	if l.Host != "" && !isValidHost(l.Host) {
		respondError(c, http.StatusBadRequest, "invalid host address")
		return
	}

	// Per-listener TLS knobs are validated before the row exists: a listener
	// that cannot honour its requested posture (bad CA, TLS floor below 1.2,
	// require_client_cert without any CA) must never be created as "running".
	if err := s.validateListenerTLSShape(l.Scheme, listenerTLSOverridesFor(&l)); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	l.Enabled = true
	l.Status = "running"
	// New listeners belong to the operator's tenant; the body must not set it.
	l.TenantID = s.currentTenantID(c)
	if err := s.db.Create(&l).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Listener create"))
		return
	}

	// Start a real listener for the requested port/type. The row is only
	// reported as "running" if the bind actually happened (or the address is
	// already served by the main HTTP server); otherwise it is marked stopped
	// so the operator sees a truthful status instead of a dead "running" row.
	if !s.startListenerForRecord(&l, "created") {
		s.db.Model(&l).Update("status", "stopped")
	}
	s.syncListenerProbe(&l)

	s.broadcastListenerUpdate("created", &l)
	s.LogAuditRecord(c, "listener_create", "listener", fmt.Sprintf("%d", l.ID), l.Name+" "+l.Scheme, true, nil)

	respond(c, gin.H{"listener": l})
}

// loadListenerScoped reads one listener honoring tenant visibility:
// legacy operators (tid 0) see all rows, scoped operators only their own
// tenant's. ok=false doubles as "not found" — foreign rows are directly
// invisible, never 403.
func (s *Server) loadListenerScoped(c *gin.Context, id string) (db.Listener, bool) {
	var l db.Listener
	if err := s.tenantScope(s.db, c).First(&l, id).Error; err != nil {
		return db.Listener{}, false
	}
	return l, true
}

func (s *Server) handleUpdateListener(c *gin.Context) {
	id := c.Param("id")
	l, ok := s.loadListenerScoped(c, id)
	if !ok {
		respondError(c, http.StatusNotFound, "listener not found")
		return
	}
	var updates struct {
		Name          string `json:"name"`
		Scheme        string `json:"scheme"`
		Type          string `json:"type"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		Protocol      string `json:"protocol"`
		Notes         string `json:"notes"`
		Enabled       *bool  `json:"enabled"`
		Tags          string `json:"tags"`
		Color         string `json:"color"`
		Status        string `json:"status"`
		DNSDomain     string `json:"dns_domain"`
		DNSListenAddr string `json:"dns_listen_addr"`
		ICMPAddr      string `json:"icmp_addr"`
		// Pointers: an explicit "" / false clears a per-listener TLS override
		// instead of being indistinguishable from "field absent".
		ClientCAFile      *string `json:"client_ca_file"`
		RequireClientCert *bool   `json:"require_client_cert"`
		MinTLSVersion     *string `json:"min_tls_version"`
	}
	if err := c.ShouldBindJSON(&updates); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "")
		return
	}
	if updates.Name != "" {
		l.Name = updates.Name
	}
	needsNormalize := updates.Scheme != "" || updates.Protocol != "" || updates.Type != ""
	if updates.Scheme != "" {
		if !validateListenerScheme(updates.Scheme) {
			respondError(c, http.StatusBadRequest, listenerSchemeHint(updates.Scheme))
			return
		}
		l.Scheme = updates.Scheme
	} else if updates.Protocol != "" {
		if !validateListenerScheme(updates.Protocol) {
			respondError(c, http.StatusBadRequest, listenerSchemeHint(updates.Protocol))
			return
		}
		l.Protocol = updates.Protocol
	} else if updates.Type != "" {
		if !validateListenerScheme(updates.Type) {
			respondError(c, http.StatusBadRequest, listenerSchemeHint(updates.Type))
			return
		}
		l.Type = updates.Type
	}
	if needsNormalize {
		normalizeListenerProtocol(&l)
	}
	if updates.Host != "" {
		l.Host = updates.Host
	}
	if updates.Port != 0 {
		if updates.Port < 1 || updates.Port > 65535 {
			respondError(c, http.StatusBadRequest, "port must be between 1 and 65535")
			return
		}
		l.Port = updates.Port
	}
	if updates.Host != "" && !isValidHost(updates.Host) {
		respondError(c, http.StatusBadRequest, "invalid host address")
		return
	}
	// Per-listener TLS knobs. Pointers keep "clear the override" distinct from
	// "leave it alone"; the merged shape is validated before Save so a listener
	// can never be persisted in a posture it cannot serve.
	if updates.ClientCAFile != nil {
		l.ClientCAFile = strings.TrimSpace(*updates.ClientCAFile)
	}
	if updates.RequireClientCert != nil {
		l.RequireClientCert = *updates.RequireClientCert
	}
	if updates.MinTLSVersion != nil {
		l.MinTLSVersion = strings.TrimSpace(*updates.MinTLSVersion)
	}
	if err := s.validateListenerTLSShape(l.Scheme, listenerTLSOverridesFor(&l)); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if updates.Notes != "" {
		l.Notes = updates.Notes
	}
	if updates.Enabled != nil {
		l.Enabled = *updates.Enabled
	}
	if updates.Tags != "" {
		l.Tags = updates.Tags
	}
	if updates.Color != "" {
		l.Color = updates.Color
	}
	if updates.Status != "" {
		l.Status = updates.Status
	}
	if updates.DNSDomain != "" {
		l.DNSDomain = updates.DNSDomain
	}
	if updates.DNSListenAddr != "" {
		l.DNSListenAddr = updates.DNSListenAddr
	}
	if updates.ICMPAddr != "" {
		l.ICMPAddr = updates.ICMPAddr
	}
	// Keep protocol-specific fields consistent with Host for port-less schemes
	// when the operator edited only host (UI exposes Host for DNS/ICMP too).
	switch l.Scheme {
	case "dns":
		if l.DNSDomain == "" {
			l.DNSDomain = l.Host
		}
	case "icmp":
		if l.ICMPAddr == "" {
			l.ICMPAddr = l.Host
			if l.Host == "" {
				l.ICMPAddr = "0.0.0.0"
			}
		}
	}
	// Capture the old listener key BEFORE Save mutates l, so we stop the
	// previously-running listener (not the new one) when its bind address
	// changes. GORM's Save writes back all fields, so computing the key
	// afterwards would target the new config and leave the old one orphaned.
	oldKey := listenerKey(&l)
	if err := s.db.Save(&l).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Listener update"))
		return
	}

	// Sync extra listener state (start/stop/restart)
	changed := updates.Host != "" || updates.Port != 0 || updates.Scheme != "" || updates.Protocol != "" || updates.Type != "" ||
		updates.DNSDomain != "" || updates.DNSListenAddr != "" || updates.ICMPAddr != "" ||
		updates.ClientCAFile != nil || updates.RequireClientCert != nil || updates.MinTLSVersion != nil
	if changed {
		s.stopExtraListener(oldKey)
	}
	if l.Enabled {
		if !s.startListenerForRecord(&l, "updated") {
			l.Status = "stopped"
			s.db.Model(&l).Update("status", "stopped")
		}
	} else if updates.Enabled != nil && !*updates.Enabled {
		s.stopExtraListener(oldKey)
	}
	s.syncListenerProbe(&l)

	s.broadcastListenerUpdate("updated", &l)
	s.LogAuditRecord(c, "listener_update", "listener", id, l.Name+" "+l.Scheme, true, nil)

	respond(c, gin.H{"listener": l})
}

func (s *Server) handleDeleteListener(c *gin.Context) {
	id := c.Param("id")

	// Check if any agents are using this listener
	var agentCount int64
	if err := s.db.Model(&db.Implant{}).Where("listener_id = ?", id).Count(&agentCount).Error; err != nil {
		slog.Error("Failed to count listener agents", "listener_id", id, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check agent count"})
		return
	}
	if agentCount > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":       fmt.Sprintf("Cannot delete listener: %d agents still using this listener", agentCount),
			"agent_count": agentCount,
		})
		return
	}

	// Load listener to stop any running extra listener
	l, ok := s.loadListenerScoped(c, id)
	if !ok {
		respondError(c, http.StatusNotFound, "listener not found")
		return
	}
	s.stopExtraListener(listenerKey(&l))
	if s.circuitBreaker != nil {
		s.circuitBreaker.UnregisterTarget(listenerTargetID(&l))
	}

	result := s.tenantScope(s.db, c).Delete(&db.Listener{}, id)
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(result.Error, "Listener delete"))
		return
	}
	if result.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "listener not found")
		return
	}
	s.broadcastListenerUpdate("deleted", &l)
	s.LogAuditRecord(c, "listener_delete", "listener", id, l.Name+" "+l.Scheme, true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleEnableListener(c *gin.Context) {
	id := c.Param("id")
	l, ok := s.loadListenerScoped(c, id)
	if !ok {
		respondError(c, http.StatusNotFound, "listener not found")
		return
	}
	l.Enabled = true
	if err := s.db.Save(&l).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Listener enable"))
		return
	}
	if s.startListenerForRecord(&l, "enabled") {
		l.Status = "running"
	} else {
		l.Status = "stopped"
	}
	if err := s.db.Save(&l).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Listener enable"))
		return
	}
	s.syncListenerProbe(&l)
	s.broadcastListenerUpdate("enabled", &l)
	s.LogAuditRecord(c, "listener_enable", "listener", id, l.Name+" "+l.Scheme, true, nil)
	respond(c, gin.H{"message": "Listener enabled"})
}

func (s *Server) handleDisableListener(c *gin.Context) {
	id := c.Param("id")
	l, ok := s.loadListenerScoped(c, id)
	if !ok {
		respondError(c, http.StatusNotFound, "listener not found")
		return
	}
	l.Enabled = false
	l.Status = "stopped"
	if err := s.db.Save(&l).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Listener disable"))
		return
	}
	s.stopExtraListener(listenerKey(&l))
	s.syncListenerProbe(&l)
	s.broadcastListenerUpdate("disabled", &l)
	s.LogAuditRecord(c, "listener_disable", "listener", id, l.Name+" "+l.Scheme, true, nil)
	respond(c, gin.H{"message": "Listener disabled"})
}

func (s *Server) handleListenersPage(c *gin.Context) {
	var listeners []db.Listener
	if err := s.db.Order("created_at desc").Limit(500).Find(&listeners).Error; err != nil {
		slog.Error("Failed to list listeners for page", "err", err)
	}

	var total, enabled, httpC, tcpC, dnsC, icmpC int64
	if err := s.db.Model(&db.Listener{}).Count(&total).Error; err != nil {
		slog.Error("Failed to count listeners", "err", err)
	}
	if err := s.db.Model(&db.Listener{}).Where("enabled = ?", true).Count(&enabled).Error; err != nil {
		slog.Error("Failed to count enabled listeners", "err", err)
	}
	if err := s.db.Model(&db.Listener{}).Where("type = ?", "http").Count(&httpC).Error; err != nil {
		slog.Error("Failed to count HTTP listeners", "err", err)
	}
	if err := s.db.Model(&db.Listener{}).Where("type = ?", "tcp").Count(&tcpC).Error; err != nil {
		slog.Error("Failed to count TCP listeners", "err", err)
	}
	if err := s.db.Model(&db.Listener{}).Where("type = ?", "dns").Count(&dnsC).Error; err != nil {
		slog.Error("Failed to count DNS listeners", "err", err)
	}
	if err := s.db.Model(&db.Listener{}).Where("type = ?", "icmp").Count(&icmpC).Error; err != nil {
		slog.Error("Failed to count ICMP listeners", "err", err)
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":        "ForgeC2 - Listeners",
		"ActiveNav":    "listeners",
		"Listeners":    listeners,
		"Total":        len(listeners),
		"EnabledCount": enabled,
		"HttpCount":    httpC,
		"TcpCount":     tcpC,
		"DnsCount":     dnsC,
		"IcmpCount":    icmpC,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// EnsureDefaultListener seeds a loopback HTTP listener matching the server's
// own bind address when the listeners table is empty (fresh database), so
// payload generation (one-liners, stagers) works without manual setup. It is
// metadata only — no socket is bound here; the main HTTP server already
// serves beacons on this address. Only an empty table triggers seeding, so
// operators that delete every listener are never second-guessed.
func (s *Server) EnsureDefaultListener(host string, port int) {
	var count int64
	if err := s.db.Model(&db.Listener{}).Count(&count).Error; err != nil || count > 0 {
		return
	}
	if strings.TrimSpace(host) == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port <= 0 || port > 65535 {
		port = 8000
	}
	l := db.Listener{
		Name: "default", Scheme: "http", Type: "http", Protocol: "http",
		Host: host, Port: port, Enabled: true, Status: "running",
	}
	if err := s.db.Create(&l).Error; err != nil {
		slog.Warn("default listener seed failed", "err", err)
		return
	}
	slog.Info("seeded default listener", "host", host, "port", port)
}
