package server

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The operations log is a curated view of the audit chain: operator actions
// grouped by the thing they touched, with a stable action catalogue, date
// ranges and a server-side export. The raw table stays available on "All
// events"; this file only adds read-side filtering and presentation metadata.
//
// Categories live here rather than in the browser because the filter and the
// catalogue must agree. A frontend copy would drift the first time an action
// is renamed, and a category that filters nothing is worse than no category.

// auditCategoryRule classifies an action by exact name or prefix. Prefixes are
// lower-case; `operator:` is stripped before matching because LogOperatorAction
// stores structured actions under that namespace.
type auditCategoryRule struct {
	ID       string
	Exact    []string
	Prefixes []string
}

// auditCategoryRules is ordered: the first match wins, so narrower prefixes
// (credential_, opsec_rule_) must come before broader ones (cred_, opsec_).
//
// "system" is beacon/transport traffic rather than an operator operation. The
// operations tab excludes it by default; the category remains selectable so an
// investigator can still ask for it, and "All events" is unaffected.
var auditCategoryRules = []auditCategoryRule{
	{
		ID: "auth",
		Exact: []string{
			"login", "login_failed", "login_new_ip", "logout",
			"password_change", "password_change_sessions_revoked",
		},
		Prefixes: []string{"user_", "session_", "api_key_", "role_", "mfa_", "totp_", "auth_"},
	},
	{
		ID: "credentials",
		Exact: []string{
			"cred_check", "password_spray", "wifi_credential_extraction", "vpn_creds",
			"mimikatz", "powerpick", "credential_view", "credential_export",
		},
		Prefixes: []string{"credential_", "cred_", "loot_", "hash_", "cookie_", "token_"},
	},
	{
		ID: "files",
		Exact: []string{
			"bof", "deploy_module", "delete_module", "delete_script", "save_script",
			"upload_cert", "generate_delivery",
		},
		Prefixes: []string{"file_", "bof_", "script_", "plugin_", "packer_", "module_", "upload_", "download_", "generate_", "stager_"},
	},
	{
		ID:       "infrastructure",
		Exact:    []string{"regenerate_cert"},
		Prefixes: []string{"listener_", "dns_", "redirector_", "extc2_", "relay_", "cert_", "tls_", "mesh_", "socks_", "rportfwd_", "lportfwd_"},
	},
	{
		ID:       "automation",
		Exact:    []string{"create_integration", "update_integration", "delete_integration", "launch_phishing_campaign"},
		Prefixes: []string{"automation_", "workflow_", "campaign_", "phishing_", "notification_", "siem_", "alert_", "opsec_rule_", "playbook_run", "integration"},
	},
	{
		ID:       "ai",
		Prefixes: []string{"ai_"},
	},
	{
		ID:       "tenants",
		Prefixes: []string{"tenant_"},
	},
	{
		ID: "agents",
		Exact: []string{
			"agent", "agent_action", "delete_agent", "kill_agent", "link_agent",
			"unlink_agent", "agent_elevated", "elevate_printnightmare",
		},
		Prefixes: []string{"agent_", "webrtc_agent"},
	},
	{
		ID: "tasks",
		Exact: []string{
			"view_tasks", "cancel_task", "command_result", "execute_script",
			"execute_assembly", "approve_task", "reject_task", "batch_command",
			"batch_delete_agents", "batch_sleep_clamped", "tasks_rerun_by_signature",
			"task_export", "timeline_export", "screenshot_request", "request_hostinfo",
			"remote_desktop_open", "remote_desktop_close", "remote_input",
			"screen_monitor_start", "screen_monitor_stop", "run_evasion",
			"spawn", "lateral", "privesc_check", "privilege_escalation_check",
			"uac_bypass", "persistence_add", "persistence_list", "persistence_remove",
			"scan_export", "task_auto_retry", "traffic_adapt", "traffic_auto_adapt",
		},
		Prefixes: []string{"task_", "tasks_", "command_", "shell_", "oneshot_", "approval_", "batch_", "macro_", "remote_", "screen_", "amsi_", "etw_", "opsec_", "sandbox_"},
	},
	{
		ID: "settings",
		Exact: []string{
			"settings_change", "hot_update", "hot_update_result", "push_update_key",
			"self_update", "emergency_stop", "kill_switch_arm", "kill_switch_disarm",
			"audit_purge", "audit_chain_verify", "audit_chain_tamper",
			"audit_chain_clear", "audit_chain_set", "db_backup",
			"db_maintenance_unsupported", "report_export", "handover_export",
			"migrate", "config_reload", "config_self_audit",
		},
		Prefixes: []string{"config_", "setting", "backup_", "maintenance_", "export_", "purge_", "audit_", "engagement_", "consent_", "device_code_", "observability_"},
	},
	{
		ID: "system",
		Exact: []string{
			"connect", "close", "data", "probe", "old_probe", "recent_probe",
			"verify", "view", "open", "external_append", "api_access",
			"seq_lockout", "relay_depth_exceeded", "relay_orphan_reap",
			"blocked_agent_checkin", "before_worker", "after_external",
			"tls_cert_expired", "tls_cert_expiring", "session_evict",
			"circuit_breaker_reset",
		},
		Prefixes: []string{"udp_", "tun_", "implant_", "beacon_"},
	},
}

