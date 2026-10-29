package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/auditchain"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Async audit pipeline tuning: request goroutines never touch the DB.
// A single worker owns the hash chain (in-memory lastHash) and persists
// batches. A full queue drops with a counter instead of blocking beacons.
const (
	auditQueueCap      = 4096
	auditBatchMax      = 100
	auditFlushInterval = time.Second
	auditWriteTimeout  = 5 * time.Second
)

// auditChainMu serializes the synchronous fallback path (used when the async
// worker is not running, e.g. unit tests constructing Server directly).
// Production traffic always goes through the single async worker below.
var auditChainMu sync.Mutex

// sensitivePatterns matches common sensitive field names in JSON or URL-encoded data.
// It replaces the value portion with "*****" to prevent secret leakage in audit logs.
var sensitivePatterns = []*regexp.Regexp{
	// JSON: "password":"secretvalue"
	regexp.MustCompile(`"(password|passwd|pass|secret|token|ticket|key|api_key|api_secret|api_token|jwt|session_key|session|cookie|loot_key|extc2_key|backup_key|totp_key|csrf_key|beacon_key|private_key|client_secret)"\s*:\s*"[^"]+"`),
	// JSON with escaped quotes
	regexp.MustCompile(`"(password|passwd|pass|secret|token|ticket|key|api_key|api_secret|api_token|jwt|session_key|session|cookie|loot_key|extc2_key|backup_key|totp_key|csrf_key|beacon_key|private_key|client_secret)"\s*:\s*'[^']+'`),
	// URL-encoded or plain: password=secretvalue. Bare pass/ticket/key are
	// included for lateral-movement credentials (user/pass/ticket in task
	// args); cosmetic over-masking ("hotkey=") is acceptable, leakage is not.
	regexp.MustCompile(`(?i)(password|passwd|pass|secret|token|ticket|key|api_key|api_secret|api_token|jwt|session_key|session|cookie|loot_key|extc2_key|backup_key|totp_key|csrf_key|beacon_key|private_key|client_secret)\s*[:=]\s*\S{4,}`),
	// Key material hex/blobs
	regexp.MustCompile(`(?i)(-----BEGIN.*?KEY-----)(.|\n)*?(-----END.*?KEY-----)`),
}

// sanitizeDetails masks sensitive field values in audit log details.
// This is a defense-in-depth measure — callers should also avoid sending secrets,
// but this catches accidental leakage.
func sanitizeDetails(details string) string {
	if details == "" || !strings.ContainsAny(details, "=:") {
		return details
	}
	result := details
	for _, re := range sensitivePatterns {
		result = re.ReplaceAllString(result, "$1:*****")
	}
	return result
}

// LogAuditRecord creates an audit log entry
// c may be nil for non-HTTP paths (e.g. TCP transport beacons)
func (s *Server) LogAuditRecord(c *gin.Context, action, resource, agentID, details string, success bool, err error) {
	entries := s.buildAuditEntries(c, []auditEntry{{action, resource, agentID, details, success, err}})
	s.flushAuditEntries(entries)
}

// auditEntry is a lightweight intermediate for batch collection.
type auditEntry struct {
	action, resource, agentID, details string
	success                            bool
	err                                error
}

// LogAuditRecords batch-inserts multiple audit log entries in a single DB round-trip.
// c may be nil for non-HTTP paths.
func (s *Server) LogAuditRecords(c *gin.Context, entries []auditEntry) {
	if len(entries) == 0 {
		return
	}
	logEntries := s.buildAuditEntries(c, entries)
	s.flushAuditEntries(logEntries)
}

func (s *Server) buildAuditEntries(c *gin.Context, entries []auditEntry) []db.AuditLog {
	var user, ip string
	tenantID := uint(0)
	if c != nil {
		if u, exists := c.Get("user"); exists {
			if us, ok := u.(string); ok {
				user = us
			} else {
				user = "system"
			}
		} else {
			user = "system"
		}
		ip = c.ClientIP()
		if ip == "" {
			ip = c.Request.RemoteAddr
		}
		// Stamp the owning tenant so audit reads can be scoped. A failed
		// tenant lookup stays 0 (legacy/unscoped) rather than inventing a
		// scope: the entry is still written, and the fallback stays visible
		// to unscoped admins instead of vanishing.
		if tid, ok := s.resolveTenant(c); ok {
			tenantID = tid
		}
	} else {
		user = "system"
	}

	result := make([]db.AuditLog, 0, len(entries))
	for _, e := range entries {
		errorMsg := ""
		if e.err != nil {
			errorMsg = e.err.Error()
		}
		logEntry := db.AuditLog{
			User:     user,
			Action:   e.action,
			Resource: e.resource,
			AgentID:  e.agentID,
			TenantID: tenantID,
			IP:       ip,
			Success:  e.success,
			Error:    sanitizeDetails(errorMsg),
			Details:  sanitizeDetails(e.details),
		}
		result = append(result, logEntry)
	}
	return result
}

