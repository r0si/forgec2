package server

import (
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Pre-compiled regex patterns for credential parsing (performance optimization)
var (
	credReBlock       *regexp.Regexp
	credReDomain      *regexp.Regexp
	credReNTLM        *regexp.Regexp
	credReSHA1        *regexp.Regexp
	credRePassword    *regexp.Regexp
	credReSAM         *regexp.Regexp
	credReSplitBlocks *regexp.Regexp
	credReSimple      *regexp.Regexp
	credOnce          sync.Once
)

func initCredRegexps() {
	credOnce.Do(func() {
		credReBlock = regexp.MustCompile(`(?i)(?:Username|User)\s*:\s*(.+)`)
		credReDomain = regexp.MustCompile(`(?i)Domain\s*:\s*(.+)`)
		credReNTLM = regexp.MustCompile(`(?i)NTLM\s*:\s*([a-fA-F0-9]{32})`)
		credReSHA1 = regexp.MustCompile(`(?i)SHA1?\s*:\s*([a-fA-F0-9]{40})`)
		credRePassword = regexp.MustCompile(`(?i)Password\s*:\s*(.+?)\s*$`)
		credReSAM = regexp.MustCompile(`^([^\s:]+):(\d+):([a-fA-F0-9]{32}):([a-fA-F0-9]{32}):::`)
		credReSplitBlocks = regexp.MustCompile(`\n\s*\n`)
		credReSimple = regexp.MustCompile(`^(?:([^\s:\\]+)\\)?([^\s:]+):(.+)$`)
	})
}

// splitASREPRoastLine parses one agent asreproast line
// ("$krb5asrep$<etype>$<user>@<realm>:<cipherhex>") into user and realm. The
// agent requests RC4-only tickets (etype 23), so the stored line is directly
// hashcat -m 18200 crackable; any other valid etype is kept as an artifact.
func splitASREPRoastLine(line string) (user string, realm string, ok bool) {
	if !strings.HasPrefix(line, "$krb5asrep$") {
		return "", "", false
	}
	rest := line[len("$krb5asrep$"):]
	etypeEnd := strings.Index(rest, "$")
	if etypeEnd <= 0 {
		return "", "", false
	}
	for _, c := range rest[:etypeEnd] {
		if c < '0' || c > '9' {
			return "", "", false
		}
	}
	acct := rest[etypeEnd+1:]
	at := strings.Index(acct, "@")
	colon := strings.LastIndex(acct, ":")
	if at <= 0 || colon <= at+1 {
		return "", "", false
	}
	if len(acct[colon+1:]) < 16 {
		return "", "", false
	}
	return acct[:at], acct[at+1 : colon], true
}

// parseAndStoreASREPRoastResults ingests agent asreproast output into the
// credential vault. The agent emits one hashcat -m 18200 line per Roastable
// account; failure notes ("[!] ...") and foreign lines are skipped. The stored
// hash is the full line so operators can paste it straight into hashcat.
func (s *Server) parseAndStoreASREPRoastResults(agentID string, raw string, taskID uint) {
	database := s.db
	lines := strings.Split(raw, "\n")
	type credKey struct {
		AgentID, Domain, Username, Hash, Source string
	}
	var existing []db.CredentialEntry
	database.Model(&db.CredentialEntry{}).
		Select("agent_id, domain, username, hash").
		Where("agent_id = ? AND source = 'asreproast'", agentID).
		Find(&existing)
	existSet := make(map[credKey]bool, len(existing))
	for _, k := range existing {
		existSet[credKey{AgentID: agentID, Domain: k.Domain, Username: k.Username, Hash: k.Hash, Source: "asreproast"}] = true
	}

	var newEntries []db.CredentialEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[!]") {
			continue
		}
		user, realm, ok := splitASREPRoastLine(line)
		if !ok {
			continue
		}
		k := credKey{AgentID: agentID, Domain: realm, Username: user, Hash: line, Source: "asreproast"}
		if existSet[k] {
			continue
		}
		existSet[k] = true
		newEntries = append(newEntries, db.CredentialEntry{
			AgentID: agentID, Domain: realm, Username: user, Hash: line,
			Source: "asreproast", Type: "krb_asrep", Notes: "AS-REP: " + user + "@" + realm, TaskID: taskID,
		})
	}
	if len(newEntries) > 0 {
		if err := database.Create(&newEntries).Error; err != nil {
			slog.Error("Failed to store asreproast hashes", "agent_id", agentID, "err", err)
		} else {
			slog.Info("AS-REP roast hashes stored in vault", "agent_id", agentID, "count", len(newEntries))
			s.LogAuditRecord(nil, "credential_ingest", "credential", agentID,
				"stored "+strconv.Itoa(len(newEntries))+" asreproast hashes", true, nil)
			s.broadcastOperatorEvent(map[string]interface{}{
				"type":     "credential_update",
				"action":   "found",
				"agent_id": agentID,
				"count":    len(newEntries),
			})
		}
	}
}

