package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

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

// auditEntryHash computes the tamper-evident chain hash for one entry.
// TenantID is deliberately NOT part of the chain input: entries written
// before the column existed hash over the same field set, so adding tenant
// scoping does not invalidate historical EntryHash values.
// auditChainVerdict is the result of verifying the tamper-evident audit chain.
type auditChainVerdict struct {
	OK         bool   `json:"ok"`
	Checked    int    `json:"checked"`
	FirstBadID uint   `json:"first_bad_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
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
	var rows []db.AuditLog
	if err := s.db.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return auditChainVerdict{Reason: "audit rows could not be read"}
	}
	if len(rows) == 0 {
		return auditChainVerdict{OK: true}
	}

	// Anchor: the row immediately before the window (if any).
	anchor := ""
	var prev db.AuditLog
	if err := s.db.Where("id < ?", rows[0].ID).Order("id DESC").Limit(1).Find(&prev).Error; err == nil && prev.ID != 0 {
		anchor = prev.EntryHash
	}

	expectPrev := anchor
	checked := 0
	for i := range rows {
		row := &rows[i]
		if want := auditEntryHash(row); row.EntryHash != want {
			return auditChainVerdict{Checked: checked, FirstBadID: row.ID, Reason: "entry content does not match its hash"}
		}
		if expectPrev != "" && row.PrevHash != expectPrev {
			return auditChainVerdict{Checked: checked, FirstBadID: row.ID, Reason: "previous-hash link is broken (row edited, removed or reordered)"}
		}
		expectPrev = row.EntryHash
		checked++
	}
	return auditChainVerdict{OK: true, Checked: checked}
}

// handleVerifyAuditChain reports whether the audit chain is intact. Read-only:
// it exists so an investigator can prove the log was not edited after the fact.
func (s *Server) handleVerifyAuditChain(c *gin.Context) {
	verdict := s.verifyAuditChain(atoiDefault(c.Query("limit"), 1000))
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

func auditEntryHash(e *db.AuditLog) string {
	// fmt.Appendf writes the exact same bytes as []byte(fmt.Sprintf(...))
	// without the intermediate string allocation, so the digest — and the
	// tamper-evident chain built on it — is unchanged for historical rows.
	var buf []byte
	buf = fmt.Appendf(buf, "%s|%s|%s|%s|%s|%t|%s|%s",
		e.User, e.Action, e.Resource, e.AgentID, e.IP, e.Success, e.Error, e.Details)
	h := sha256.Sum256(buf)
	return fmt.Sprintf("%x", h)
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
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
