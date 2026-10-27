package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// ── Shared truncation ─────────────────────────────────────────────────────

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return "..."
	}
	// Back off to a valid rune boundary so multi-byte UTF-8 (Chinese
	// hostnames, CJK task output) is never sliced mid-sequence — encoding/
	// json would otherwise emit U+FFFD mojibake for the tail.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// ── Situation snapshot (injected as live system context) ──────────────────

type aiSituation struct {
	AgentsTotal     int64               `json:"agents_total"`
	AgentsOnline    int64               `json:"agents_online"`
	AgentsOffline   int64               `json:"agents_offline"`
	ElevatedOnline  int64               `json:"elevated_online"`
	StaleOnline     int64               `json:"stale_online"`
	OnlineOS        map[string]int64    `json:"online_os"`
	ListenersActive int64               `json:"listeners_active"`
	PendingApproval int64               `json:"pending_approval"`
	AIPending       int64               `json:"ai_pending_approval"`
	ActiveAlerts    int64               `json:"active_alerts"`
	Credentials     int64               `json:"credentials"`
	OperatorsOnline int                 `json:"operators_online"`
	RecentAgents    []map[string]string `json:"recent_agents"`
}

func (s *Server) collectSituation(reqCtx *aiReqCtx) aiSituation {
	out := aiSituation{OnlineOS: map[string]int64{}, RecentAgents: []map[string]string{}}
	if s.db == nil {
		return out
	}
	scoped := reqCtx != nil && reqCtx.Principal.UserID != 0
	agents := func() *gorm.DB {
		query := s.db.Model(&db.Implant{})
		if scoped {
			query = query.Where("tenant_id = ?", reqCtx.Principal.TenantID)
		}
		return query
	}
	hasPermission := func(permission string) bool {
		return !scoped || reqCtx.Principal.hasPermission(s.db, permission)
	}

	agents().Count(&out.AgentsTotal)
	agents().Where("status = ?", "online").Count(&out.AgentsOnline)
	out.AgentsOffline = out.AgentsTotal - out.AgentsOnline
	agents().Where("status = ? AND elevated = ?", "online", true).Count(&out.ElevatedOnline)
	var onlineHosts []db.Implant
	// The snapshot only counts stale online agents: project the three columns
	// implantIsStale reads instead of the full row, since this runs on every
	// AI chat request and the online set can be large.
	agents().Where("status = ?", "online").Select("status, last_seen, current_interval").Find(&onlineHosts)
	for _, a := range onlineHosts {
		if implantIsStale(a) {
			out.StaleOnline++
		}
	}
	if hasPermission(db.PermListenersRead) {
		s.aiTenantScope(s.db.Model(&db.Listener{}), reqCtx).Where("enabled = ?", true).Count(&out.ListenersActive)
	}
	if hasPermission(db.PermTasksRead) {
		tasks := s.db.Model(&db.Task{})
		if scoped {
			tenantAgents := s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID)
			tasks = tasks.Where("agent_id IN (?)", tenantAgents)
		}
		tasks.Where("status = ?", "pending_approval").Count(&out.PendingApproval)
		tasks.Where("status = ? AND created_by = ?", "pending_approval", "ai").Count(&out.AIPending)
	}
	if hasPermission(db.PermOpsecRead) {
		// Alerts carry the alerting agent's tenant, so this count must be scoped like
		// everything else here. It was global: one tenant could see how noisy another
		// tenant's estate was by asking the assistant for a summary.
		alerts := s.db.Model(&db.Alert{}).Where("status = ?", "active")
		if scoped {
			alerts = alerts.Where("tenant_id = ?", reqCtx.Principal.TenantID)
		}
		alerts.Count(&out.ActiveAlerts)
	}
	if hasPermission(db.PermCredsRead) {
		credentials := s.db.Model(&db.CredentialEntry{})
		if scoped {
			credentials = credentials.Where("tenant_id = ?", reqCtx.Principal.TenantID)
		}
		credentials.Count(&out.Credentials)
	}
	if hasPermission(db.PermUsersRead) && s.operatorSessions != nil {
		out.OperatorsOnline = s.operatorSessions.ActiveOperatorCount()
	}

	type osRow struct {
		OS    string
		Count int64
	}
	var osRows []osRow
	agents().Select("os as os, count(*) as count").Where("status = ?", "online").Group("os").Scan(&osRows)
	for _, row := range osRows {
		label := row.OS
		if strings.TrimSpace(label) == "" {
			label = "unknown"
		}
		out.OnlineOS[label] = row.Count
	}

	var recent []db.Implant
	agents().Order("last_seen desc").Limit(5).Find(&recent)
	for _, a := range recent {
		out.RecentAgents = append(out.RecentAgents, map[string]string{
			"id": a.ID, "hostname": a.Hostname, "os": a.OS, "status": a.Status,
			"username": a.Username, "last_seen": a.LastSeen.Format(time.RFC3339),
		})
	}
	return out
}

