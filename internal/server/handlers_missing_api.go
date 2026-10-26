package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleAPIPackerTemplates(c *gin.Context) {
	templates := payload.BuiltinTemplates()
	respond(c, gin.H{"templates": templates})
}

func (s *Server) handleAPIPackerInfo(c *gin.Context) {
	respond(c, gin.H{
		// Every value advertised here maps to an implemented code path in
		// payload.BuildArtifact. Anything not listed (dll, self-signed /
		// authenticode certificates, tls entry point) is not implemented and
		// must not be offered.
		"encode_types": []string{"none", "xor", "aes", "sgn"},
		"entry_points": []string{"direct", "thread", "callback"},
		"timestamps":   []string{"random", "keep", "custom"},
		"cert_options": []string{"none", "self_signed"},
		"output_types": []string{"exe", "ps1", "raw", "shellcode"},
	})
}

func (s *Server) handleAPISettings(c *gin.Context) {
	// Gather DB stats
	var totalAgents, onlineAgents int64
	if err := s.db.Model(&db.Implant{}).Count(&totalAgents).Error; err != nil {
		slog.Error("Failed to count agents", "err", err)
	}
	if err := s.db.Model(&db.Implant{}).Where("last_seen > ?", time.Now().Add(-s.offlineThreshold())).Count(&onlineAgents).Error; err != nil {
		slog.Error("Failed to count online agents", "err", err)
	}

	var totalListeners, totalCreds, totalTokens, totalAudits int64
	if err := s.db.Model(&db.Listener{}).Count(&totalListeners).Error; err != nil {
		slog.Error("Failed to count listeners", "err", err)
	}
	if err := s.db.Model(&db.CredentialEntry{}).Count(&totalCreds).Error; err != nil {
		slog.Error("Failed to count credentials", "err", err)
	}
	if err := s.db.Model(&db.TokenEntry{}).Count(&totalTokens).Error; err != nil {
		slog.Error("Failed to count tokens", "err", err)
	}
	if err := s.db.Model(&db.AuditLog{}).Count(&totalAudits).Error; err != nil {
		slog.Error("Failed to count audit logs", "err", err)
	}

	// Agents that speak a protocol version the server would refuse: they
	// never check in, so this is where the operator finds out.
	var outdatedProtocolAgents int64
	if err := s.db.Model(&db.Implant{}).
		Where("protocol_version > 0 AND protocol_version <> ?", protocol.CurrentProtocolVersion).
		Count(&outdatedProtocolAgents).Error; err != nil {
		slog.Error("Failed to count agents on an outdated protocol", "err", err)
	}

	var dbSize int64
	s.configMu.RLock()
	dbPath := s.cfg.Database.Path
	// One snapshot: every field below is mutated by the settings save
	// handlers (or the config reloader) under the write lock, so reading
	// them here unlocked was a data race and could pair a torn mix of old
	// and new values in one response.
	cfgSnap := struct {
		port, offlineThreshold, sessionMaxAge, cleanupRetention int
		host, tcpAddr, logLevel                                 string
		tlsEnabled, tcpEnabled                                  bool
		defaultInterval, defaultJitter                          int
		defaultSkipTLS                                          bool
		defaultUA                                               string
	}{
		port:             s.cfg.Server.Port,
		host:             s.cfg.Server.Host,
		tlsEnabled:       s.cfg.Server.TLSEnabled,
		tcpEnabled:       s.cfg.Server.TCPEnabled,
		tcpAddr:          s.cfg.Server.TCPAddr,
		logLevel:         s.cfg.Logging.Level,
		offlineThreshold: s.cfg.Server.OfflineThreshold,
		sessionMaxAge:    s.cfg.Server.SessionMaxAgeHours,
		cleanupRetention: s.cfg.Server.CleanupRetentionDays,
		defaultInterval:  s.cfg.Implant.DefaultInterval,
		defaultJitter:    s.cfg.Implant.DefaultJitter,
		defaultSkipTLS:   s.cfg.Implant.DefaultSkipTLS,
		defaultUA:        s.cfg.Implant.DefaultUA,
	}
	s.configMu.RUnlock()
	if fi, err := os.Stat(dbPath); err == nil {
		dbSize = fi.Size()
	}

	uptime := time.Since(s.startTime)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	respond(c, gin.H{
		"server": gin.H{
			"port":                     cfgSnap.port,
			"host":                     cfgSnap.host,
			"tls_enabled":              cfgSnap.tlsEnabled,
			"tcp_enabled":              cfgSnap.tcpEnabled,
			"tcp_addr":                 cfgSnap.tcpAddr,
			"log_level":                cfgSnap.logLevel,
			"offline_threshold":        cfgSnap.offlineThreshold,
			"session_max_age":          cfgSnap.sessionMaxAge,
			"cleanup_retention":        cfgSnap.cleanupRetention,
			"total_agents":             totalAgents,
			"online_agents":            onlineAgents,
			"total_listeners":          totalListeners,
			"total_credentials":        totalCreds,
			"total_tokens":             totalTokens,
			"total_audits":             totalAudits,
			"database_size":            dbSize,
			"database_path":            "***",
			"data_dir":                 "***",
			"uptime":                   uptime.String(),
			"go_version":               runtime.Version(),
			"goos":                     runtime.GOOS,
			"goarch":                   runtime.GOARCH,
			"goroutines":               runtime.NumGoroutine(),
			"alloc_mem":                int64(m.Alloc),
			"total_alloc_mem":          int64(m.TotalAlloc),
			"num_cpu":                  runtime.NumCPU(),
			"protocol_version":         protocol.CurrentProtocolVersion,
			"protocol_compat_window":   s.protocolCompatWindow(),
			"protocol_outdated_agents": outdatedProtocolAgents,
		},
		"agent": gin.H{
			"default_interval": cfgSnap.defaultInterval,
			"default_jitter":   cfgSnap.defaultJitter,
			"default_skip_tls": cfgSnap.defaultSkipTLS,
			"default_ua":       cfgSnap.defaultUA,
		},
	})
}

