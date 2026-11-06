package server

import (
	"archive/zip"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleHandoverExport builds a one-shot engagement handover bundle: every
// operator-facing artefact (agents, tasks, sanitized credentials, network
// hosts, IOCs, audit trail, AI engagement memory, STIX 2.1 indicators) packed
// into a single ZIP download.
//
// Plaintext passwords are deliberately EXCLUDED — the bundle carries hashes
// and has_password/has_hash markers only; the live vault stays the source of
// truth for reusable secrets. GET /api/handover/export?days=30
func (s *Server) handleHandoverExport(c *gin.Context) {
	if !s.requireExportScope(c, "handover_export", "system") {
		return
	}
	days := atoiDefault(c.Query("days"), 30)
	if days < 1 || days > 365 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days)
	iocs, _, err := s.extractIOCs(days, false, s.reportAgentIDs(c))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to extract indicators")
		return
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("forgec2-handover-%s.zip", stamp)

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/zip")
	c.Status(http.StatusOK)

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	addJSON := func(name string, payload interface{}) {
		body, ok := marshalJSONSafe(payload)
		if !ok {
			slog.Error("Handover: failed to marshal", "file", name)
			return
		}
		w, err := zw.Create(name)
		if err != nil {
			return
		}
		_, _ = w.Write(body)
	}

	var agentCount, taskCount, credCount, hostCount, iocCount int64

	// ── Agents ──
	var agents []db.Implant
	s.tenantScope(s.db, c).Order("hostname").Find(&agents)
	agentCount = int64(len(agents))
	type handoverAgent struct {
		ID        string `json:"id"`
		Hostname  string `json:"hostname"`
		IP        string `json:"ip"`
		PublicIP  string `json:"public_ip"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		Username  string `json:"username"`
		Domain    string `json:"domain"`
		Integrity string `json:"integrity"`
		Status    string `json:"status"`
		Version   string `json:"version"`
		Notes     string `json:"notes"`
		Tags      string `json:"tags"`
		CreatedAt string `json:"created_at"`
		LastSeen  string `json:"last_seen"`
	}
	agentsOut := make([]handoverAgent, 0, len(agents))
	for _, a := range agents {
		agentsOut = append(agentsOut, handoverAgent{
			ID: a.ID, Hostname: a.Hostname, IP: a.IP, PublicIP: a.PublicIP,
			OS: a.OS, Arch: a.Arch, Username: a.Username, Domain: a.Domain,
			Integrity: a.Integrity, Status: a.Status, Version: a.Version,
			Notes: a.Notes, Tags: a.Tags,
			CreatedAt: a.CreatedAt.Format(time.RFC3339),
			LastSeen:  a.LastSeen.Format(time.RFC3339),
		})
	}
	addJSON("agents.json", agentsOut)

	// ── Tasks in range (results truncated per row; full text lives in tasks.jsonl) ──
	// Result is read whole and cut in Go, not with SQL SUBSTR. The column holds
	// AES-256-GCM ciphertext (FC2ENC:) and truncating ciphertext produces an
	// undecryptable blob, which Task.AfterFind then passes straight through — so
	// every exported task shipped a 4 KiB garbage blob where its output should
	// be. The order is therefore fixed: decrypt (hook), truncate (truncateStr).
	// Reads are chunked so at most HandoverTaskChunk results are resident at
	// once; Task.Result is capped at MaxResultSize, so a window-sized read of
	// encrypted rows is unbounded in the pathological case.
	tw, twErr := zw.Create("tasks.jsonl")
	if twErr != nil {
		slog.Error("Handover: failed to create tasks.jsonl", "err", twErr)
		tw = nil
	}
	for offset := 0; offset < HandoverTaskLimit; offset += HandoverTaskChunk {
		var chunk []db.Task
		if err := s.tenantScope(s.db, c).
			Select("id, agent_id, type, command, status, error, created_at, updated_at, created_by, result").
			Where("created_at >= ?", since).
			Order("created_at asc, id asc").
			Offset(offset).Limit(HandoverTaskChunk).
			Find(&chunk).Error; err != nil {
			slog.Error("Handover: failed to query tasks", "err", err)
			break
		}
		for _, t := range chunk {
			taskCount++
			if tw == nil {
				continue
			}
			line := map[string]interface{}{
				"id": t.ID, "agent_id": t.AgentID, "type": t.Type,
				"command": t.Command, "status": t.Status,
				"result":   truncateStr(redactSensitiveResultForAuxiliary(t.Type, t.Result), HandoverResultMaxLen),
				"error":    t.Error,
				"created":  t.CreatedAt.Format(time.RFC3339),
				"operator": t.CreatedBy,
			}
			if line2, ok := marshalJSONSafe(line); ok {
				_, _ = tw.Write(append(line2, '\n'))
			}
		}
		if len(chunk) < HandoverTaskChunk {
			break
		}
	}

	// ── Credentials (SANITIZED: plaintext password column never loaded) ──
	// Dedicated scan struct: CredentialEntry's AfterFind decrypts Password,
	// so loading the model just to test "has password" pulled every secret
	// into RAM. The boolean is computed in SQL instead.
	var creds []struct {
		ID        uint
		AgentID   string
		Domain    string
		Username  string
		Type      string
		Source    string
		Hash      string
		HasPass   bool `gorm:"column:has_password"`
		Confirmed bool
		Tags      string
		Notes     string
		CreatedAt time.Time
	}
	s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).
		Select("id, agent_id, domain, username, type, source, hash, confirmed, tags, notes, created_at, (password <> '') AS has_password").
		Order("created_at asc").Find(&creds)
	credCount = int64(len(creds))
	type handoverCred struct {
		ID        uint   `json:"id"`
		AgentID   string `json:"agent_id"`
		Domain    string `json:"domain"`
		Username  string `json:"username"`
		Type      string `json:"type"`
		Source    string `json:"source"`
		Hash      string `json:"hash,omitempty"`
		HasPass   bool   `json:"has_password"`
		Confirmed bool   `json:"confirmed"`
		Tags      string `json:"tags,omitempty"`
		Notes     string `json:"notes,omitempty"`
	}
	credsOut := make([]handoverCred, 0, len(creds))
	for _, cr := range creds {
		credsOut = append(credsOut, handoverCred{
			ID: cr.ID, AgentID: cr.AgentID, Domain: cr.Domain, Username: cr.Username,
			Type: cr.Type, Source: cr.Source, Hash: cr.Hash, HasPass: cr.HasPass,
			Confirmed: cr.Confirmed, Tags: cr.Tags, Notes: cr.Notes,
		})
	}
	addJSON("credentials.sanitized.json", credsOut)

	// ── Network hosts discovered ──
	// NetworkHost carries no tenant column: contain through the owning
	// agent (same pattern as the report network section).
	var hosts []db.NetworkHost
	hostQuery := s.db.Order("updated_at desc").Limit(20000)
	if tid := s.currentTenantID(c); tid != 0 {
		hostQuery = hostQuery.Where("agent_id IN (?)", s.reportAgentIDs(c))
	}
	hostQuery.Find(&hosts)
	hostCount = int64(len(hosts))
	addJSON("network_hosts.json", hosts)

	// ── IOCs + STIX bundle ──
	iocCount = int64(len(iocs))
	addJSON("iocs.json", iocs)
	addJSON("iocs.stix2.json", buildStixBundle(iocs, stamp))

	// ── Audit trail in range ──
	// Audit rows belong to operator usernames: contain through the
	// caller's tenant (reportUsernames mirrors the dashboard pattern).
	var audits []db.AuditLog
	s.db.Where("\"user\" IN (?) AND created_at >= ?", s.reportUsernames(c), since).Order("created_at asc").Limit(50000).Find(&audits)
	addJSON("audit.jsonl", audits)

	// ── Screenshot manifest (filenames only; binaries stay on server) ──
	type shotRef struct {
		AgentID string `json:"agent_id"`
		File    string `json:"file"`
	}
	shots := make([]shotRef, 0)
	var shotAgents []db.Implant
	s.tenantScope(s.db, c).Select("id").Find(&shotAgents)
	for _, a := range shotAgents {
		for _, m := range s.listScreenshotModTimes(a.ID) {
			shots = append(shots, shotRef{AgentID: a.ID, File: m.name})
		}
	}
	addJSON("screenshots.manifest.json", shots)

	// ── AI engagement memory ──
	// Engagement notes are operator-global memory: only legacy operators
	// see them; scoped operators get a bundle limited to their tenant.
	s.configMu.RLock()
	notes := s.cfg.AI.EngagementNotes
	s.configMu.RUnlock()
	if notes != "" && s.currentTenantID(c) == 0 {
		addJSON("engagement_notes.txt", map[string]interface{}{"notes": notes})
	}

	// ── README manifest ──
	readme := fmt.Sprintf(`ForgeC2 Engagement Handover Bundle
Generated: %s
Range: last %d days

Contents:
  agents.json                  %d implant records (no keys/materials)
  tasks.jsonl                  %d task records incl. truncated results
  credentials.sanitized.json   %d credential records (NO plaintext passwords)
  network_hosts.json           %d discovered hosts
  iocs.json / iocs.stix2.json  %d extracted indicators (+ STIX 2.1 bundle)
  audit.jsonl                  operator audit trail in range
  screenshots.manifest.json    screenshot filename manifest (binaries on server)
  engagement_notes.txt         persistent AI/operator engagement memory

NOTE: plaintext passwords are intentionally excluded. The live ForgeC2
credential vault remains the source of truth for reusable secrets.
`, time.Now().UTC().Format(time.RFC3339), days, agentCount, taskCount, credCount, hostCount, iocCount)
	if w, err := zw.Create("README.txt"); err == nil {
		_, _ = w.Write([]byte(readme))
	}

	s.LogAuditRecord(c, "handover_export", "system", "",
		fmt.Sprintf("Handover bundle exported (%d agents, %d tasks, %d creds, %d IOCs)", agentCount, taskCount, credCount, iocCount), true, nil)
}