// auditCategoryOrder is the stable order the UI renders categories in. "other"
// is the fallback classifier bucket; the rest are deliberate groupings.
var auditCategoryOrder = []string{
	"auth", "agents", "tasks", "credentials", "files",
	"infrastructure", "automation", "ai", "tenants", "settings", "system", "other",
}

func auditCategoryKnown(id string) bool {
	for _, v := range auditCategoryOrder {
		if v == id {
			return true
		}
	}
	return false
}

// auditActionCategory returns the operator-facing category for an action.
// `operator:` is stripped: LogOperatorAction stores "operator:agent_delete",
// and both spellings must classify the same way.
func auditActionCategory(action string) string {
	a := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(action)), "operator:")
	for _, rule := range auditCategoryRules {
		for _, exact := range rule.Exact {
			if a == exact {
				return rule.ID
			}
		}
		for _, prefix := range rule.Prefixes {
			if strings.HasPrefix(a, prefix) {
				return rule.ID
			}
		}
	}
	return "other"
}

// auditCategoryCondition builds the SQL for one category. Both the raw action
// and its `operator:` form are matched, because the middleware logs coarse
// actions unprefixed while structured operator actions carry the prefix.
func auditCategoryCondition(id string) (string, []any, bool) {
	if !auditCategoryKnown(id) {
		return "", nil, false
	}
	if id == "other" {
		// "other" is the classifier's fallback: everything none of the
		// explicit rules match. Build the negation from the same rules rather
		// than maintaining a second list of names.
		var parts []string
		var args []any
		for _, rule := range auditCategoryRules {
			if cond, ruleArgs, ok := auditCategoryRuleCondition(rule); ok {
				parts = append(parts, cond)
				args = append(args, ruleArgs...)
			}
		}
		if len(parts) == 0 {
			return "", nil, true
		}
		return "NOT (" + strings.Join(parts, " OR ") + ")", args, true
	}
	for _, rule := range auditCategoryRules {
		if rule.ID == id {
			return auditCategoryRuleCondition(rule)
		}
	}
	return "", nil, false
}