func (s *Server) flushAuditEntries(entries []db.AuditLog) {
	if len(entries) == 0 {
		return
	}
	// Audit must never crash the server: tests and degraded states may
	// call in without a database. Log and drop instead of panicking.
	if s == nil || s.db == nil {
		slog.Warn("Dropping audit entries: no database", "count", len(entries))
		s.auditNoteDropped(len(entries))
		return
	}
	// No worker (unit tests, pre-Run): persist synchronously on the caller.
	if s.auditQueue == nil {
		s.flushAuditEntriesSync(entries)
		return
	}
	select {
	case s.auditQueue <- entries:
	default:
		s.auditNoteDropped(len(entries))
		slog.Warn("Dropping audit entries: queue full", "count", len(entries), "cap", auditQueueCap)
	}
}

// auditNoteDropped counts dropped entries (nil-safe for bare test Servers).
func (s *Server) auditNoteDropped(n int) {
	if s == nil {
		return
	}
	s.auditDropped.Add(int64(n))
	if s.metrics != nil && s.metrics.AuditDroppedTotal != nil {
		s.metrics.AuditDroppedTotal.Add(float64(n))
	}
}

// auditQueueDepth reports the number of batches waiting in the async audit
// queue (0 when the worker is not running). Surfaced on the authed
// /api/v1/health endpoint so operators can alert on backlog before drops
// start; nil-safe for bare test Servers.
func (s *Server) auditQueueDepth() int {
	if s == nil {
		return 0
	}
	return len(s.auditQueue)
}

// flushAuditEntriesSync is the synchronous fallback used when the async
// worker is not running. Callers are request goroutines in tests only.
func (s *Server) flushAuditEntriesSync(entries []db.AuditLog) {
	// Serialize appends so the read-last-entry + insert is atomic with respect
	// to other synchronous appends.
	auditChainMu.Lock()
	defer auditChainMu.Unlock()

	if err := s.persistAuditBatch(entries, ""); err != nil {
		slog.Error("Failed to batch-create audit logs", "count", len(entries), "err", err)
		s.auditNoteDropped(len(entries))
		return
	}
	s.sendToSIEM(entries)
}

// auditChainExportRow is one machine-verifiable audit row. It carries both hashes
// and every field the entry hash covers, so a verifier outside this process can
// recompute the chain without database access.
type auditChainExportRow struct {
	ID        uint   `json:"id"`
	TenantID  uint   `json:"tenant_id"`
	User      string `json:"user"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	AgentID   string `json:"agent_id"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
	Error     string `json:"error"`
	Details   string `json:"details"`
	PrevHash  string `json:"prev_hash"`
	EntryHash string `json:"entry_hash"`
	CreatedAt string `json:"created_at"`
}