func (s *Server) handleAPIMeshTopology(c *gin.Context) {
	var peers []db.MeshPeer
	if err := s.db.Limit(500).Find(&peers).Error; err != nil {
		// An empty 200 here draws a topology with no peers at all.
		handleQueryError(c, err, "Failed to query mesh peers")
		return
	}
	type topoNode struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		PeerCount int    `json:"peer_count"`
		P2PMode   string `json:"p2p_mode"`
	}
	nodeMap := make(map[string]*topoNode)
	var edges [][2]string
	for _, p := range peers {
		if _, ok := nodeMap[p.AgentID]; !ok {
			nodeMap[p.AgentID] = &topoNode{ID: p.AgentID, Label: p.AgentID, P2PMode: "relay"}
		}
		if _, ok := nodeMap[p.PeerID]; !ok {
			nodeMap[p.PeerID] = &topoNode{ID: p.PeerID, Label: p.PeerID, P2PMode: "relay"}
		}
		edges = append(edges, [2]string{p.AgentID, p.PeerID})
	}
	nodes := make([]topoNode, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, *n)
	}
	type topoEdge struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	edgeList := make([]topoEdge, 0, len(edges))
	for _, e := range edges {
		edgeList = append(edgeList, topoEdge{From: e[0], To: e[1]})
	}
	respond(c, gin.H{"nodes": nodes, "edges": edgeList})
}

func (s *Server) handleAPITranslationsStats(c *gin.Context) {
	stats := GetTranslationStats()
	missing := make(map[string][]string)
	for lang := range SupportedLanguages {
		missing[lang] = GetMissingTranslations(lang)
	}
	allKeys := GetAllTranslationKeys()
	respond(c, gin.H{
		"stats":      stats,
		"total_keys": len(allKeys),
		"missing":    missing,
	})
}