// parseAndStoreCredentials parses common credential dump formats (mimikatz-style)
// and stores extracted entries in the credential vault.
func (s *Server) parseAndStoreCredentials(agentID string, raw string, taskID uint) {
	database := s.db
	entries := parseCredentialsFromText(raw, agentID, taskID)
	if len(entries) == 0 {
		return
	}

	// Attribute harvests to the agent's owning tenant once, up front, so the
	// per-row write hook doesn't re-SELECT the agent (and the default tenant)
	// for every credential on the single-writer pool.
	if tid := s.agentTenantID(agentID); tid != 0 {
		for i := range entries {
			entries[i].TenantID = tid
		}
	}

	// Optimization: load existing dedup keys once and use a HashSet for O(1)
	// lookup. The bounded dedup_key column (a digest of the plaintext
	// identity, populated at insert) replaces the full-row scan: no blob I/O,
	// no per-row AfterFind decryption, and it stops re-walking the agent's
	// entire credential history on every fresh harvest.
	existingSet := make(map[string]bool)
	var lastID uint
	const batchSize = 1000
	for {
		var rows []struct {
			ID       uint
			DedupKey string `gorm:"dedup_key"`
		}
		if err := database.Model(&db.CredentialEntry{}).
			Select("id", "dedup_key").
			Where("agent_id = ? AND id > ? AND dedup_key <> ?", agentID, lastID, "").
			Order("id ASC").Limit(batchSize).Scan(&rows).Error; err != nil {
			slog.Error("Failed to load existing credential dedup keys", "err", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			existingSet[r.DedupKey] = true
		}
		lastID = rows[len(rows)-1].ID
	}

	// Filter duplicates using HashSet. Keys digest the plaintext identity, so
	// they are computed on the parsed (unencrypted) entries — the write hook
	// stores the same digest in dedup_key at insert time.
	var batch []db.CredentialEntry
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		key := db.ComputeCredDedupKey(e.AgentID, e.Domain, e.Username, e.Password, e.Hash)
		if existingSet[key] || seen[key] {
			continue
		}
		seen[key] = true
		batch = append(batch, e)
	}

	if len(batch) > 0 {
		if err := database.CreateInBatches(batch, 50).Error; err != nil {
			// Fail-closed per-row degrade: a vault outage must reject rows
			// (never store plaintext), and a single bad row must not sink
			// the batch. Retry rows individually and count the outcome.
			stored, failed := 0, 0
			for i := range batch {
				if e := database.Create(&batch[i]).Error; e != nil {
					failed++
					slog.Error("Failed to store credential entry", "agent_id", agentID, "err", e)
				} else {
					stored++
				}
			}
			if s.metrics != nil && s.metrics.VaultErrorsTotal != nil && failed > 0 {
				s.metrics.VaultErrorsTotal.WithLabelValues("harvest-batch").Add(float64(failed))
			}
			slog.Warn("Credential batch partially stored", "agent_id", agentID, "stored", stored, "failed", failed, "batch_err", err)
			return
		}
		slog.Info("Credentials stored in vault", "agent_id", agentID, "count", len(batch))
		s.LogAuditRecord(nil, "credential_ingest", "credential", agentID,
			"stored "+strconv.Itoa(len(batch))+" credentials (source="+parseCredentialSource(raw)+")", true, nil)
		// Push the vault change to open dashboard sessions so the
		// Credentials page refreshes without polling.
		s.broadcastOperatorEvent(map[string]interface{}{
			"type":     "credential_update",
			"action":   "found",
			"agent_id": agentID,
			"count":    len(batch),
		})
	}
}

// agentTenantID resolves the tenant owning an agent's harvests, with a
// default-tenant fallback for orphaned agent rows (mirrors the CredentialEntry
// write hook, but resolved once per harvest instead of once per row). Returns
// 0 when neither resolves; callers leave TenantID unset in that case and the
// hook's own fallback takes over.
func (s *Server) agentTenantID(agentID string) uint {
	var agent db.Implant
	if err := s.db.Select("tenant_id").Where("id = ?", agentID).First(&agent).Error; err == nil && agent.TenantID != 0 {
		return agent.TenantID
	}
	var t db.Tenant
	if err := s.db.Where("name = ?", db.DefaultTenantName).First(&t).Error; err == nil {
		return t.ID
	}
	return 0
}

// parseCredentialSource classifies a raw credential dump for audit detail
// strings. The raw bytes are never logged.
func parseCredentialSource(raw string) string {
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "krb5tgs") || strings.Contains(low, "$krb5tgs"):
		return "kerberoast"
	case strings.Contains(low, "sekurlsa") || strings.Contains(low, "logonpasswords"):
		return "mimikatz"
	default:
		return "dump"
	}
}

// splitKerberoastLine splits one kerberoast output line into
// (user, domain, spn, hash). Three formats are accepted:
//   - legacy "SPN:HASH" from older implants (user/domain derived from the SPN),
//   - hashcat-mode lines "$krb5tgs$23$*user$realm$spn*$checksum$edata2" emitted
//     by the agent's DER converter; the account segment carries user, realm and
//     spn verbatim,
//   - AES lines "$krb5tgs$17$user$realm$checksum$edata2" / "$krb5tgs$18$…"
//     (hashcat 19600/19700) where the UPN-derived user/realm are the fields and
//     there is no SPN segment.
//
// The full hashcat line is always kept as the stored hash so it can be dropped
// straight into hashcat -m 13100 / 19600 / 19700.
func splitKerberoastLine(line string) (user string, domain string, spn string, hash string, ok bool) {
	if strings.HasPrefix(line, "$krb5tgs$17$") || strings.HasPrefix(line, "$krb5tgs$18$") {
		// "$krb5tgs$18$user$realm$checksum$edata2" — four $-delimited fields.
		fields := strings.Split(line[len("$krb5tgs$18$"):], "$")
		if len(fields) != 4 || fields[0] == "" || fields[1] == "" {
			return "", "", "", "", false
		}
		checksum, edata2 := fields[2], fields[3]
		hexBytes, err := hex.DecodeString(checksum)
		if err != nil || len(hexBytes) != 12 {
			return "", "", "", "", false
		}
		if len(edata2) < 64 {
			return "", "", "", "", false
		}
		if _, err := hex.DecodeString(edata2); err != nil {
			return "", "", "", "", false
		}
		return fields[0], fields[1], fields[0], line, true
	}
	if strings.HasPrefix(line, "$krb5tgs$") {
		// "$krb5tgs$23$*user$realm$spn*$checksum$edata2" — the account segment
		// is delimited by the leading "$*" and the trailing "*$".
		acctStart := strings.Index(line, "$*")
		if acctStart < 0 {
			return "", "", "", "", false
		}
		acctBody := line[acctStart+2:]
		acctEnd := strings.Index(acctBody, "*$")
		if acctEnd < 0 {
			return "", "", "", "", false
		}
		ap := strings.Split(acctBody[:acctEnd], "$")
		if len(ap) != 3 || ap[0] == "" || ap[1] == "" || ap[2] == "" {
			return "", "", "", "", false
		}
		payload := acctBody[acctEnd+2:]
		sep := strings.Index(payload, "$")
		if sep != 32 || len(payload) <= sep+1 {
			return "", "", "", "", false
		}
		return ap[0], ap[1], ap[2], line, true
	}
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", "", "", false
	}
	spn = strings.TrimSpace(parts[0])
	hash = strings.TrimSpace(parts[1])
	if spn == "" || hash == "" {
		return "", "", "", "", false
	}
	user = spn
	if atIdx := strings.Index(spn, "@"); atIdx > 0 {
		user = spn[:atIdx]
		domain = spn[atIdx+1:]
	} else if slashIdx := strings.Index(spn, "/"); slashIdx > 0 {
		user = spn[slashIdx+1:]
		domain = spn[:slashIdx]
	}
	return user, domain, spn, hash, true
}