// handleExportAuditChain streams the chain as newline-delimited JSON so an
// investigator can verify it offline, and so a copy exists that a later rewrite of
// the whole table cannot match.
func (s *Server) handleExportAuditChain(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusServiceUnavailable, "database is not initialized")
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "ndjson")))
	if format != "ndjson" && format != "json" && format != "ecs" && format != "cef" {
		respondError(c, http.StatusBadRequest, `invalid format (expected "ndjson", "json", "ecs" or "cef")`)
		return
	}
	limit := atoiDefault(c.Query("limit"), 50000)
	if limit <= 0 || limit > 200000 {
		limit = 50000
	}
	// since_id makes the export incremental: an investigator who already holds a
	// verified copy fetches only the rows appended since it, instead of pulling the
	// whole table again. Ids are monotonic, so the anchor is simply "greater than".
	sinceID := uint(atoiDefault(c.Query("since_id"), 0))

	query := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c)
	if sinceID > 0 {
		query = query.Where("id > ?", sinceID)
	}

	// Investigation filters. They are deliberately separate from since_id: since_id keeps the chain
	// CONTIGUOUS (it is a prefix continuation), while these skip rows, so a filtered export is a
	// selection and not a verifiable chain. The response says so in headers rather than by injecting
	// a metadata line into the NDJSON, because a verifier must be able to treat every line as a row.
	filtered := false
	if actor := strings.TrimSpace(c.Query("user")); actor != "" {
		query = query.Where("user = ?", actor)
		filtered = true
	}
	if action := strings.TrimSpace(c.Query("action")); action != "" {
		query = query.Where("action = ?", action)
		filtered = true
	}
	if agentID := strings.TrimSpace(c.Query("agent_id")); agentID != "" {
		query = query.Where("agent_id = ?", agentID)
		filtered = true
	}
	// The same parser the task export uses, so "since 24h" cannot mean two different things depending on
	// which export an investigator opened.
	if rawSince := strings.TrimSpace(c.Query("since")); rawSince != "" {
		at, ok := parseTimeBound(rawSince, time.Now())
		if !ok {
			respondError(c, http.StatusBadRequest, formatTimeBoundError("since"))
			return
		}
		query = query.Where("created_at >= ?", comparableLocalTime(at))
		filtered = true
	}
	// until bounds the other side, with the same spellings and the same parser the task export uses: an
	// investigation often wants "the night of the incident", not "everything after it".
	var untilBound time.Time
	hasUntil := false
	if rawUntil := strings.TrimSpace(c.Query("until")); rawUntil != "" {
		at, ok := parseTimeBound(rawUntil, time.Now())
		if !ok {
			respondError(c, http.StatusBadRequest, formatTimeBoundError("until"))
			return
		}
		untilBound, hasUntil = at, true
		query = query.Where("created_at <= ?", comparableLocalTime(at))
		filtered = true
	}
	if hasUntil {
		// A window that ends before it starts selects nothing, and answering an empty file would look like "no
		// audit rows that night" - so it is refused instead.
		if rawSince := strings.TrimSpace(c.Query("since")); rawSince != "" {
			if sinceAt, ok := parseTimeBound(rawSince, time.Now()); ok && sinceAt.After(untilBound) {
				respondError(c, http.StatusBadRequest, "since must not be after until")
				return
			}
		}
	}
	var rows []db.AuditLog
	if err := query.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read the audit chain")
		return
	}

	// Revalidation, not caching: an investigator who re-fetches the same export should get 304 instead
	// of another few megabytes. The tag is derived from the newest audit id, which every append bumps -
	// including the audit_purge marker a purge writes - so it changes whenever the export could have.
	// The format is part of the tag: the NDJSON and the JSON document of the same rows are different
	// documents, so a client that revalidated one must not be told 304 for the other.
	etag := s.auditExportETag(c, sinceID, limit, filtered, format)
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, no-cache")
	if ifNoneMatch(c, etag) {
		// AbortWithStatus rather than Status: the 304 must be written before gin flushes any body.
		c.AbortWithStatus(http.StatusNotModified)
		return
	}

	// The export is buffered so a 200 is always a complete, self-consistent document - which means the
	// buffer is the memory bound. ?limit= allows 200000 rows and a row carries details, error text and
	// two hashes, so without a byte cap one request could ask the single-writer box for hundreds of
	// megabytes. Rows stop being appended at the cap and the response says it was truncated, which is
	// the same honesty rule the row-count header follows.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	byteLimited := false
	for i := range rows {
		if buf.Len() >= auditExportMaxBytes {
			byteLimited = true
			rows = rows[:i]
			break
		}
		row := auditChainExportRow{
			ID: rows[i].ID, TenantID: rows[i].TenantID, User: rows[i].User,
			Action: rows[i].Action, Resource: rows[i].Resource, AgentID: rows[i].AgentID,
			IP: rows[i].IP, Success: rows[i].Success, Error: rows[i].Error,
			Details: rows[i].Details, PrevHash: rows[i].PrevHash, EntryHash: rows[i].EntryHash,
			CreatedAt: rows[i].CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if err := enc.Encode(&row); err != nil {
			slog.Error("Failed to encode an audit chain row", "id", rows[i].ID, "err", err)
			return
		}
	}
	// Same timestamp convention as the task export: two downloads in a day must not overwrite each
	// other in the browser.
	name := "forgec2-audit-chain_" + time.Now().Format("2006-01-02_150405") + ".ndjson"
	c.Header("X-Audit-Export-Rows", strconv.Itoa(len(rows)))
	// Silent truncation is the failure mode worth preventing here: a limited export looks exactly like
	// a complete one, and an investigator verifying a chain would not know part of it was left behind.
	if len(rows) >= limit || byteLimited {
		c.Header("X-Audit-Export-Truncated", "true")
	} else {
		c.Header("X-Audit-Export-Truncated", "false")
	}
	if filtered {
		name = "forgec2-audit-chain_filtered_" + time.Now().Format("2006-01-02_150405") + ".ndjson"
		c.Header("X-Audit-Export-Filtered", "true")
		// 199 is the "miscellaneous warning" class; the text is what a human reads.
		c.Header("Warning", `199 - "filtered export: rows are a selection, not a contiguous hash chain; verify the unfiltered export"`)
	} else {
		c.Header("X-Audit-Export-Filtered", "false")
	}
	// The boolean above says a selection was made; the scope says WHICH selection, so an investigator can reproduce it instead of
	// guessing which of six filters produced the file. Same shape as the task export's header.
	if scope := auditExportScope(c); scope != "" {
		c.Header(auditExportScopeHeader, scope)
		applyExportScope(c, scope)
	} else {
		c.Header(auditExportScopeHeader, "all")
		applyExportScope(c, "all")
	}
	// SIEM projections come BEFORE the json branch: they are alternatives to the verifiable export, not variants of it.
	if format == "ecs" || format == "cef" {
		name = strings.TrimSuffix(name, ".ndjson") + siemFormatFilename(format)
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		// Stated as a header, not only in the docs: a projection looks like an export and cannot verify anything.
		c.Header("Warning", fmt.Sprintf(`199 - %s"`, siemProjectionNote(format)))
		var buf strings.Builder
		for i := range rows {
			row := auditChainExportRow{
				ID: rows[i].ID, TenantID: rows[i].TenantID, User: rows[i].User,
				Action: rows[i].Action, Resource: rows[i].Resource, AgentID: rows[i].AgentID,
				IP: rows[i].IP, Success: rows[i].Success, Error: rows[i].Error,
				Details: rows[i].Details, PrevHash: rows[i].PrevHash, EntryHash: rows[i].EntryHash,
				CreatedAt: rows[i].CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			if format == "cef" {
				buf.WriteString(auditToCEF(row, ServerVersion))
				buf.WriteByte('\n')
				continue
			}
			encoded, err := json.Marshal(auditToECS(row))
			if err != nil {
				handleQueryError(c, err, "Failed to encode the ECS projection")
				return
			}
			buf.Write(encoded)
			buf.WriteByte('\n')
		}
		c.Data(http.StatusOK, siemContentType(format), []byte(buf.String()))
		return
	}

	if format == "json" {
		// One document instead of newline-delimited rows, for tools that do not stream. The rows are the SAME
		// objects, with both hashes: projection is deliberately not offered here, because a verifier needs every
		// field the entry hash covers and a projected export would look verifiable while being unverifiable.
		payload := make([]auditChainExportRow, 0, len(rows))
		for i := range rows {
			payload = append(payload, auditChainExportRow{
				ID: rows[i].ID, TenantID: rows[i].TenantID, User: rows[i].User,
				Action: rows[i].Action, Resource: rows[i].Resource, AgentID: rows[i].AgentID,
				IP: rows[i].IP, Success: rows[i].Success, Error: rows[i].Error,
				Details: rows[i].Details, PrevHash: rows[i].PrevHash, EntryHash: rows[i].EntryHash,
				CreatedAt: rows[i].CreatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		name = strings.TrimSuffix(name, ".ndjson") + ".json"
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		c.JSON(http.StatusOK, gin.H{
			"count":     len(payload),
			"truncated": len(rows) >= limit || byteLimited,
			"filtered":  filtered,
			"format":    "json",
			"rows":      payload,
		})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	c.Data(http.StatusOK, "application/x-ndjson; charset=utf-8", buf.Bytes())
}

// auditChainVerdict is the result of verifying the tamper-evident audit chain.
type auditChainVerdict struct {
	OK         bool   `json:"ok"`
	Checked    int    `json:"checked"`
	FirstBadID uint   `json:"first_bad_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Discontinuities counts purged gaps accepted at the window start (a purge
	// removes rows by design; it is not tampering).
	Discontinuities int `json:"discontinuities,omitempty"`
	// SignatureKind says which of this system's four "signatures" this verdict is about. The word is overloaded - an audit
	// chain entry, an Ed25519 bundle signature, a plugin package signature and a normalised failure shape - and a reader who
	// takes this one for a keyed signature believes the chain proves more than it does.
	SignatureKind string `json:"signature_kind"`
}

// auditHealthCacheTTL bounds how often the UI health endpoint re-walks the chain.
const auditHealthCacheTTL = time.Minute

// cachedAuditChainVerdict returns a verdict no older than ttl, recomputing it
// otherwise. Only the UI health view uses the cache; /ready verifies every poll
// because it is the thing an alert fires on.
func (s *Server) cachedAuditChainVerdict(ttl time.Duration) auditChainVerdict {
	s.auditVerdictMu.Lock()
	defer s.auditVerdictMu.Unlock()
	if !s.auditVerdictAt.IsZero() && time.Since(s.auditVerdictAt) < ttl {
		return s.auditVerdict
	}
	verdict := s.verifyAuditChain(auditReadyChainWindow)
	s.auditVerdict = verdict
	s.auditVerdictAt = time.Now()
	return verdict
}

// verifyAuditChain walks the newest `limit` audit rows in id order and checks the
// two properties the chain exists for: each row's EntryHash still matches its
// own content, and each row's PrevHash names the previous row's EntryHash.
//
// Nothing verified the chain before: six comments call it tamper-evident and the
// retention floor exists to protect it, but a hand-edited row or a deleted one
// went unnoticed. The anchor row just before the window supplies the predecessor
// hash, so the first checked row is verified too.
func (s *Server) verifyAuditChain(limit int) auditChainVerdict {
	if s == nil || s.db == nil {
		return auditChainVerdict{OK: true}
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	// Newest first, then reversed in memory: ORDER BY id ASC LIMIT n returned the
	// OLDEST n rows, so on any server with more than a window of history the verifier
	// re-checked the same ancient block forever and recent tampering was never seen -
	// the opposite of what this function exists for. The walk below still needs
	// ascending order, and rows[0] must stay the oldest row of the window so the anchor
	// lookup keeps working.
	var rows []db.AuditLog
	if err := s.db.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return auditChainVerdict{Reason: "audit rows could not be read"}
	}
	if len(rows) == 0 {
		return auditChainVerdict{OK: true}
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	// Anchor: the row immediately before the window (if any).
	anchor := ""
	var prev db.AuditLog
	if err := s.db.Where("id < ?", rows[0].ID).Order("id DESC").Limit(1).Find(&prev).Error; err == nil && prev.ID != 0 {
		anchor = prev.EntryHash
	}

	expectPrev := anchor
	checked := 0
	purgedBoundaries := 0
	for i := range rows {
		row := &rows[i]
		if want := auditEntryHash(row); row.EntryHash != want {
			return auditChainVerdict{Checked: checked, FirstBadID: row.ID, Reason: "entry content does not match its hash"}
		}
		if expectPrev != "" && row.PrevHash != expectPrev {
			// A gap in the id sequence means rows were removed, and an audit purge
			// legitimately deletes old rows (scoped per tenant, so the removed ids
			// are usually not contiguous). Treat the first row after such a gap as
			// a new anchor; without this the purge endpoint made /ready cry
			// tampering on its own maintenance.
			if i == 0 && prev.ID != 0 && prev.ID+1 < row.ID {
				purgedBoundaries++
			} else {
				return auditChainVerdict{Checked: checked, FirstBadID: row.ID, Reason: "previous-hash link is broken (row edited, removed or reordered)"}
			}
		}
		expectPrev = row.EntryHash
		checked++
	}
	// The head check: the newest hash must match the last line of the anchor file.
	// An attacker who rewrites every row (recomputing all hashes) keeps the table
	// internally consistent, so only a copy outside it can catch that.
	if head := s.auditAnchorHead(); head != "" && head != rows[len(rows)-1].EntryHash {
		return auditChainVerdict{Checked: checked, Discontinuities: purgedBoundaries,
			FirstBadID: rows[len(rows)-1].ID,
			Reason:     "chain head does not match the anchor file (rows were rewritten or the table was replaced)"}
	}
	return auditChainVerdict{OK: true, Checked: checked, Discontinuities: purgedBoundaries}
}

// auditAnchorPath is the append-only file that records the chain head outside the
// audit table. Empty when no data directory is configured (bare test servers).
//
// The path is resolved ONCE at startup (initAuditAnchor) rather than read from the
// config here: the audit path runs from handlers that already hold configMu for
// write (config saves audit themselves), and taking the read lock again deadlocks
// an RWMutex that is not reentrant.
func (s *Server) auditAnchorPath() string {
	return s.auditAnchorFile
}

// initAuditAnchor resolves the anchor file path. Called before traffic starts.
func (s *Server) initAuditAnchor() {
	dir := s.rawDataDir()
	if dir == "" {
		s.auditAnchorFile = ""
		return
	}
	s.auditAnchorFile = filepath.Join(dir, "audit-anchor.log")
}

// appendAuditAnchor records one chain head. Best-effort: an anchor write failure
// must never block an audit insert (losing the alarm is better than losing the
// entry), so it only logs.
func (s *Server) appendAuditAnchor(head string, id uint) {
	path := s.auditAnchorPath()
	if path == "" || head == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Warn("Failed to create the audit anchor directory", "err", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("Failed to open the audit anchor file", "err", err)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %d %s\n", head, id, time.Now().UTC().Format(time.RFC3339)); err != nil {
		slog.Warn("Failed to append to the audit anchor file", "err", err)
	}
}

// auditAnchorHead returns the most recently recorded chain head, or "" when no
// anchor exists yet (fresh install, or a deployment without a data directory).
func (s *Server) auditAnchorHead() string {
	path := s.auditAnchorPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Fields(lines[i])
		if len(fields) >= 1 && fields[0] != "" {
			return fields[0]
		}
	}
	return ""
}

// auditChainHead is the fingerprint an operator archives out of band: the newest
// row id and its entry hash, plus whether the server-side anchor file agrees.
// Pasting it into an incident report (or a ticket) gives a later investigator
// something to compare against that a rewrite of the table cannot reproduce.
type auditChainHead struct {
	ID        uint   `json:"id"`
	EntryHash string `json:"entry_hash"`
	CreatedAt string `json:"created_at,omitempty"`
	// AnchorMatches is false when the anchor file disagrees with the table, which
	// is the signature of a rewritten or replaced audit table.
	AnchorMatches bool `json:"anchor_matches"`
	// AnchorRecording is true when an anchor exists at all; a fresh install has
	// none yet, which is not a failure.
	AnchorRecorded bool `json:"anchor_recorded"`
}

// handleAuditChainAnchor returns the current chain head for out-of-band archiving.
func (s *Server) handleAuditChainAnchor(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusServiceUnavailable, "database is not initialized")
		return
	}
	var newest db.AuditLog
	head := auditChainHead{AnchorMatches: true}
	query := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c)
	if err := query.Order("id DESC").Limit(1).Find(&newest).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read the audit chain")
		return
	}
	if newest.ID != 0 {
		head.ID = newest.ID
		head.EntryHash = newest.EntryHash
		head.CreatedAt = newest.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if anchored := s.auditAnchorHead(); anchored != "" {
		head.AnchorRecorded = true
		head.AnchorMatches = head.EntryHash == "" || anchored == head.EntryHash
	}
	respond(c, head)
}

// handleVerifyAuditChain reports whether the audit chain is intact. Read-only:
// it exists so an investigator can prove the log was not edited after the fact.
func (s *Server) handleVerifyAuditChain(c *gin.Context) {
	verdict := s.verifyAuditChain(atoiDefault(c.Query("limit"), 1000))
	// Say which of the four "signatures" this verdict is about, so nobody reads it as a keyed signature.
	verdict.SignatureKind = auditSignatureKind
	if verdict.OK {
		respond(c, verdict)
		return
	}
	// A broken chain is a finding, not a server error: answer 200 with ok=false
	// so the console can render it, and record it.
	slog.Error("Audit chain verification failed", "first_bad_id", verdict.FirstBadID, "reason", verdict.Reason)
	s.LogAuditRecord(c, "audit_chain_verify", "audit", "", fmt.Sprintf("chain verification FAILED at row %d: %s", verdict.FirstBadID, verdict.Reason), false, nil)
	respond(c, verdict)
}

// auditEntryHash computes the tamper-evident chain hash for one entry.
// TenantID is deliberately NOT part of the chain input: entries written before
// the column existed hash over the same field set, so adding tenant scoping does
// not invalidate historical EntryHash values.
func auditEntryHash(e *db.AuditLog) string {
	// The byte layout lives in internal/auditchain so cmd/verify-audit computes the same digest: two implementations
	// would turn a correct export into a false tampering alarm offline, which is the worst possible failure for a
	// tamper-evidence tool.
	return auditchain.Hash(e.User, e.Action, e.Resource, e.AgentID, e.IP, e.Success, e.Error, e.Details)
}

// persistAuditBatch chains entries onto prevHash ("": read from DB) and
// inserts them in a single bounded transaction.
func (s *Server) persistAuditBatch(entries []db.AuditLog, prevHash string) error {
	// The single choke point before the DB, so the sanitizer runs here as well as
	// in buildAuditEntries: callers that construct db.AuditLog rows directly (the
	// webhook and callback paths do) used to skip it entirely, and one of them
	// persisted a webhook URL - which is a credential. Idempotent.
	for i := range entries {
		entries[i].Details = sanitizeDetails(entries[i].Details)
		entries[i].Error = sanitizeDetails(entries[i].Error)
	}
	ctx, cancel := context.WithTimeout(context.Background(), auditWriteTimeout)
	defer cancel()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lastHash := prevHash
		if lastHash == "" {
			var lastEntry db.AuditLog
			if err := tx.Order("id DESC").First(&lastEntry).Error; err == nil {
				lastHash = lastEntry.EntryHash
			}
		}
		for i := range entries {
			entries[i].PrevHash = lastHash
			entries[i].EntryHash = auditEntryHash(&entries[i])
			lastHash = entries[i].EntryHash
		}
		return tx.CreateInBatches(entries, 50).Error
	})

	// Anchor the new head outside the table once the batch is committed. Without
	// this, an attacker with write access can recompute every hash and the table
	// stays internally consistent; the anchor file is what they would also have to
	// rewrite, and it is not covered by the DB backup.
	if err == nil && len(entries) > 0 {
		last := entries[len(entries)-1]
		s.appendAuditAnchor(last.EntryHash, last.ID)
	}
	return err
}

// startAuditWorker launches the single async audit writer. Called from Run();
// tests that never call Run keep the synchronous fallback.
func (s *Server) startAuditWorker() {
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	s.auditQueue = make(chan []db.AuditLog, auditQueueCap)
	if s.db != nil {
		var lastEntry db.AuditLog
		if err := s.db.Order("id DESC").First(&lastEntry).Error; err == nil {
			s.auditLastHash = lastEntry.EntryHash
		}
	}
	s.wg.Add(1)
	go s.auditWorkerLoop()
}

func (s *Server) auditWorkerLoop() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in audit worker, draining aborted", "recover", r)
		}
	}()
	var pending []db.AuditLog
	ticker := time.NewTicker(auditFlushInterval)
	defer ticker.Stop()
	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = nil
		s.writeAuditBatchWithRetry(batch)
	}
	for {
		select {
		case <-s.ctx.Done():
			// Drain remaining entries with a bounded budget, then exit so
			// shutdown is never held hostage by a wedged database.
			drainDeadline := time.After(auditWriteTimeout)
		drain:
			for {
				select {
				case entries := <-s.auditQueue:
					pending = append(pending, entries...)
					if len(pending) >= auditBatchMax {
						flush()
					}
				case <-drainDeadline:
					break drain
				default:
					break drain
				}
			}
			flush()
			return
		case entries := <-s.auditQueue:
			pending = append(pending, entries...)
			if len(pending) >= auditBatchMax {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// writeAuditBatchWithRetry persists one batch, chaining onto the worker-owned
// lastHash. Retries briefly, then drops with a counter (never blocks).
func (s *Server) writeAuditBatchWithRetry(batch []db.AuditLog) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-s.ctx.Done():
				s.auditNoteDropped(len(batch))
				return
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		if err = s.persistAuditBatch(batch, s.auditLastHash); err == nil {
			s.auditLastHash = batch[len(batch)-1].EntryHash
			s.sendToSIEM(batch)
			return
		}
	}
	slog.Error("Dropping audit batch after retries", "count", len(batch), "err", err)
	s.auditNoteDropped(len(batch))
}

func (s *Server) sendToSIEM(entries []db.AuditLog) {
	if s.siem == nil {
		return
	}
	for _, logEntry := range entries {
		s.siem.Send(SIEMEvent{
			Timestamp: logEntry.CreatedAt,
			Action:    logEntry.Action,
			Resource:  logEntry.Resource,
			AgentID:   logEntry.AgentID,
			User:      logEntry.User,
			IP:        logEntry.IP,
			Success:   logEntry.Success,
			Error:     logEntry.Error,
			Details:   logEntry.Details,
		})
	}
}

// AuditMiddleware is a middleware to log API access
func (s *Server) AuditMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip logging for static files and health checks
		path := c.Request.URL.Path
		if path == "/favicon.ico" || path == "/health" {
			c.Next()
			return
		}

		// Log the request before processing
		var user string
		if u, exists := c.Get("user"); exists {
			if s, ok := u.(string); ok {
				user = s
			} else {
				user = "anonymous"
			}
		} else {
			user = "anonymous"
		}

		ip := c.ClientIP()
		if ip == "" {
			ip = c.Request.RemoteAddr
		}

		slog.Info("API access",
			"method", c.Request.Method,
			"path", path,
			"user", user,
			"ip", ip,
		)

		// Process the request
		c.Next()

		// Log the response
		statusCode := c.Writer.Status()
		success := statusCode >= http.StatusOK && statusCode < http.StatusBadRequest

		// Create audit log for important actions
		if shouldLogAction(path) {
			s.LogAuditRecord(c, getActionType(path), path, "", "", success, nil)
		}
	}
}

// shouldLogAction determines if an action should be logged
func shouldLogAction(path string) bool {
	// Log authentication, agent management, credential access, and command actions.
	// Settings/users/plugins/config changes go through LogOperatorAction, but the
	// middleware also records them here so a direct API call cannot bypass audit.
	// Prefixes must match real route paths (see routes_*.go): user management
	// lives under /users/..., settings writes under /settings/..., not /api/*.
	actionsToLog := []string{
		"/login",
		"/logout",
		"/agents/",
		"/generate/",
		"/tasks",
		"/api/credentials/",
		"/api/tasks/",
		"/api/settings",
		"/api/users",
		"/api/plugins",
		"/api/config",
		// Sensitive writes previously missing from the prefix whitelist.
		"/users/",           // add/edit/toggle/password/force-logout/session revoke
		"/settings/",        // password, malleable, backup/restore, JWT, certs
		"/api/listeners",    // listener create/update/enable/disable/delete
		"/api/api-keys",     // API key create/revoke/rotate
		"/api/scripts",      // script save/delete/execute
		"/api/automation/",  // automation rule CRUD
		"/api/webhooks",     // webhook create/delete/test
		"/api/update-check", // includes admin hot-update
		"/extc2/",           // external C2 channel config
		"/integrations",     // integration create/update/delete/toggle
	}
	for _, action := range actionsToLog {
		if len(path) >= len(action) && path[:len(action)] == action {
			return true
		}
	}
	return false
}

// getActionType extracts the action type from path
func getActionType(path string) string {
	if len(path) >= 7 && path[:7] == "/login" {
		return "login"
	}
	if len(path) >= 8 && path[:8] == "/logout" {
		return "logout"
	}
	if len(path) >= 8 && path[:8] == "/agents/" {
		return "agent_action"
	}
	if len(path) >= 10 && path[:10] == "/generate/" {
		return "generate"
	}
	if len(path) >= 6 && path[:6] == "/tasks" {
		return "view_tasks"
	}
	if len(path) >= 7 && path[:7] == "/users/" {
		return "user_management"
	}
	if len(path) >= 10 && path[:10] == "/settings/" {
		return "settings_change"
	}
	if len(path) >= 14 && path[:14] == "/api/listeners" {
		return "listener_manage"
	}
	if len(path) >= 13 && path[:13] == "/api/api-keys" {
		return "api_key_manage"
	}
	return "api_access"
}

// OperatorAction represents a structured audit trail entry for operator-initiated actions.
type OperatorAction struct {
	Action    string            `json:"action"`
	Resource  string            `json:"resource"`
	TargetID  string            `json:"target_id,omitempty"`
	Details   string            `json:"details,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	RiskLevel string            `json:"risk_level"`
}

// LogOperatorAction records a structured audit trail entry for high-value operator actions.
func (s *Server) LogOperatorAction(c *gin.Context, action OperatorAction) {
	user := "system"
	ip := ""
	if c != nil {
		if u, exists := c.Get("user"); exists {
			if us, ok := u.(string); ok {
				user = us
			}
		}
		ip = c.ClientIP()
	}

	details := sanitizeDetails(action.Details)
	if action.TargetID != "" {
		details = "target=" + action.TargetID + " " + details
	}

	logEntry := db.AuditLog{
		User:     user,
		Action:   "operator:" + action.Action,
		Resource: action.Resource,
		AgentID:  action.TargetID,
		IP:       ip,
		Success:  true,
		Details:  details,
	}

	// Route through the chained flush path so operator actions are part of the
	// same tamper-evident hash chain as beacon-triggered entries.
	s.flushAuditEntries([]db.AuditLog{logEntry})

	slog.Warn("Operator action",
		"user", user,
		"action", action.Action,
		"resource", action.Resource,
		"target", action.TargetID,
		"risk", action.RiskLevel,
		"ip", ip,
	)
}

// LogEmergencyAction logs a killswitch/emergency stop action.
func (s *Server) LogEmergencyAction(c *gin.Context, action string, agentCount int) {
	s.LogOperatorAction(c, OperatorAction{
		Action:    action,
		Resource:  "emergency",
		Details:   "affected_agents=" + itoa(agentCount),
		RiskLevel: "critical",
	})
}

// LogUserManagementAction logs user CRUD operations.
func (s *Server) LogUserManagementAction(c *gin.Context, action, targetUser, details string) {
	s.LogOperatorAction(c, OperatorAction{
		Action:    action,
		Resource:  "user",
		TargetID:  targetUser,
		Details:   details,
		RiskLevel: "high",
	})
}

// LogPluginAction logs plugin install/uninstall/configure operations.
func (s *Server) LogPluginAction(c *gin.Context, action, pluginName, details string) {
	s.LogOperatorAction(c, OperatorAction{
		Action:    action,
		Resource:  "plugin",
		TargetID:  pluginName,
		Details:   details,
		RiskLevel: "medium",
	})
}

// sensitiveConfigFieldMarkers name the config fields whose VALUES must never be
// written to the audit log. Matching is deliberately broad ("key", "token",
// "secret"): over-redacting an innocent field costs a reader a value, leaking a
// key material costs the engagement.
var sensitiveConfigFieldMarkers = []string{
	"secret", "password", "passwd", "key", "token", "credential", "cookie", "session",
}

func isSensitiveConfigField(field string) bool {
	f := strings.ToLower(field)
	for _, marker := range sensitiveConfigFieldMarkers {
		if strings.Contains(f, marker) {
			return true
		}
	}
	return false
}

// LogConfigChange logs system configuration changes.
//
// The values are part of the details string as "old=… new=…", which the generic
// sanitizer cannot recognise as sensitive: the key it sees is "old"/"new", not
// the field name. A change to jwt_secret therefore wrote both secrets to a
// long-lived, admin-readable table. Sensitive fields now record that they
// changed without recording either value.
func (s *Server) LogConfigChange(c *gin.Context, field, oldValue, newValue string) {
	details := "old=" + oldValue + " new=" + newValue
	if isSensitiveConfigField(field) {
		details = "values redacted (sensitive field)"
	}
	s.LogOperatorAction(c, OperatorAction{
		Action:    "config_change",
		Resource:  "system_config",
		TargetID:  field,
		Details:   details,
		RiskLevel: "high",
	})
}

// auditExportMaxBytes bounds how much of the chain one response may hold in memory. A variable so a
// test can drive the cap without generating a 64 MiB table.
var auditExportMaxBytes = 64 << 20