func (s *Server) buildSituationSnapshot() string {
	snap := s.collectSituation(nil)
	if s.db == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Current situation snapshot (live)\n")
	sb.WriteString(fmt.Sprintf("- Agents: %d total, %d online, %d offline, %d elevated-online, %d stale-online\n",
		snap.AgentsTotal, snap.AgentsOnline, snap.AgentsOffline, snap.ElevatedOnline, snap.StaleOnline))
	if len(snap.OnlineOS) > 0 {
		sb.WriteString("- Online OS mix:")
		for osName, n := range snap.OnlineOS {
			sb.WriteString(fmt.Sprintf(" %s=%d", osName, n))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("- Listeners: %d active\n", snap.ListenersActive))
	sb.WriteString(fmt.Sprintf("- Tasks pending approval: %d", snap.PendingApproval))
	if snap.AIPending > 0 {
		sb.WriteString(fmt.Sprintf(" (%d AI-proposed)", snap.AIPending))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("- Active alerts: %d · credentials in vault: %d · operators online: %d\n",
		snap.ActiveAlerts, snap.Credentials, snap.OperatorsOnline))
	if len(snap.RecentAgents) > 0 {
		sb.WriteString("- Recently seen agents:\n")
		for _, a := range snap.RecentAgents {
			sb.WriteString(fmt.Sprintf("  - %s (%s, %s, %s, %s, last_seen %s)\n",
				a["id"], a["hostname"], a["os"], a["username"], a["status"], a["last_seen"]))
		}
	}
	return sb.String()
}

// ── Sensitive command guard ───────────────────────────────────────────────

var sensitiveCommandKeywords = map[string]bool{
	"mimikatz":        true,
	"secretsdump":     true,
	"dcsync":          true,
	"kerberoast":      true,
	"psexec":          true,
	"wce":             true,
	"bloodhound":      true,
	"sharphound":      true,
	"sekurlsa":        true,
	"lsass":           true,
	"sam":             true,
	"wdigest":         true,
	"ntdsutil":        true,
	"comsvcs":         true,
	"rubeus":          true,
	"sharpkatz":       true,
	"kiwi":            true,
	"invoke-mimikatz": true,
	"ntds":            true,
	"shadowcopy":      true,
	"shadow copy":     true,
	"procdump":        true,
}

func isSensitiveCommand(cmd string) bool {
	// Normalize: lower, replace punctuation with space, collapse spaces
	lower := strings.ToLower(cmd)
	// Strip common obfuscation: punctuation and shell separators -> space
	repl := strings.NewReplacer(";", " ", "&", " ", "|", " ", "`", " ", "$", " ", "(", " ", ")", " ", "\"", " ", "'", " ", ",", " ", ".", " ", ":", " ", "/", " ", "\\", " ", "-", " ", "_", " ")
	norm := repl.Replace(lower)
	// Remove extra spaces
	norm = strings.Join(strings.Fields(norm), " ")
	for kw := range sensitiveCommandKeywords {
		if strings.Contains(norm, kw) || strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func canonicalJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		// Sort keys by re-marshaling via ordered map
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ordered := make(map[string]any, len(x))
		for _, k := range keys {
			ordered[k] = canonicalJSON(x[k])
		}
		return ordered
	case []any:
		for i, e := range x {
			x[i] = canonicalJSON(e)
		}
		return x
	default:
		return v
	}
}

var (
	taskWaitMaxDuration = AITaskWaitMax
	taskPollMinInterval = AITaskPollMinInterval
)

type executeCommandArgs struct {
	AgentID       string
	Command       string
	Shell         string
	WaitForResult bool
}

func parseExecuteCommandArgs(argsJSON string) executeCommandArgs {
	var raw struct {
		AgentID       string `json:"agent_id"`
		Command       string `json:"command"`
		Shell         string `json:"shell"`
		WaitForResult *bool  `json:"wait_for_result"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		slog.Error("ai: failed to unmarshal execute command args", "error", err, "args", truncateString(argsJSON, 100))
	}
	out := executeCommandArgs{
		AgentID:       raw.AgentID,
		Command:       raw.Command,
		Shell:         raw.Shell,
		WaitForResult: true,
	}
	if raw.WaitForResult != nil {
		out.WaitForResult = *raw.WaitForResult
	}
	return out
}