// auditCategoryRuleCondition turns one classifier rule into SQL. A rule with
// no names would match everything (NOT nothing), so it is reported as absent
// rather than silently widening the selection.
func auditCategoryRuleCondition(rule auditCategoryRule) (string, []any, bool) {
	conds := make([]string, 0, len(rule.Exact)*2+len(rule.Prefixes)*2)
	args := make([]any, 0, len(rule.Exact)*2+len(rule.Prefixes)*2)
	for _, exact := range rule.Exact {
		conds = append(conds, "action = ?", "action = ?")
		args = append(args, exact, "operator:"+exact)
	}
	for _, prefix := range rule.Prefixes {
		conds = append(conds, "action LIKE ? ESCAPE '\\'", "action LIKE ? ESCAPE '\\'")
		args = append(args, escapeLike(prefix)+"%", "operator:"+escapeLike(prefix)+"%")
	}
	if len(conds) == 0 {
		return "", nil, false
	}
	return "(" + strings.Join(conds, " OR ") + ")", args, true
}

// auditTimeBounds applies the shared time window (since / until) to a query.
//
// The audit list, its CSV export and the action catalogue must describe the
// same window: a catalogue count taken over all time while the list shows
// the last seven days is a number the page cannot honour, and an operator
// comparing the two concludes the list is broken.
func auditTimeBounds(c *gin.Context, query *gorm.DB) (*gorm.DB, string) {
	var sinceAt, untilAt time.Time
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		at, ok := parseTimeBound(raw, time.Now())
		if !ok {
			return nil, formatTimeBoundError("since")
		}
		sinceAt = at
		query = query.Where("created_at >= ?", comparableLocalTime(at))
	}
	if raw := strings.TrimSpace(c.Query("until")); raw != "" {
		at, ok := parseTimeBound(raw, time.Now())
		if !ok {
			return nil, formatTimeBoundError("until")
		}
		untilAt = at
		query = query.Where("created_at <= ?", comparableLocalTime(at))
	}
	if !sinceAt.IsZero() && !untilAt.IsZero() && sinceAt.After(untilAt) {
		return nil, "since must not be after until"
	}
	return query, ""
}

// auditLogQuery applies every operations-log filter to one query. The list and
// the CSV export share it, so "what I see" and "what I exported" cannot be two
// different selections.
//
// Returns a user-facing rejection message for an invalid bound; the caller
// answers 400. A nil query is never returned with an empty message.
func (s *Server) auditLogQuery(c *gin.Context) (*gorm.DB, string) {
	query := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).Order("created_at DESC")

	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + escapeLike(search) + "%"
		query = query.Where(
			`("user" LIKE ? ESCAPE '\' OR action LIKE ? ESCAPE '\' OR resource LIKE ? ESCAPE '\' OR details LIKE ? ESCAPE '\' OR agent_id LIKE ? ESCAPE '\' OR error LIKE ? ESCAPE '\')`,
			like, like, like, like, like, like,
		)
	}
	match := strings.ToLower(strings.TrimSpace(c.Query("match")))
	if match != "" && match != "exact" && match != "contains" {
		return nil, `invalid match (expected "exact" or "contains")`
	}
	if action := strings.TrimSpace(c.Query("action")); action != "" {
		if match == "contains" {
			// The events tab's verb filter (create/update/delete/...) names a
			// class, while rows store specific actions; a substring match makes
			// the label true. The `operator:` form carries the same substring,
			// so it needs no special case here.
			query = query.Where("action LIKE ? ESCAPE '\\'", "%"+escapeLike(action)+"%")
		} else {
			// Accept the catalogue's raw value and the unprefixed spelling an
			// operator may type by hand.
			query = query.Where("(action = ? OR action = ?)", action, "operator:"+strings.TrimPrefix(action, "operator:"))
		}
	}
	if user := strings.TrimSpace(c.Query("user")); user != "" {
		query = query.Where(`"user" = ?`, user)
	}
	if agentID := strings.TrimSpace(c.Query("agent_id")); agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	if host := strings.TrimSpace(c.Query("host")); host != "" {
		// The same host vocabulary the fleet list groups by ("h:<name>",
		// "ip:<address>", "id:<beacon>"): one machine can carry several
		// sessions, and the operator asking "what happened on this host?"
		// means all of them.
		implants := s.tenantScope(s.db.Model(&db.Implant{}), c).Select("id")
		switch {
		case strings.HasPrefix(host, "h:"):
			implants = implants.Where("LOWER(hostname) = ?", strings.TrimPrefix(host, "h:"))
		case strings.HasPrefix(host, "ip:"):
			addr := strings.TrimPrefix(host, "ip:")
			implants = implants.Where("ip = ? OR public_ip = ?", addr, addr)
		case strings.HasPrefix(host, "id:"):
			implants = implants.Where("id = ?", strings.TrimPrefix(host, "id:"))
		default:
			// A bare hostname or address: match either.
			implants = implants.Where("LOWER(hostname) = ? OR ip = ? OR public_ip = ?", strings.ToLower(host), host, host)
		}
		query = query.Where("agent_id IN (?)", implants)
	}
	if result := strings.ToLower(strings.TrimSpace(c.Query("result"))); result != "" {
		switch result {
		case "success", "ok":
			query = query.Where("success = ?", true)
		case "failed", "failure", "error":
			query = query.Where("success = ?", false)
		default:
			return nil, `invalid result (expected "success" or "failed")`
		}
	}
	if category := strings.ToLower(strings.TrimSpace(c.Query("category"))); category != "" {
		cond, args, ok := auditCategoryCondition(category)
		if !ok {
			return nil, fmt.Sprintf("invalid category %q", category)
		}
		query = query.Where(cond, args...)
	}
	if scope := strings.ToLower(strings.TrimSpace(c.Query("scope"))); scope != "" && scope != "all" {
		if scope != "operations" {
			return nil, `invalid scope (expected "operations" or "all")`
		}
		// A category selection is explicit, so it wins over the default scope
		// exclusion: picking "System events" must return them.
		if strings.TrimSpace(c.Query("category")) == "" {
			if cond, args, ok := auditCategoryCondition("system"); ok {
				query = query.Where("NOT "+cond, args...)
			}
		}
	}

	// The scope exclusion is skipped when a category is explicit (see above),
	// so the window is the last filter the list applies.
	return auditTimeBounds(c, query)
}