func (s *Server) handleAPIPrivesc(c *gin.Context) {
	type privescResult struct {
		ID        uint      `json:"id"`
		AgentID   string    `json:"agent_id"`
		Command   string    `json:"command"`
		Result    string    `json:"result"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
	}
	// Queried into db.Task so Task.AfterFind decrypts Result/Error; scanning
	// straight into the JSON view struct served raw FC2ENC: ciphertext to the
	// privesc panel whenever loot encryption was on.
	var tasks []db.Task
	s.db.Table("tasks").
		Select("id, agent_id, command, result, status, created_at").
		Where("type = ?", "privesc_check").
		Order("created_at DESC").
		Limit(100).
		Find(&tasks)
	results := make([]privescResult, 0, len(tasks))
	for _, t := range tasks {
		results = append(results, privescResult{
			ID: t.ID, AgentID: t.AgentID, Command: t.Command,
			Result: t.Result, Status: t.Status, CreatedAt: t.CreatedAt,
		})
	}
	respond(c, gin.H{"results": results})
}

func (s *Server) handleAPITimelineData(c *gin.Context) {
	type timelineEntry struct {
		ID        uint      `json:"id"`
		Timestamp time.Time `json:"timestamp"`
		Type      string    `json:"type"`
		User      string    `json:"user"`
		Action    string    `json:"action"`
		Details   string    `json:"details"`
		AgentID   string    `json:"agent_id"`
		IP        string    `json:"ip"`
		Success   bool      `json:"success"`
	}

	events := make([]timelineEntry, 0)

	// Audit logs
	var auditLogs []struct {
		ID        uint
		CreatedAt time.Time
		User      string
		Action    string
		Details   string
		AgentID   string
		IP        string
		Success   bool
	}
	if err := s.db.Table("audit_logs").
		Select("id, created_at, user, action, details, agent_id, ip, success").
		Order("created_at DESC").Limit(200).Find(&auditLogs).Error; err != nil {
		// The timeline merges two feeds; dropping one silently would show
		// a partial history as if it were complete.
		handleQueryError(c, err, "Failed to query audit logs for timeline")
		return
	}
	for _, l := range auditLogs {
		events = append(events, timelineEntry{
			ID: l.ID, Timestamp: l.CreatedAt, Type: "audit",
			User: l.User, Action: l.Action, Details: l.Details,
			AgentID: l.AgentID, IP: l.IP, Success: l.Success,
		})
	}

	// Recent tasks
	// Dest is db.Task so Task.AfterFind decrypts Command for the
	// SensitiveTaskTypes; the bare struct rendered an "FC2ENC:..." blob as the
	// event's action text.
	var tasks []db.Task
	if err := s.db.Table("tasks").
		Select("id, created_at, agent_id, type, command, status").
		Order("created_at DESC").Limit(200).Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query tasks for timeline")
		return
	}
	for _, t := range tasks {
		events = append(events, timelineEntry{
			ID: t.ID, Timestamp: t.CreatedAt, Type: "task",
			Action:  fmt.Sprintf("[%s] %s %s", t.Status, t.Type, t.Command),
			AgentID: t.AgentID, Success: t.Status == "completed",
		})
	}

	respond(c, gin.H{"events": events, "total": len(events)})
}

func (s *Server) handleAPIChatHistory(c *gin.Context) {
	channel := c.DefaultQuery("channel", "general")
	var msgs []db.ChatMessage
	if err := s.db.Where("channel = ?", channel).Order("created_at asc").Limit(200).Find(&msgs).Error; err != nil {
		// An empty 200 here reads as "the channel is quiet".
		handleQueryError(c, err, "Failed to query chat history")
		return
	}
	respond(c, gin.H{"messages": msgs})
}

func (s *Server) handleAPIChatChannels(c *gin.Context) {
	type channelRow struct {
		Channel      string `gorm:"column:channel" json:"channel"`
		MessageCount int    `gorm:"column:message_count" json:"message_count"`
		LastMessage  string `gorm:"column:last_message" json:"last_message"`
		LastTime     string `gorm:"column:last_time" json:"last_time"`
	}
	var rows []channelRow
	if err := s.db.Model(&db.ChatMessage{}).
		Select("channel, COUNT(*) as message_count, MAX(message) as last_message, MAX(created_at) as last_time").
		Group("channel").Order("MAX(created_at) DESC").Limit(500).Find(&rows).Error; err != nil {
		// An empty 200 here reads as "no chat channels exist".
		handleQueryError(c, err, "Failed to query chat channels")
		return
	}
	respond(c, gin.H{"channels": rows})
}

// buildChainEntries loads the agent parent/child relations for the chain
// topology views. A failed read returns an error so callers answer 500
// instead of drawing an empty topology that reads as "no agents".
func (s *Server) buildChainEntries() ([]gin.H, error) {
	var agents []db.Implant
	if err := s.db.Select("id, hostname, parent_agent_id").Order("last_seen desc").Limit(5000).Find(&agents).Error; err != nil {
		return nil, err
	}
	entries := make([]gin.H, 0, len(agents))
	for _, a := range agents {
		entries = append(entries, gin.H{
			"id":        a.ID,
			"hostname":  a.Hostname,
			"parent_id": a.ParentAgentID,
		})
	}
	return entries, nil
}

// respondChainEntries answers a chain-topology request, mapping a failed
// relation load to a 500 (never an empty topology).
func (s *Server) respondChainEntries(c *gin.Context, key string) {
	entries, err := s.buildChainEntries()
	if err != nil {
		handleQueryError(c, err, "Failed to build chain entries")
		return
	}
	respond(c, gin.H{key: entries})
}

func (s *Server) handleAPIChainGraph(c *gin.Context) {
	s.respondChainEntries(c, "nodes")
}

func (s *Server) handleAPIChainList(c *gin.Context) {
	s.respondChainEntries(c, "chains")
}

func (s *Server) handleAPISendChatMessage(c *gin.Context) {
	var req struct {
		Message string `json:"message"`
		Channel string `json:"channel"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Message == "" {
		respondError(c, http.StatusBadRequest, "message is required")
		return
	}
	if len(req.Message) > MaxChatMessageBytes {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("message too long (max %d bytes)", MaxChatMessageBytes))
		return
	}
	if req.Channel == "" {
		req.Channel = "general"
	}
	msg := db.ChatMessage{
		Username:  s.currentUsername(c),
		Message:   req.Message,
		Channel:   req.Channel,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(&msg).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Chat message"))
		return
	}
	respond(c, gin.H{"id": msg.ID})
}