// parseAndStoreKerberoastResults parses kerberoast TGS hash output (SPN:HASH
// or hashcat-mode lines) and stores entries in the credential vault.
func (s *Server) parseAndStoreKerberoastResults(agentID string, raw string, taskID uint) {
	database := s.db
	lines := strings.Split(raw, "\n")
	// Batch-load existing hashes to avoid N+1 queries. Scan into the full
	// model (not an anonymous struct) so AfterFind transparently decrypts
	// the stored hash and the dedup key compares plaintext.
	type credKey struct {
		AgentID, Domain, Username, Hash, Source string
	}
	var existing []db.CredentialEntry
	database.Model(&db.CredentialEntry{}).
		Select("agent_id, domain, username, hash").
		Where("agent_id = ? AND source = 'kerberoast'", agentID).
		Find(&existing)
	existSet := make(map[credKey]bool, len(existing))
	for _, k := range existing {
		existSet[credKey{AgentID: agentID, Domain: k.Domain, Username: k.Username, Hash: k.Hash, Source: "kerberoast"}] = true
	}

	var newEntries []db.CredentialEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		user, domain, spn, hash, ok := splitKerberoastLine(line)
		if !ok {
			continue
		}
		k := credKey{AgentID: agentID, Domain: domain, Username: user, Hash: hash, Source: "kerberoast"}
		if existSet[k] {
			continue
		}
		existSet[k] = true
		newEntries = append(newEntries, db.CredentialEntry{
			AgentID: agentID, Domain: domain, Username: user, Hash: hash,
			Source: "kerberoast", Type: "krb_tgs", Notes: "SPN: " + spn, TaskID: taskID,
		})
	}
	if len(newEntries) > 0 {
		if err := database.Create(&newEntries).Error; err != nil {
			slog.Error("Failed to store kerberoast hashes", "agent_id", agentID, "err", err)
		} else {
			slog.Info("Kerberoast hashes stored in vault", "agent_id", agentID, "count", len(newEntries))
			s.LogAuditRecord(nil, "credential_ingest", "credential", agentID,
				"stored "+strconv.Itoa(len(newEntries))+" kerberoast hashes", true, nil)
			// Push the vault change to open dashboard sessions so the
			// Credentials page refreshes without polling.
			s.broadcastOperatorEvent(map[string]interface{}{
				"type":     "credential_update",
				"action":   "found",
				"agent_id": agentID,
				"count":    len(newEntries),
			})
		}
	}
}