// auditActionCount is one row of the catalogue: what the action is called, the
// category it belongs to and how often it occurred. The browser uses it for
// both dropdowns, so it never hard-codes the action list.
type auditActionCount struct {
	Action   string `json:"action"`
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

// handleGetAuditActions returns the distinct actions in the caller's tenant,
// with counts. It is a read-only catalogue for the operations filters; the
// limit keeps a pathological audit table from producing an unbounded dropdown.
//
// The counts honour the same time window as the list (since / until), so the
// number next to a category is the number of rows that category shows. The
// scope exclusion is deliberately NOT applied: the catalogue covers every
// category, so the system events stay reachable - picking one is an explicit
// choice that overrides the default exclusion on the list.
func (s *Server) handleGetAuditActions(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusServiceUnavailable, "database is not initialized")
		return
	}
	base := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c)
	query, rejection := auditTimeBounds(c, base)
	if rejection != "" {
		respondError(c, http.StatusBadRequest, rejection)
		return
	}
	var rows []struct {
		Action string
		Count  int64
	}
	if err := query.
		Select("action, COUNT(*) AS count").
		Group("action").
		Order("count DESC, action ASC").
		Limit(500).
		Scan(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read the audit action catalogue")
		return
	}

	actions := make([]auditActionCount, 0, len(rows))
	counts := make(map[string]int64, len(auditCategoryOrder))
	for _, row := range rows {
		category := auditActionCategory(row.Action)
		counts[category] += row.Count
		actions = append(actions, auditActionCount{Action: row.Action, Category: category, Count: row.Count})
	}
	categories := make([]gin.H, 0, len(auditCategoryOrder))
	for _, id := range auditCategoryOrder {
		categories = append(categories, gin.H{"id": id, "count": counts[id]})
	}
	respond(c, gin.H{"actions": actions, "categories": categories})
}

