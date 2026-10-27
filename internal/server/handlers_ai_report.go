package server

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// mitreCoverageCounts summarizes technique coverage: how many tactics have at
// least one exercised technique vs tactics whose mapped task types were never
// run. Shared by get_coverage_gaps and generate_report.
//
// scope narrows the census to the caller's visible agents; nil is the unscoped
// (legacy single-tenant) view. Without it the numbers came from EVERY tenant's task
// history, so one team's report revealed which techniques another team had run.
func (s *Server) mitreCoverageCounts(scope func(*gorm.DB) *gorm.DB) (coveredTactics, gapTactics int, usedTypes []string, err error) {
	used := map[string]bool{}
	typeQuery := s.db.Table("tasks")
	if scope != nil {
		typeQuery = scope(typeQuery)
	}
	if err := typeQuery.Distinct().Pluck("type", &usedTypes).Error; err != nil {
		return 0, 0, nil, fmt.Errorf("load MITRE task types: %w", err)
	}
	for _, t := range usedTypes {
		used[t] = true
	}
	seenCovered := map[string]bool{}
	seenGap := map[string]bool{}
	for _, grp := range attackTacticMap {
		for _, tech := range grp.Techniques {
			isCovered := false
			for _, tt := range tech.TaskTypes {
				if used[tt] {
					isCovered = true
					break
				}
			}
			if isCovered {
				seenCovered[grp.Tactic] = true
			} else {
				seenGap[grp.Tactic] = true
			}
		}
	}
	return len(seenCovered), len(seenGap), usedTypes, nil
}