// parseAndStorePasswordSprayResults ingests valid password spray hits into
// the credential vault. The spray output is JSON with per-user status; the
// sprayed password and domain travel in the agent-side task (Command) and are
// copied into the stored entries. Only status "valid" hits are stored (they
// were verified by LogonUser on the agent), locked/erroneous results are
// discarded. Returns the number of new entries stored.
func (s *Server) parseAndStorePasswordSprayResults(agentID string, task db.Task, raw string) int {
	if task.Command == "" || raw == "" {
		return 0
	}
	cmdParts := strings.SplitN(task.Command, "|", 4)
	if len(cmdParts) < 2 {
		return 0
	}
	password := cmdParts[0]
	domain := cmdParts[1]

	var out struct {
		Results []struct {
			User   string `json:"user"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out.Results) == 0 {
		return 0
	}

	type credKey struct {
		AgentID, Domain, Username, Password, Source string
	}
	var existing []db.CredentialEntry
	s.db.Model(&db.CredentialEntry{}).
		Select("domain, username, password").
		Where("agent_id = ? AND source = ?", agentID, "password_spray").
		Find(&existing)
	existSet := make(map[credKey]bool, len(existing))
	for _, k := range existing {
		existSet[credKey{agentID, k.Domain, k.Username, k.Password, "password_spray"}] = true
	}

	// Resolve the owning tenant once for the whole result instead of letting
	// the per-row write hook re-SELECT it.
	tenantID := s.agentTenantID(agentID)
	var newEntries []db.CredentialEntry
	for _, r := range out.Results {
		if r.Status != "valid" {
			continue
		}
		entryUser := strings.TrimSpace(r.User)
		if entryUser == "" {
			continue
		}
		entryDomain := domain
		if atIdx := strings.Index(entryUser, "@"); atIdx > 0 {
			if entryDomain == "" {
				entryDomain = entryUser[atIdx+1:]
			}
			entryUser = entryUser[:atIdx]
		}
		if slashIdx := strings.Index(entryUser, "\\"); slashIdx > 0 {
			if entryDomain == "" {
				entryDomain = entryUser[:slashIdx]
			}
			entryUser = entryUser[slashIdx+1:]
		}
		if entryUser == "" {
			continue
		}
		k := credKey{agentID, entryDomain, entryUser, password, "password_spray"}
		if existSet[k] {
			continue
		}
		existSet[k] = true
		newEntries = append(newEntries, db.CredentialEntry{
			AgentID:   agentID,
			Domain:    entryDomain,
			Username:  entryUser,
			Password:  password,
			Source:    "password_spray",
			Type:      "cleartext",
			Confirmed: true, // verified by LogonUser during the spray
			Notes:     "validated via password spray",
			TaskID:    task.ID,
			TenantID:  tenantID,
		})
	}

	if len(newEntries) == 0 {
		return 0
	}
	if err := s.db.Create(&newEntries).Error; err != nil {
		slog.Error("Failed to store password spray credentials", "agent_id", agentID, "err", err)
		return 0
	}
	s.recordUsageBatch(newEntries, task.ID, agentID, "spray", "ok")
	slog.Info("Password spray hits stored in vault", "agent_id", agentID, "count", len(newEntries))
	s.LogAuditRecord(nil, "credential_ingest", "credential", agentID,
		"stored "+strconv.Itoa(len(newEntries))+" password spray credentials", true, nil)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":     "credential_update",
		"action":   "found",
		"agent_id": agentID,
		"count":    len(newEntries),
	})
	return len(newEntries)
}

// parseAndStoreCredCheckResult processes a completed cred_check task result:
// a "valid" status confirms the matching vault entry (agent + domain +
// username + password) and resets the per-(agent,domain) fuse; invalid/locked
// results record a fuse failure. The task Command carries
// user|domain|password|[dc_ip]; the password is never written to audit.
// Returns true when at least one vault entry was confirmed.
func (s *Server) parseAndStoreCredCheckResult(agentID string, task db.Task, raw string) bool {
	if task.Command == "" || raw == "" {
		return false
	}
	cmdParts := strings.SplitN(task.Command, "|", 4)
	if len(cmdParts) < 3 {
		return false
	}
	cmdDomain := strings.TrimSpace(cmdParts[1])
	cmdPassword := cmdParts[2]

	var out struct {
		Results []struct {
			User   string `json:"user"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out.Results) == 0 {
		return false
	}
	r := out.Results[0]
	status := r.Status

	switch status {
	case "valid":
		s.credCheckFuse.reset(agentID, cmdDomain)
	case "invalid", "locked":
		s.credCheckFuse.recordFailure(agentID, cmdDomain)
	default:
		return false
	}

	confirmed := false
	if status == "valid" {
		entryUser := strings.TrimSpace(r.User)
		entryDomain := cmdDomain
		if atIdx := strings.Index(entryUser, "@"); atIdx > 0 {
			if entryDomain == "" {
				entryDomain = entryUser[atIdx+1:]
			}
			entryUser = entryUser[:atIdx]
		}
		if slashIdx := strings.Index(entryUser, "\\"); slashIdx > 0 {
			if entryDomain == "" {
				entryDomain = entryUser[:slashIdx]
			}
			entryUser = entryUser[slashIdx+1:]
		}
		if entryUser == "" {
			return false
		}

		var entries []db.CredentialEntry
		err := s.db.Where("agent_id = ? AND domain = ? AND username = ?",
			agentID, entryDomain, entryUser).Find(&entries).Error
		if err != nil {
			slog.Error("Failed to load candidate credentials", "agent_id", agentID, "error", err)
			return false
		}
		// Password is encrypted at rest (BeforeCreate), so the match happens in
		// Go against the AfterFind-decrypted value, like the spray dedup.
		var matched *db.CredentialEntry
		for i := range entries {
			if entries[i].Password == cmdPassword {
				matched = &entries[i]
				break
			}
		}
		if matched == nil {
			// The checked credential is not (or no longer) in the vault —
			// still log the successful validation for the audit trail.
			slog.Info("Credential validated but not in vault",
				"agent_id", agentID, "user", entryUser, "domain", entryDomain)
		} else {
			entry := *matched
			entry.Confirmed = true
			entry.TaskID = task.ID
			now := time.Now()
			if err := s.db.Model(&entry).Updates(map[string]interface{}{
				"confirmed":        true,
				"task_id":          task.ID,
				"notes":            appendCredCheckNote(entry.Notes),
				"verify_status":    "valid",
				"last_verified_at": &now,
			}).Error; err != nil {
				slog.Error("Failed to confirm credential entry", "agent_id", agentID, "error", err)
				return false
			}
			confirmed = true
			s.RecordUsage(matched.ID, task.ID, agentID, "verify", "ok",
				fmt.Sprintf("%s@%s", entryUser, entryDomain), "")
			slog.Info("Credential confirmed via cred_check", "agent_id", agentID, "user", entryUser, "domain", entryDomain)
			s.LogAuditRecord(nil, "cred_check", "credential", agentID,
				fmt.Sprintf("credential confirmed: %s@%s", entryUser, entryDomain), true, nil)
		}
	}

	if status == "invalid" || status == "locked" {
		// Lifecycle feedback: pending batch-verification entries for this user
		// flip to invalid so operators stop chasing dead credentials.
		rUser := strings.TrimSpace(r.User)
		if atIdx := strings.Index(rUser, "@"); atIdx > 0 {
			rUser = rUser[:atIdx]
		}
		if slashIdx := strings.Index(rUser, "\\"); slashIdx > 0 {
			rUser = rUser[slashIdx+1:]
		}
		if rUser != "" {
			now := time.Now()
			s.db.Model(&db.CredentialEntry{}).
				Where("agent_id = ? AND domain = ? AND username = ? AND verify_status = ?",
					agentID, cmdDomain, rUser, "pending").
				Updates(map[string]interface{}{
					"verify_status":    "invalid",
					"last_verified_at": &now,
				})
		}
	}

	if confirmed {
		s.broadcastOperatorEvent(map[string]interface{}{
			"type":     "credential_update",
			"action":   "confirmed",
			"agent_id": agentID,
			"count":    1,
		})
	}
	return confirmed
}

// appendCredCheckNote marks a vault entry as validated by a credential check.
func appendCredCheckNote(notes string) string {
	if strings.Contains(notes, "validated via credential check") {
		return notes
	}
	if notes == "" {
		return "validated via credential check"
	}
	return notes + "; validated via credential check"
}

// parseCredentialsFromText handles multiple output formats
func parseCredentialsFromText(raw string, agentID string, taskID uint) []db.CredentialEntry {
	initCredRegexps() // Ensure regexps are compiled
	var entries []db.CredentialEntry

	// Pattern 1: mimikatz sekurlsa::logonpasswords style
	blocks := credReSplitBlocks.Split(raw, -1)
	for _, block := range blocks {
		var entry db.CredentialEntry
		entry.AgentID = agentID
		entry.TaskID = taskID
		entry.Source = "mimikatz"

		if m := credReBlock.FindStringSubmatch(block); len(m) > 1 {
			entry.Username = strings.TrimSpace(m[1])
		}
		if m := credReDomain.FindStringSubmatch(block); len(m) > 1 {
			entry.Domain = strings.TrimSpace(m[1])
		}
		if m := credReNTLM.FindStringSubmatch(block); len(m) > 1 {
			entry.Hash = strings.TrimSpace(m[1])
			entry.Type = "ntlm"
		}
		if m := credReSHA1.FindStringSubmatch(block); len(m) > 1 && entry.Hash == "" {
			entry.Hash = strings.TrimSpace(m[1])
			entry.Type = "sha1"
		}
		if m := credRePassword.FindStringSubmatch(block); len(m) > 1 {
			pw := strings.TrimSpace(m[1])
			if pw != "(null)" && pw != "" {
				entry.Password = pw
				if entry.Type == "" {
					entry.Type = "cleartext"
				}
			}
		}

		if entry.Username != "" && entry.Username != "(null)" && (entry.Hash != "" || entry.Password != "") {
			entries = append(entries, entry)
		}
	}

	// Pattern 2: SAM hash dump format 鈥?username:rid:lmhash:nthash:::
	for _, line := range strings.Split(raw, "\n") {
		if m := credReSAM.FindStringSubmatch(strings.TrimSpace(line)); len(m) > 4 {
			entries = append(entries, db.CredentialEntry{
				AgentID:  agentID,
				Username: m[1],
				Hash:     m[4],
				Source:   "sam",
				Type:     "ntlm",
				TaskID:   taskID,
			})
		}
	}

	// Pattern 3: Simple domain\user:password or user:password lines
	if len(entries) == 0 {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if m := credReSimple.FindStringSubmatch(line); len(m) > 3 {
				domain := strings.TrimSpace(m[1])
				user := strings.TrimSpace(m[2])
				pw := strings.TrimSpace(m[3])
				if strings.Contains(pw, "/") || strings.Contains(pw, "\\") || len(pw) > 256 {
					continue
				}
				entries = append(entries, db.CredentialEntry{
					AgentID:  agentID,
					Domain:   domain,
					Username: user,
					Password: pw,
					Source:   "manual_parse",
					Type:     "cleartext",
					TaskID:   taskID,
				})
			}
		}
	}

	return entries
}