func (s *Server) handleDownloadBuild(c *gin.Context) {
	id := c.Param("id")
	var logEntry db.BuildLog
	if err := s.db.First(&logEntry, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, "build not found")
		return
	}
	if logEntry.OutputPath == "" {
		respondError(c, http.StatusNotFound, "no artifact available")
		return
	}
	if _, err := os.Stat(logEntry.OutputPath); os.IsNotExist(err) {
		respondError(c, http.StatusNotFound, "artifact file not found on disk")
		return
	}
	// serveFileSafe performs the canonical containment and symlink checks. Do
	// not duplicate them with a raw string-prefix test here: paths such as
	// "implants-evil" share the prefix of the real directory and make that
	// check misleading, while the helper already handles all path boundaries.
	serveFileSafe(c, logEntry.OutputPath, s.implantDataDir(), logEntry.Filename)
}

func (s *Server) handleConfigReload(c *gin.Context) {
	if s.configReloader == nil {
		respondError(c, http.StatusServiceUnavailable, "config reloader not initialized")
		return
	}

	if err := s.configReloader.Reload(); err != nil {
		slog.Error("Config reload failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Config reload"))
		return
	}

	s.LogAuditRecord(c, "config_reload", "system", "", "Configuration reloaded from disk", true, nil)
	respond(c, gin.H{"message": "configuration reloaded"})
}

func (s *Server) handlePackerArtifact(c *gin.Context) {
	var req payload.BuildArtifactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid request")
		return
	}

	if req.ShellcodeB64 == "" && req.RawEXEB64 == "" {
		respondError(c, http.StatusBadRequest, "either shellcode_b64 or raw_exe_b64 is required")
		return
	}

	dataDir := s.rawDataDir()
	if !filepath.IsAbs(dataDir) {
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
	}

	artifact, filename, err := payload.BuildArtifact(req, dataDir)
	if err != nil {
		slog.Error("Packer artifact build failed", "err", err)
		var verr *payload.ArtifactValidationError
		if errors.As(err, &verr) {
			respondError(c, http.StatusBadRequest, verr.Msg)
			return
		}
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Build"))
		return
	}

	s.LogAuditRecord(c, "packer_build_artifact", "packer", "", fmt.Sprintf("Built %s artifact (%d bytes)", filename, len(artifact)), true, nil)
	respond(c, gin.H{
		"data":     base64.StdEncoding.EncodeToString(artifact),
		"filename": filename,
		"size":     len(artifact),
	})
}