// buildAIMarkdownReport renders a live engagement snapshot as a markdown
// document. The scope controls which sections are included:
//
//	full       -- everything (default)
//	executive  -- overview + findings + recommendations
//	technical  -- agents + tasks + credentials + network + iocs
//	coverage   -- MITRE ATT&CK gap analysis only
//
// Returns the markdown body and the ordered list of included section keys.
// reqCtx carries the caller's tenant: authenticated AI principals see only
// their own tenant's rows (tenant 0 included, mirroring resolveAIAgentID);
// nil/anonymous callers keep the legacy global view.
func (s *Server) buildAIMarkdownReport(scope string, reqCtx *aiReqCtx) (string, []string, error) {
	now := time.Now()
	since := now.AddDate(0, 0, -30)
	scoped := reqCtx != nil && reqCtx.Principal.UserID != 0
	agentScope := s.principalAgentIDs(reqCtx)

	var agents []db.Implant
	agentQuery := s.db.Order("last_seen desc").Limit(200)
	if scoped {
		agentQuery = agentQuery.Where("id IN (?)", agentScope)
	}
	if err := agentQuery.Find(&agents).Error; err != nil {
		return "", nil, fmt.Errorf("load agents: %w", err)
	}
	online, elevated := 0, 0
	domSet := map[string]bool{}
	for _, a := range agents {
		if a.Status == "online" {
			online++
		}
		if a.Elevated {
			elevated++
		}
		if d := strings.TrimSpace(a.Domain); d != "" {
			domSet[d] = true
		}
	}

	var totT, okT, failT, actT int64
	scopedTasks := func(q *gorm.DB) *gorm.DB {
		if scoped {
			return q.Where("agent_id IN (?)", agentScope)
		}
		return q
	}
	if err := scopedTasks(s.db.Model(&db.Task{})).Where("created_at >= ?", since).Count(&totT).Error; err != nil {
		return "", nil, fmt.Errorf("count recent tasks: %w", err)
	}
	if err := scopedTasks(s.db.Model(&db.Task{})).Where("created_at >= ? AND status = ?", since, "completed").Count(&okT).Error; err != nil {
		return "", nil, fmt.Errorf("count completed tasks: %w", err)
	}
	if err := scopedTasks(s.db.Model(&db.Task{})).Where("created_at >= ? AND status = ?", since, "failed").Count(&failT).Error; err != nil {
		return "", nil, fmt.Errorf("count failed tasks: %w", err)
	}
	if err := scopedTasks(s.db.Model(&db.Task{})).Where("status IN ?", []string{"pending", TaskStatusPendingApproval}).Count(&actT).Error; err != nil {
		return "", nil, fmt.Errorf("count queued tasks: %w", err)
	}

	var totC, valC, invC int64
	if err := scopedTasks(s.db.Model(&db.CredentialEntry{})).Count(&totC).Error; err != nil {
		return "", nil, fmt.Errorf("count credentials: %w", err)
	}
	if err := scopedTasks(s.db.Model(&db.CredentialEntry{})).Where("verify_status = ?", "valid").Count(&valC).Error; err != nil {
		return "", nil, fmt.Errorf("count valid credentials: %w", err)
	}
	if err := scopedTasks(s.db.Model(&db.CredentialEntry{})).Where("verify_status = ?", "invalid").Count(&invC).Error; err != nil {
		return "", nil, fmt.Errorf("count invalid credentials: %w", err)
	}

	var listeners []db.Listener
	listenerQuery := s.db.Order("created_at desc").Limit(100)
	if scoped {
		listenerQuery = listenerQuery.Where("tenant_id = ?", reqCtx.Principal.TenantID)
	}
	if err := listenerQuery.Find(&listeners).Error; err != nil {
		return "", nil, fmt.Errorf("load listeners: %w", err)
	}
	lisEnabled := 0
	for _, l := range listeners {
		if l.Enabled {
			lisEnabled++
		}
	}

	iocs, _, err := s.extractIOCs(30, false, agentScope)
	if err != nil {
		return "", nil, err
	}
	sort.Slice(iocs, func(i, j int) bool { return iocs[i].Count > iocs[j].Count })

	// The report already narrows tasks and credentials to the visible agents; the
	// coverage census and the recent-failure list were the two queries that did not.
	reportScope := func(q *gorm.DB) *gorm.DB {
		if scoped {
			return q.Where("agent_id IN (?)", agentScope)
		}
		return q
	}
	covTac, gapTac, usedTypes, err := s.mitreCoverageCounts(reportScope)
	if err != nil {
		return "", nil, err
	}

	var failed []db.Task
	if err := scopedTasks(s.db.Where("status = ? AND created_at >= ?", "failed", since)).Order("created_at desc").Limit(8).Find(&failed).Error; err != nil {
		return "", nil, fmt.Errorf("load recent failures: %w", err)
	}

	allSections := []string{"overview", "agents", "tasks", "credentials", "network", "iocs", "coverage", "findings", "recommendations"}
	included := make([]string, 0, len(allSections))
	for _, k := range allSections {
		switch scope {
		case "executive":
			if k == "overview" || k == "findings" || k == "recommendations" {
				included = append(included, k)
			}
		case "technical":
			if k == "agents" || k == "tasks" || k == "credentials" || k == "network" || k == "iocs" {
				included = append(included, k)
			}
		case "coverage":
			if k == "coverage" {
				included = append(included, k)
			}
		default:
			included = append(included, k)
		}
	}
	isIncluded := func(k string) bool {
		for _, v := range included {
			if v == k {
				return true
			}
		}
		return false
	}

	var b strings.Builder
	w := func(format string, args ...interface{}) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	w("# ForgeC2 Engagement Report (%s)", scope)
	w("")
	w("_Generated %s by the AI assistant - data window: last 30 days_\n", now.Format("2006-01-02 15:04"))

	if isIncluded("overview") {
		successRate := 0.0
		if totT > 0 {
			successRate = float64(okT) * 100 / float64(totT)
		}
		w("## Overview\n")
		w("| Metric | Value |")
		w("|---|---|")
		w("| Agents | %d total / %d online / %d elevated |", len(agents), online, elevated)
		w("| Tasks (30d) | %d executed / %.1f%% success / %d queued |", totT, successRate, actT)
		w("| Credentials | %d collected / %d valid / %d invalid |", totC, valC, invC)
		w("| Listeners | %d configured / %d enabled |", len(listeners), lisEnabled)
		w("| MITRE coverage | %d tactics exercised / %d gaps |", covTac, gapTac)
		w("| IOCs extracted | %d unique indicators |", len(iocs))
		w("")
	}

	if isIncluded("agents") {
		w("## Agents\n")
		w("| Hostname | IP | OS | User | Domain | Integrity | Status | Last Seen |")
		w("|---|---|---|---|---|---|---|---|")
		shown := len(agents)
		if shown > 15 {
			shown = 15
		}
		for _, a := range agents[:shown] {
			integ := a.Integrity
			if integ == "" {
				integ = "-"
			}
			w("| %s | %s | %s | %s | %s | %s | %s | %s |",
				a.Hostname, a.IP, a.OS, a.Username, a.Domain, integ,
				a.Status, a.LastSeen.Format("01-02 15:04"))
		}
		if len(agents) > shown {
			w("\n_(+%d more)_", len(agents)-shown)
		}
		w("")
	}

	if isIncluded("tasks") {
		w("## Task Activity (30d)\n")
		w("- Executed: **%d** / Completed: **%d** / Failed: **%d** / Queued-pending: **%d**", totT, okT, failT, actT)
		if failT > 0 {
			w("\n### Recent failures\n")
			for _, t := range failed {
				errMsg := t.Error
				if errMsg == "" {
					errMsg = truncateStr(redactSensitiveResultForAuxiliary(t.Type, t.Result), 80)
				}
				w("- #%d `%s` on %s -- %s", t.ID, t.Type, t.AgentID, truncateStr(errMsg, 120))
			}
			w("")
		}
	}

	if isIncluded("credentials") {
		w("## Credentials\n")
		unchecked := totC - valC - invC
		if unchecked < 0 {
			unchecked = 0
		}
		w("- Total entries: **%d**", totC)
		w("- Verified valid: **%d** / invalid: **%d** / unchecked: **%d**", valC, invC, unchecked)
		if len(domSet) > 0 {
			doms := make([]string, 0, len(domSet))
			for d := range domSet {
				doms = append(doms, d)
			}
			sort.Strings(doms)
			w("- Domains seen: %s", strings.Join(doms, ", "))
		}
		w("")
	}

	if isIncluded("network") {
		w("## Network Infrastructure\n")
		w("| Listener | Scheme | Bind | Enabled | Status |")
		w("|---|---|---|---|---|")
		for _, l := range listeners {
			bind := fmt.Sprintf("%s:%d", l.Host, l.Port)
			if l.Scheme == "dns" {
				bind = "dns://" + l.DNSDomain
			} else if l.Scheme == "icmp" {
				bind = "icmp://" + l.ICMPAddr
			}
			w("| %s | %s | %s | %t | %s |", l.Name, l.Scheme, bind, l.Enabled, l.Status)
		}
		subnetSet := map[string]bool{}
		for _, a := range agents {
			if idx := strings.LastIndex(a.IP, "."); idx > 0 {
				subnetSet[a.IP[:idx]+".0/24"] = true
			}
		}
		if len(subnetSet) > 0 {
			subs := make([]string, 0, len(subnetSet))
			for sn := range subnetSet {
				subs = append(subs, sn)
			}
			sort.Strings(subs)
			w("\nAgent subnets: %s", strings.Join(subs, ", "))
		}
		w("")
	}

	if isIncluded("iocs") {
		w("## Indicators of Compromise\n")
		if len(iocs) == 0 {
			w("_No IOCs extracted in the window._\n")
		} else {
			top := len(iocs)
			if top > 12 {
				top = 12
			}
			w("| Type | Value | Count |")
			w("|---|---|---|")
			for _, e := range iocs[:top] {
				w("| %s | `%s` | %d |", e.Type, e.Value, e.Count)
			}
			w("")
		}
	}

	if isIncluded("coverage") {
		w("## MITRE ATT&CK Coverage\n")
		w("- Tactics with exercised techniques: **%d**", covTac)
		w("- Tactics with gaps (mapped but never run): **%d**", gapTac)
		if len(usedTypes) > 0 {
			types := append([]string(nil), usedTypes...)
			sort.Strings(types)
			w("- Task types exercised: %s", strings.Join(types, ", "))
		}
		w("\n_Use get_coverage_gaps for the per-technique breakdown._")
		w("")
	}

	if isIncluded("findings") {
		w("## Key Findings\n")
		n := 0
		if len(agents) > 0 && elevated > 0 {
			n++
			w("%d. **%d of %d agents run elevated** -- credential access and lateral movement are viable from those sessions.", n, elevated, len(agents))
		}
		if totC > 0 && valC == 0 && invC == 0 {
			n++
			w("%d. **No credentials verified yet** -- run batch verification before relying on harvested secrets.", n)
		}
		if invC > 0 {
			n++
			w("%d. **%d credentials marked invalid** -- prune or re-harvest to avoid lockout risk.", n, invC)
		}
		if lisEnabled == 0 && len(listeners) > 0 {
			n++
			w("%d. **All listeners disabled** -- new implants cannot check in.", n)
		}
		for _, t := range failed {
			if n >= 6 {
				break
			}
			n++
			w("%d. Failed task #%d (`%s`) on %s: %s", n, t.ID, t.Type, t.AgentID, truncateStr(t.Error, 100))
		}
		if n == 0 {
			w("_No significant findings in the current window._")
		}
		w("")
	}

	if isIncluded("recommendations") {
		w("## Recommendations\n")
		recN := 0
		rec := func(line string) {
			recN++
			w("%d. %s", recN, line)
		}
		if offline := len(agents) - online; len(agents) > 0 && offline > online {
			rec(fmt.Sprintf("Majority of agents (%d/%d) are offline -- re-establish footholds or deploy new stagers on fresh listener infrastructure.", offline, len(agents)))
		}
		if totC > 0 && valC == 0 && invC == 0 {
			rec("Verify harvested credentials in batch (Credentials page -> Verify selected) before lateral movement.")
		}
		if gapTac > covTac {
			rec("Coverage gaps outnumber exercised tactics -- pick low-noise techniques from get_coverage_gaps that complement existing access.")
		}
		if lisEnabled < 2 {
			rec("Maintain at least two independent egress channels (e.g. HTTPS + DNS) so one blocked channel does not strand implants.")
		}
		if elevated == 0 && len(agents) > 0 {
			rec("No elevated sessions -- prioritize privilege escalation (UAC bypass, token manipulation) on high-value hosts.")
		}
		if recN == 0 {
			rec("Posture looks healthy -- continue staged collection and document new findings via engagement notes.")
		}
		w("")
	}

	md := b.String()
	if len(md) > 60000 {
		md = md[:60000] + "\n\n_(truncated)_"
	}
	return md, included, nil
}