// handleCredentialsPage renders the credentials vault page (DB-backed)
func (s *Server) handleCredentialsPage(c *gin.Context) {
	var creds []db.CredentialEntry
	query := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).Order("created_at desc")

	tagFilter := c.Query("tag")
	searchQuery := c.Query("search")
	expiryFilter := c.Query("expiry")
	confirmedFilter := c.Query("confirmed")
	agentFilter := c.Query("agent_id")
	lifecycleFilter := c.Query("lifecycle")

	if agentFilter != "" {
		query = query.Where("agent_id = ?", agentFilter)
	}

	if tagFilter != "" {
		query = query.Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(tagFilter)+"%")
	}

	if searchQuery != "" {
		query = query.Where("(domain LIKE ? ESCAPE '\\' OR username LIKE ? ESCAPE '\\' OR notes LIKE ? ESCAPE '\\')",
			"%"+escapeLike(searchQuery)+"%", "%"+escapeLike(searchQuery)+"%", "%"+escapeLike(searchQuery)+"%")
	}

	if expiryFilter != "" {
		// Compare against Go-derived timestamps instead of SQLite
		// datetime('now'): stored times carry the local offset (e.g. +08:00)
		// while datetime('now') is UTC, making naive string comparison
		// timezone-dependent. Passing time.Time parameters keeps both sides
		// on the same storage format.
		now := time.Now()
		weekAhead := now.Add(7 * 24 * time.Hour)
		switch expiryFilter {
		case "expired":
			query = query.Where("expires_at IS NOT NULL AND expires_at < ?", now)
		case "expiring":
			query = query.Where("expires_at IS NOT NULL AND expires_at BETWEEN ? AND ?", now, weekAhead)
		case "valid":
			query = query.Where("expires_at IS NULL OR expires_at > ?", now)
		}
	}

	if confirmedFilter != "" {
		switch confirmedFilter {
		case "true":
			query = query.Where("confirmed = ?", true)
		case "false":
			query = query.Where("confirmed = ?", false)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		handleQueryError(c, err, "Failed to count credentials page")
		return
	}

	limit := 5000
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= limit {
			limit = n
		}
	}

	if err := query.Limit(limit).Find(&creds).Error; err != nil {
		handleQueryError(c, err, "Failed to query credentials page")
		return
	}

	for i := range creds {
		creds[i].Notes = decryptCredNotes(creds[i].Notes)
	}

	// Aggregate latest usage per credential for lifecycle computation.
	credIDs := make([]uint, len(creds))
	for i, c := range creds {
		credIDs[i] = c.ID
	}
	latestUsageMap := make(map[uint]*db.CredentialUsage)
	if len(credIDs) > 0 {
		// One row per credential, not the whole ledger. The page previously
		// read every usage row for up to 5000 credentials into memory and
		// de-duped in Go; credential_usages is append-only and (before the
		// retention sweep) unbounded, so the vault page's memory cost grew
		// with engagement history rather than with the page size.
		//
		// CredentialLifecycle only reads Action and Result, and ROW_NUMBER is
		// the portable way to pick the newest row per group (SQLite >= 3.25 and
		// Postgres both support it; a bare column alongside MAX() would be a
		// SQLite-only extension).
		type latestUsageRow struct {
			CredentialID uint
			Action       string
			Result       string
		}
		ranked := s.db.Model(&db.CredentialUsage{}).
			Select("credential_id, action, result, created_at, ROW_NUMBER() OVER (PARTITION BY credential_id ORDER BY created_at DESC) AS rn").
			Where("credential_id IN ?", credIDs)
		var rows []latestUsageRow
		if err := s.db.Table("(?) AS ranked", ranked).Where("rn = 1").Scan(&rows).Error; err != nil {
			// Swallowing this rendered every credential as "fresh" with no
			// evidence the read failed — a fabricated lifecycle label.
			handleQueryError(c, err, "Failed to aggregate credential usage")
			return
		}
		for i := range rows {
			latestUsageMap[rows[i].CredentialID] = &db.CredentialUsage{
				CredentialID: rows[i].CredentialID,
				Action:       rows[i].Action,
				Result:       rows[i].Result,
			}
		}
	}

	now := time.Now()
	type credWithLifecycle struct {
		db.CredentialEntry
		Lifecycle string `json:"lifecycle"`
	}
	var enriched []credWithLifecycle
	for _, cred := range creds {
		lc := db.CredentialLifecycle(cred, latestUsageMap[cred.ID], now)
		if lifecycleFilter != "" && lc != lifecycleFilter {
			continue
		}
		enriched = append(enriched, credWithLifecycle{cred, lc})
	}

	var allTags []string
	var tagStrings []string
	// Tenant scope: the filter dropdown must not leak other tenants' tag names.
	if err := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).Where("tags != '' AND tags IS NOT NULL").Limit(5000).Pluck("tags", &tagStrings).Error; err != nil {
		slog.Error("Failed to pluck credential tags", "err", err)
		tagStrings = []string{}
	}
	tagSet := make(map[string]int)
	for _, tags := range tagStrings {
		for _, tag := range strings.Split(tags, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tagSet[tag]++
			}
		}
	}
	for tag := range tagSet {
		allTags = append(allTags, tag)
	}

	var credsTasks []db.Task
	if err := s.tenantScope(s.db.Preload("Agent"), c).
		Where("type = ?", "creds").
		Order("created_at desc").Limit(100).Find(&credsTasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query creds tasks")
		return
	}

	var related []db.Task
	if err := s.tenantScope(s.db.Preload("Agent"), c).
		Where("type = ? AND (command LIKE ? OR command LIKE ? OR command LIKE ?)", "shell", "%mimikatz%", "%sekurlsa%", "%lsass%").
		Order("created_at desc").Limit(30).Find(&related).Error; err != nil {
		handleQueryError(c, err, "Failed to query related creds tasks")
		return
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":           "ForgeC2 - Credential Center",
		"ActiveNav":       "credentials",
		"VaultEntries":    enriched,
		"CredsTasks":      credsTasks,
		"RelatedTasks":    related,
		"VaultCount":      len(enriched),
		"Total":           int(total),
		"AllTags":         allTags,
		"TagFilter":       tagFilter,
		"search_query":    searchQuery,
		"ExpiryFilter":    expiryFilter,
		"AgentFilter":     agentFilter,
		"LifecycleFilter": lifecycleFilter,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// exportRateLimit tracks credential export requests per IP to prevent abuse.
var (
	exportRateMu     sync.Mutex
	exportRateMap    = make(map[string][]time.Time)
	exportRateLimit  = 5           // max exports per window
	exportRateWindow = time.Minute // sliding window
)

func checkExportRateLimit(ip string) bool {
	exportRateMu.Lock()
	defer exportRateMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-exportRateWindow)
	// Prune old entries
	timestamps := exportRateMap[ip]
	valid := timestamps[:0]
	for _, t := range timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= exportRateLimit {
		exportRateMap[ip] = valid
		return false
	}
	exportRateMap[ip] = append(valid, now)
	// Amortized cleanup: prune IPs whose entries are all expired so the
	// map cannot grow unbounded with abandoned addresses.
	if len(exportRateMap) > 1024 && len(exportRateMap)%64 == 0 {
		for k, ts := range exportRateMap {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(exportRateMap, k)
			}
		}
	}
	return true
}