// ── Settings Maintenance Purge ────────────────────────────────────────

func (s *Server) handleSettingsMaintenancePurge(c *gin.Context) {
	retentionDays := s.cleanupRetentionDays()
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	type purgeResult struct {
		Table string `json:"table"`
		Count int64  `json:"count"`
	}
	var results []purgeResult

	tables := []struct {
		name string
		col  string
	}{
		{"build_logs", "created_at"},
		{"tasks", "created_at"},
		{"chat_messages", "created_at"},
		// NOTE: audit_logs / opsec_history / circuit_breaker_events are
		// deliberately NOT purgable here. Forensic timelines are governed
		// solely by server.audit_retention_days (floor 90d) in the automatic
		// cleanup; a one-click UI purge must never erase the tamper chain.
	}
	for _, t := range tables {
		var count int64
		if err := s.db.Table(t.name).Where(t.col+" < ?", cutoff).Count(&count).Error; err != nil {
			slog.Error("Failed to count purgeable records", "table", t.name, "err", err)
		}
		if count > 0 {
			s.db.Table(t.name).Where(t.col+" < ?", cutoff).Delete(nil)
			results = append(results, purgeResult{Table: t.name, Count: count})
		}
	}

	s.LogAuditRecord(c, "maintenance_purge", "system", "", fmt.Sprintf("Purged data older than %d days (forensic tables excluded)", retentionDays), true, nil)
	respond(c, gin.H{"retention_days": retentionDays, "purged": results, "excluded": []string{"audit_logs", "opsec_history", "circuit_breaker_events"}})
}

// ── Agent Chain ──────────────────────────────────────────────────────

func (s *Server) handleAgentChainGet(c *gin.Context) {
	s.respondChainEntries(c, "chain")
}

func (s *Server) handleAgentChainSet(c *gin.Context) {
	// The agent comes from the PATH param (:id) — the UI and OpenAPI spec
	// never send a body agent_id, so the body-only read 400'd every call
	// (feature dead end-to-end). The body may still carry parent_id.
	var req struct {
		ParentID string `json:"parent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	agentID := c.Param("id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id is required")
		return
	}
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	if agentID == req.ParentID {
		respondError(c, http.StatusBadRequest, "agent cannot be its own parent")
		return
	}
	// Ancestor-cycle guard: walking parent_agent_id from the new parent must
	// never reach the child, or proxy chains loop forever.
	if req.ParentID != "" {
		seen := map[string]bool{agentID: true}
		cur := req.ParentID
		for depth := 0; cur != "" && depth < 64; depth++ {
			if seen[cur] {
				respondError(c, http.StatusBadRequest, "chain cycle detected: parent already routes through this agent")
				return
			}
			seen[cur] = true
			var parent db.Implant
			if err := s.db.Select("id, parent_agent_id").First(&parent, "id = ?", cur).Error; err != nil {
				break // unknown parent id — allow, agent may register later
			}
			cur = parent.ParentAgentID
		}
	}
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("id = ?", agentID).Update("parent_agent_id", req.ParentID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Update agent chain"))
		return
	}
	s.LogAuditRecord(c, "agent_chain_set", "agents", agentID, fmt.Sprintf("Parent set to %s", req.ParentID), true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleAgentChainClear(c *gin.Context) {
	// Path param is the source of truth (see handleAgentChainSet).
	agentID := c.Param("id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id is required")
		return
	}
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("id = ?", agentID).Update("parent_agent_id", "").Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Clear agent chain"))
		return
	}
	s.LogAuditRecord(c, "agent_chain_clear", "agents", agentID, "Chain cleared", true, nil)
	respond(c, gin.H{})
}

// ── Mesh Route ───────────────────────────────────────────────────────

func (s *Server) handleMeshRoute(c *gin.Context) {
	// :agentId path param (the UI posts to /mesh/route/<id> with a body;
	// the old c.Query read made this endpoint an unconditional 400).
	agentID := c.Param("agentId")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id is required")
		return
	}
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	var req struct {
		ParentID string `json:"parent_id"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.ParentID != "" && req.ParentID != agentID {
		// Persist the requested mesh route on the implant record.
		if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("id = ?", agentID).
			Update("parent_id", req.ParentID).Error; err != nil {
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "Set mesh route"))
			return
		}
		s.LogAuditRecord(nil, "mesh_route_set", "agents", agentID, fmt.Sprintf("Mesh parent set to %s", req.ParentID), true, nil)
	}
	var peers []db.MeshPeer
	if err := s.db.Where("agent_id = ? OR peer_id = ?", agentID, agentID).Limit(500).Find(&peers).Error; err != nil {
		// An empty 200 here reads as "this agent has no mesh peers".
		handleQueryError(c, err, "Failed to query mesh route")
		return
	}
	respond(c, gin.H{"peers": peers})
}