// safeCSVCell protects an exported cell from spreadsheet formula injection and
// quotes it the way encoding/csv expects. The audit table can contain
// attacker-influenced text (a username, a beacon hostname), and an export is
// routinely opened in Excel.
func safeCSVCell(value string) string {
	text := strings.TrimLeft(value, " \t\r\n")
	if strings.HasPrefix(text, "=") || strings.HasPrefix(text, "+") ||
		strings.HasPrefix(text, "-") || strings.HasPrefix(text, "@") ||
		strings.HasPrefix(text, "\t") || strings.HasPrefix(text, "\r") {
		return "'" + value
	}
	return value
}

// auditLogItem is one audit row for the console: the chain fields plus the
// name of the beacon it belongs to.
//
// "Which host?" is the first question about an operation, and the agent id
// alone does not answer it - an operator reading the audit log would have to
// look every id up in the fleet list.
type auditLogItem struct {
	db.AuditLog
	AgentHostname string `json:"agent_hostname,omitempty"`
}

// agentHostnames resolves the beacon names of the given ids.
//
// One bounded query over the page's beacons: the audit rows carry an id, and
// the console needs the name next to it.
func (s *Server) agentHostnames(c *gin.Context, ids []string) map[string]string {
	names := map[string]string{}
	if len(ids) == 0 {
		return names
	}
	type row struct {
		ID       string
		Hostname string
	}
	var rows []row
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).
		Select("id, hostname").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		// Advisory: the name is a convenience, the rows must still load.
		slog.Warn("Audit agent names unavailable", "err", err)
		return names
	}
	for _, r := range rows {
		if r.Hostname != "" {
			names[r.ID] = r.Hostname
		}
	}
	return names
}

// handleExportAuditLogsCSV streams the filtered operations log as CSV. The
// page-level browser export only covers the visible 50 rows; an investigation
// or a compliance handover needs every matching row, which is why this is
// server-side. The row cap is published in a header rather than silently
// truncating.
func (s *Server) handleExportAuditLogsCSV(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusServiceUnavailable, "database is not initialized")
		return
	}
	query, rejection := s.auditLogQuery(c)
	if rejection != "" {
		respondError(c, http.StatusBadRequest, rejection)
		return
	}
	limit := atoiDefault(c.Query("limit"), 50000)
	if limit <= 0 || limit > 200000 {
		limit = 50000
	}

	var rows []db.AuditLog
	if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read the audit log")
		return
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="audit-operations-%s.csv"`, time.Now().Format("20060102-150405")))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Audit-Export-Rows", strconv.Itoa(len(rows)))
	if truncated {
		c.Header("X-Audit-Export-Truncated", "true")
	}

	// UTF-8 BOM: Excel needs it to read Chinese usernames and regions instead
	// of mojibake. A verifier reading the file as bytes only sees the BOM at
	// the start of the header line.
	if _, err := c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"Time", "User", "IP", "Action", "Category", "Resource", "Target", "Host", "Result", "Details"})
	// The beacon names of the exported rows: the file has to answer "which
	// host?" as well as the screen does. The distinct ids are bounded by the
	// fleet, not by the row count.
	hostnames := s.agentHostnames(c, auditAgentIDs(rows))
	for i := range rows {
		row := &rows[i]
		result := "success"
		if !row.Success {
			result = "failed"
		}
		details := row.Details
		if row.Error != "" {
			if details != "" {
				details += " | "
			}
			details += row.Error
		}
		_ = w.Write([]string{
			row.CreatedAt.Format(time.RFC3339),
			safeCSVCell(row.User),
			safeCSVCell(row.IP),
			safeCSVCell(row.Action),
			auditActionCategory(row.Action),
			safeCSVCell(row.Resource),
			safeCSVCell(row.AgentID),
			safeCSVCell(hostnames[row.AgentID]),
			result,
			safeCSVCell(details),
		})
	}
	w.Flush()
}