func (s *Server) handleExportCredentials(c *gin.Context) {
	if !s.requireExportScope(c, "credential_export", "credential") {
		return
	}
	if !checkExportRateLimit(c.ClientIP()) {
		respondError(c, http.StatusTooManyRequests, "export rate limit exceeded, try again later")
		return
	}

	s.LogAuditRecord(c, "credential_export", "credential", "", "credentials exported as CSV", true, nil)

	var creds []db.CredentialEntry
	query := s.tenantScope(s.db.WithContext(s.ctx), c).Order("created_at desc").Limit(5000)

	tagFilter := c.Query("tag")
	expiryFilter := c.Query("expiry")

	if tagFilter != "" {
		query = query.Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(tagFilter)+"%")
	}

	if expiryFilter != "" {
		now := time.Now()
		weekAhead := now.Add(7 * 24 * time.Hour)
		switch expiryFilter {
		case "expired":
			query = query.Where("expires_at IS NOT NULL AND expires_at < ?", now)
		case "expiring":
			query = query.Where("expires_at IS NOT NULL AND expires_at BETWEEN ? AND ?", now, weekAhead)
		case "valid":
			query = query.Where("expires_at IS NULL OR expires_at > ?", now)
		}
	}

	if err := query.Limit(5000).Find(&creds).Error; err != nil {
		// Exporting an empty CSV as if it were the full vault is the worst
		// failure mode here: an operator would archive "no credentials".
		slog.Error("Failed to query credentials for export", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to load credentials for export")
		return
	}

	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", "attachment; filename=credentials.csv")

	w := csv.NewWriter(c.Writer)
	w.Write([]string{"ID", "AgentID", "Domain", "Username", "Password", "Hash", "Source", "Type", "Tags", "ExpiresAt", "Confirmed", "Created"})
	for _, e := range creds {
		w.Write([]string{
			strconv.FormatUint(uint64(e.ID), 10),
			csvSanitize(e.AgentID), csvSanitize(e.Domain), csvSanitize(e.Username),
			csvSanitize(e.Password), csvSanitize(e.Hash),
			csvSanitize(e.Source), csvSanitize(e.Type), csvSanitize(e.Tags),
			e.ExpiresAt.Format("2006-01-02 15:04:05"),
			strconv.FormatBool(e.Confirmed),
			e.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
}

func (s *Server) handleAddCredential(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	entry := db.CredentialEntry{
		TenantID: s.currentTenantID(c),
		AgentID:  c.PostForm("agent_id"),
		Domain:   c.PostForm("domain"),
		Username: c.PostForm("username"),
		Password: c.PostForm("password"),
		Hash:     c.PostForm("hash"),
		Source:   "manual",
		Type:     c.PostForm("type"),
		Notes:    c.PostForm("notes"),
	}
	if entry.AgentID != "" && entry.TenantID != 0 {
		var visible int64
		if err := s.db.Model(&db.Implant{}).Where("id = ? AND tenant_id = ?", entry.AgentID, entry.TenantID).Count(&visible).Error; err != nil || visible == 0 {
			respondError(c, http.StatusBadRequest, "agent is not visible to this tenant")
			return
		}
	}
	if entry.Type == "" {
		if entry.Hash != "" {
			entry.Type = "ntlm"
		} else {
			entry.Type = "cleartext"
		}
	}
	if err := s.db.Create(&entry).Error; err != nil {
		slog.Error("Failed to create credential entry", "error", err)
		s.LogAuditRecord(c, "credential_add", "credential", entry.AgentID, "add credential failed", false, err)
		respondError(c, http.StatusInternalServerError, "failed to add credential")
		return
	}
	s.LogAuditRecord(c, "credential_add", "credential", entry.AgentID,
		"added credential (domain="+entry.Domain+", user="+entry.Username+", type="+entry.Type+")", true, nil)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":     "credential_update",
		"action":   "added",
		"id":       entry.ID,
		"agent_id": entry.AgentID,
	})
	respond(c, gin.H{"id": entry.ID})
}

func (s *Server) handleGetCredential(c *gin.Context) {
	idStr := c.Param("cred_id")
	if _, err := strconv.ParseUint(idStr, 10, 32); err != nil {
		respondError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var cred db.CredentialEntry
	if !s.findTenantOrFail(c, &cred, idStr, "credential") {
		return
	}
	cred.Notes = decryptCredNotes(cred.Notes)
	s.LogAuditRecord(c, "credential_view", "credential", cred.AgentID,
		"viewed credential id="+c.Param("cred_id")+" (domain="+cred.Domain+", user="+cred.Username+")", true, nil)
	respond(c, cred)
}

func (s *Server) handleDeleteCredential(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	idStr := c.Param("cred_id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid id")
		return
	}
	// Tenant gate: the delete below is by raw id, so verify ownership first —
	// otherwise any tenant-scoped operator could delete any tenant's vault row.
	var owned db.CredentialEntry
	if !s.findTenantOrFail(c, &owned, idStr, "credential") {
		return
	}
	// Delete atomically with the usage-ledger rows: CredentialUsage has no FK
	// cascade, so entries previously accumulated dangling references forever.
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("credential_id = ?", id).Delete(&db.CredentialUsage{}).Error; err != nil {
			return err
		}
		return tx.Delete(&db.CredentialEntry{}, id).Error
	}); err != nil {
		slog.Error("Failed to delete credential", "id", id, "error", err)
		s.LogAuditRecord(c, "credential_delete", "credential", "", "delete credential id="+idStr+" failed", false, err)
		respondError(c, http.StatusInternalServerError, "failed to delete")
		return
	}
	s.LogAuditRecord(c, "credential_delete", "credential", "", "deleted credential id="+idStr, true, nil)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":   "credential_update",
		"action": "deleted",
		"id":     id,
	})
	respond(c, gin.H{})
}