func (s *Server) handlePackerBundle(c *gin.Context) {
	var req struct {
		AgentEXEB64    string `json:"agent_exe"`
		PESectionText  string `json:"pe_section_text"`
		PESectionData  string `json:"pe_section_data"`
		PESectionRdata string `json:"pe_section_rdata"`
		PESectionReloc string `json:"pe_section_reloc"`
		Timestamp      string `json:"timestamp"`
		TimestampDate  string `json:"timestamp_date"`
		ImportDLLs     string `json:"import_dlls"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid request")
		return
	}

	if req.AgentEXEB64 == "" {
		respondError(c, http.StatusBadRequest, "agent_exe is required")
		return
	}

	artifact, err := base64.StdEncoding.DecodeString(req.AgentEXEB64)
	if err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid agent_exe base64")
		return
	}
	if len(artifact) < 0x40 || artifact[0] != 'M' || artifact[1] != 'Z' {
		respondError(c, http.StatusBadRequest, "agent_exe does not decode to a PE image (missing MZ header)")
		return
	}

	sections := payload.PESectionConfig{}
	if req.PESectionText != "" {
		sections.Text = req.PESectionText
	}
	if req.PESectionData != "" {
		sections.Data = req.PESectionData
	}
	if req.PESectionRdata != "" {
		sections.Rdata = req.PESectionRdata
	}
	if req.PESectionReloc != "" {
		sections.Reloc = req.PESectionReloc
	}

	if sections != (payload.PESectionConfig{}) {
		payload.ApplyPESectionNames(artifact, sections)
	}

	// Timestamp handling: an empty value means "leave the original alone".
	// Defaulting an empty selection to "random" would silently rewrite the
	// binary the operator asked to keep untouched.
	tsOpt := payload.TimestampOption(req.Timestamp)
	if tsOpt == "" {
		tsOpt = payload.TSKeep
	}
	ts, err := payload.GenerateTimestamp(tsOpt, req.TimestampDate)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	payload.ApplyTimestamp(artifact, ts)

	if req.ImportDLLs != "" {
		dlls := strings.Split(req.ImportDLLs, ",")
		for i := range dlls {
			dlls[i] = strings.TrimSpace(dlls[i])
		}
		patched, err := payload.AddBenignImports(artifact, dlls)
		if err != nil {
			var verr *payload.ArtifactValidationError
			if errors.As(err, &verr) {
				respondError(c, http.StatusBadRequest, verr.Msg)
				return
			}
			respondErrorSafe(c, http.StatusBadRequest, err, "import manipulation failed")
			return
		}
		artifact = patched
	}

	s.LogAuditRecord(c, "packer_bundle", "packer", "", fmt.Sprintf("Bundled payload (%d bytes)", len(artifact)), true, nil)
	respond(c, gin.H{
		"data": base64.StdEncoding.EncodeToString(artifact),
		"size": len(artifact),
	})
}