func (s *Server) handleUpdateCredential(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	idStr := c.Param("cred_id")
	if _, err := strconv.ParseUint(idStr, 10, 32); err != nil {
		respondError(c, http.StatusBadRequest, "invalid id")
		return
	}

	var cred db.CredentialEntry
	if !s.findTenantOrFail(c, &cred, idStr, "credential") {
		return
	}

	if tags := c.PostForm("tags"); tags != "" {
		cred.Tags = tags
	}
	if expiresAt := c.PostForm("expires_at"); expiresAt != "" {
		t, err := time.Parse("2006-01-02", expiresAt)
		if err == nil {
			cred.ExpiresAt = t
		}
	}
	if confirmed := c.PostForm("confirmed"); confirmed != "" {
		cred.Confirmed = confirmed == "true"
	}

	if err := s.db.Save(&cred).Error; err != nil {
		slog.Error("Failed to update credential", "id", cred.ID, "error", err)
		s.LogAuditRecord(c, "credential_update", "credential", cred.AgentID, "update credential id="+idStr+" failed", false, err)
		respondError(c, http.StatusInternalServerError, "failed to update")
		return
	}
	s.LogAuditRecord(c, "credential_update", "credential", cred.AgentID, "updated credential id="+idStr, true, nil)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":     "credential_update",
		"action":   "updated",
		"id":       cred.ID,
		"agent_id": cred.AgentID,
	})
	respond(c, gin.H{})
}

// handleBatchVerifyCredentials queues cred_check tasks for the selected vault
// entries, grouped by harvesting agent. Each entry with a stored password gets
// one validation task; results flow back through parseAndStoreCredCheckResult
// which flips VerifyStatus to valid/invalid and stamps LastVerifiedAt.
// POST /credentials/batch/verify {ids: [1,2,3]}
// verifiableAgentIDs returns the distinct agent IDs of the entries that would
// reach the agent-existence check: it must apply the same skips the handler
// applies (no password at all, hash-only, or no agent), or the batched lookup
// would query agents whose entries never needed them.
func verifiableAgentIDs(entries []db.CredentialEntry) []string {
	seen := make(map[string]struct{}, len(entries))
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Password == "" || e.AgentID == "" {
			continue
		}
		if _, dup := seen[e.AgentID]; dup {
			continue
		}
		seen[e.AgentID] = struct{}{}
		ids = append(ids, e.AgentID)
	}
	return ids
}

// visibleAgentIDs returns the subset of ids the caller's tenant can reach.
// Same query and same tenant scope as the per-row First it replaces, so a
// foreign agent is just as unreachable here.
func (s *Server) visibleAgentIDs(c *gin.Context, ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return out
	}
	var found []string
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).
		Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
		// Fail closed: a failed lookup must read as "no visible agents", never
		// as "all visible", or the gate inverts into a bypass.
		slog.Warn("cred_check agent visibility lookup failed", "agents", len(ids), "err", err)
		return out
	}
	for _, id := range found {
		out[id] = true
	}
	return out
}

func (s *Server) handleBatchVerifyCredentials(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		respondError(c, http.StatusBadRequest, "ids required")
		return
	}
	if len(req.IDs) > 100 {
		req.IDs = req.IDs[:100]
	}

	var entries []db.CredentialEntry
	// Tenant scope: verify must not queue cred_check tasks (embedding another
	// tenant's cleartext password) against other tenants' vault rows.
	if err := s.tenantScope(s.db, c).Where("id IN ?", req.IDs).Find(&entries).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}

	type verifyOutcome struct {
		ID      uint   `json:"id"`
		Status  string `json:"status"` // queued, skipped_no_password, skipped_hash_only, skipped_no_agent, skipped_agent_gone, skipped_fuse, error
		TaskID  uint   `json:"task_id,omitempty"`
		AgentID string `json:"agent_id,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	outcomes := make([]verifyOutcome, 0, len(entries))
	queued := 0

	// Tenant gate, batched: entries surviving the scoped query above always
	// belong to visible agents, but each one is still re-verified — a cred_check
	// carries the vault password in its command, so it must never be queued
	// against a foreign agent. This was a SELECT per entry (up to 100 per
	// request); resolving the whole set once keeps the same predicate and the
	// same tenant scope.
	reachable := s.visibleAgentIDs(c, verifiableAgentIDs(entries))

	for _, entry := range entries {
		out := verifyOutcome{ID: entry.ID, AgentID: entry.AgentID}
		switch {
		case entry.Password == "" && entry.Hash == "":
			out.Status = "skipped_no_password"
			outcomes = append(outcomes, out)
			continue
		case entry.Password == "":
			// NTLM hashes cannot be validated by runas-style cred_check; only
			// cleartext passwords are verifiable by the agent handler.
			out.Status = "skipped_hash_only"
			outcomes = append(outcomes, out)
			continue
		case entry.AgentID == "":
			out.Status = "skipped_no_agent"
			outcomes = append(outcomes, out)
			continue
		}
		if !reachable[entry.AgentID] {
			out.Status = "skipped_agent_gone"
			outcomes = append(outcomes, out)
			continue
		}
		if s.credCheckFuse.tripped(entry.AgentID, entry.Domain) {
			out.Status = "skipped_fuse"
			outcomes = append(outcomes, out)
			continue
		}

		// The implant splits this on "|". A stored password containing one (legal,
		// and common in generated passwords) shifts every later field, so the
		// check would run against the wrong account or domain — and the fuse that
		// prevents lockouts keys on the domain the SERVER thinks it is checking.
		// Skip the entry visibly instead of queueing a task that means something
		// else.
		if credentialCommandHasSeparator(entry.Username, entry.Domain, entry.Password) {
			out.Status = "skipped_separator"
			out.Error = "value contains the credential separator (|)"
			outcomes = append(outcomes, out)
			continue
		}
		cmd := entry.Username + "|" + entry.Domain + "|" + entry.Password + "|"
		task, err := s.createTask(entry.AgentID, "cred_check", cmd, "", "", "", 0, 0, callerOpts(c)...)
		if err != nil {
			out.Status = "error"
			out.Error = sanitizeError(err, "Credential operation")
			outcomes = append(outcomes, out)
			continue
		}
		// Queued checks were invisible on WS until the next poll — broadcast
		// like every other creation path so the UI updates immediately.
		s.broadcastTaskUpdate(entry.AgentID, *task)
		now := time.Now()
		s.db.Model(&db.CredentialEntry{}).Where("id = ?", entry.ID).Updates(map[string]interface{}{
			"verify_status":    "pending",
			"last_verified_at": &now,
		})
		out.Status = "queued"
		out.TaskID = task.ID
		outcomes = append(outcomes, out)
		queued++

		s.LogAuditRecord(nil, "cred_check", "credential", entry.AgentID,
			fmt.Sprintf("batch credential check queued: %s@%s", entry.Username, entry.Domain), true, nil)
	}

	slog.Info("Batch credential verification", "requested", len(req.IDs), "queued", queued)
	respond(c, gin.H{
		"queued":  queued,
		"results": outcomes,
	})
}

func (s *Server) handleBatchAddTags(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		IDs  []uint   `json:"ids"`
		Tags []string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	if len(req.IDs) == 0 || len(req.Tags) == 0 {
		respondError(c, http.StatusBadRequest, "no ids or tags provided")
		return
	}

	newTags := strings.Join(req.Tags, ",")

	// Tenant scope: blind UPDATE by ids would otherwise retag other tenants' rows.
	if err := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).
		Where("id IN ?", req.IDs).
		Update("tags", gorm.Expr("CASE WHEN tags = '' OR tags IS NULL THEN ? ELSE tags || ',' || ? END", newTags, newTags)).Error; err != nil {
		slog.Error("Failed to batch add tags", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to update tags")
		return
	}

	s.LogAuditRecord(c, "credential_tag", "credential", "", "added tags to "+strconv.Itoa(len(req.IDs))+" credentials", true, nil)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":   "credential_update",
		"action": "updated",
		"count":  len(req.IDs),
	})
	respond(c, gin.H{"count": len(req.IDs)})
}

func (s *Server) handleToggleConfirmed(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	idStr := c.Param("cred_id")
	if _, err := strconv.ParseUint(idStr, 10, 32); err != nil {
		respondError(c, http.StatusBadRequest, "invalid id")
		return
	}

	var cred db.CredentialEntry
	if !s.findTenantOrFail(c, &cred, idStr, "credential") {
		return
	}

	cred.Confirmed = !cred.Confirmed

	if err := s.db.Save(&cred).Error; err != nil {
		slog.Error("Failed to toggle credential confirm", "id", cred.ID, "error", err)
		respondError(c, http.StatusInternalServerError, "failed to update")
		return
	}
	s.LogAuditRecord(c, "credential_confirm", "credential", cred.AgentID,
		"set confirmed="+strconv.FormatBool(cred.Confirmed)+" for credential id="+idStr, true, nil)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":     "credential_update",
		"action":   "updated",
		"id":       cred.ID,
		"agent_id": cred.AgentID,
	})
	respond(c, gin.H{"confirmed": cred.Confirmed})
}

// RecordUsage appends an entry to the credential usage ledger. It is safe to
// call with a zero credentialID (no-op).
func (s *Server) RecordUsage(credentialID uint, taskID uint, agentID, action, result, detail, operator string) {
	if credentialID == 0 {
		return
	}
	entry := db.CredentialUsage{
		CredentialID: credentialID,
		TaskID:       taskID,
		AgentID:      agentID,
		Action:       action,
		Result:       result,
		Detail:       detail,
		Operator:     operator,
	}
	if err := s.db.Create(&entry).Error; err != nil {
		slog.Error("Failed to record credential usage", "credential_id", credentialID, "action", action, "error", err)
	}
}

// recordUsageBatch appends one ledger row per credential in a single batched
// insert instead of N individually-committed inserts on the single-writer
// pool. Rows without an ID yet (create failed) are skipped.
func (s *Server) recordUsageBatch(entries []db.CredentialEntry, taskID uint, agentID, action, result string) {
	usages := make([]db.CredentialUsage, 0, len(entries))
	for _, e := range entries {
		if e.ID == 0 {
			continue
		}
		usages = append(usages, db.CredentialUsage{
			CredentialID: e.ID,
			TaskID:       taskID,
			AgentID:      agentID,
			Action:       action,
			Result:       result,
		})
	}
	if len(usages) == 0 {
		return
	}
	if err := s.db.CreateInBatches(usages, 50).Error; err != nil {
		slog.Error("Failed to record credential usage batch", "count", len(usages), "error", err)
	}
}

// apiRecordUsage handles POST /api/credentials/:id/usage — manual usage
// recording by an operator.
func (s *Server) apiRecordUsage(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	idStr := c.Param("cred_id")
	if _, err := strconv.ParseUint(idStr, 10, 64); err != nil {
		respondError(c, http.StatusBadRequest, "invalid credential id")
		return
	}
	action := strings.TrimSpace(c.PostForm("action"))
	detail := strings.TrimSpace(c.PostForm("detail"))
	if action == "" {
		action = "manual"
	}
	// Tenant gate: the usage ledger feeds verified/used/stale lifecycle, so a
	// foreign vault row must not be writable here.
	var cred db.CredentialEntry
	if !s.findTenantOrFail(c, &cred, idStr, "credential") {
		return
	}
	username, _ := c.Get("user")
	operator, _ := username.(string)
	s.RecordUsage(cred.ID, 0, cred.AgentID, action, "ok", detail, operator)
	s.broadcastOperatorEvent(map[string]interface{}{
		"type":     "credential_update",
		"action":   "used",
		"id":       cred.ID,
		"agent_id": cred.AgentID,
	})
	respond(c, gin.H{})
}
