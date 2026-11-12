package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestAuditActionCategory pins the classifier the operations filters and the
// catalogue share. `operator:` is a storage namespace, not a category: both
// spellings of the same action must land in the same bucket.
func TestAuditActionCategory(t *testing.T) {
	cases := map[string]string{
		"login":                      "auth",
		"login_failed":               "auth",
		"user_create":                "auth",
		"api_key_manage":             "auth",
		"operator:ai_profile_create": "ai",
		"ai_tool":                    "ai",
		"operator:agent_delete":      "agents",
		"agent_action":               "agents",
		"implant_checkin":            "system",
		"task_create":                "tasks",
		"command_result":             "tasks",
		"operator:execute_script":    "tasks",
		"credential_ingest":          "credentials",
		"password_spray":             "credentials",
		"file_upload_push":           "files",
		"bof_run":                    "files",
		"listener_manage":            "infrastructure",
		"dns_start":                  "infrastructure",
		"workflow_execute":           "automation",
		"notification_route_test":    "automation",
		"tenant_create":              "tenants",
		"settings_change":            "settings",
		"db_backup":                  "settings",
		"connect":                    "system",
		"udp_data":                   "system",
		"something_new":              "other",
	}
	for action, want := range cases {
		if got := auditActionCategory(action); got != want {
			t.Errorf("auditActionCategory(%q) = %q, want %q", action, got, want)
		}
	}
}

// auditOpsServer seeds one row per interesting shape: a task, a destructive
// operator action, beacon transport noise, and another tenant's row that must
// never be visible.
func auditOpsServer(t *testing.T) *Server {
	t.Helper()
	s := mustTenantServer(t)
	now := time.Now()
	rows := []db.AuditLog{
		{User: "alice", Action: "task_create", Resource: "task", TenantID: 2, Success: true, Details: "created task", CreatedAt: now.Add(-1 * time.Hour)},
		{User: "alice", Action: "operator:agent_delete", Resource: "agent", AgentID: "agent-1", TenantID: 2, Success: false, Error: "permission denied", Details: "delete agent-1", CreatedAt: now.Add(-2 * time.Hour)},
		{User: "bob", Action: "implant_checkin", Resource: "beacon", TenantID: 2, Success: true, Details: "heartbeat", CreatedAt: now.Add(-3 * time.Hour)},
		{User: "tenant1", Action: "task_create", Resource: "task", TenantID: 1, Success: true, Details: "other tenant", CreatedAt: now.Add(-1 * time.Hour)},
		{User: "carol", Action: "settings_change", Resource: "settings", TenantID: 2, Success: true, Details: "changed retention", CreatedAt: now.Add(-40 * 24 * time.Hour)},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	return s
}

type auditOpsResponse struct {
	Data struct {
		Logs   []db.AuditLog `json:"logs"`
		Total  int64         `json:"total"`
		Failed int64         `json:"failed"`
	} `json:"data"`
}

func auditOpsQuery(s *Server, t *testing.T, rawQuery string) auditOpsResponse {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
	c.Request.URL.RawQuery = rawQuery
	s.handleGetAuditLogs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("query %q: status=%d body=%s", rawQuery, w.Code, w.Body.String())
	}
	var body auditOpsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v body=%s", rawQuery, err, w.Body.String())
	}
	return body
}

func auditActions(body auditOpsResponse) []string {
	out := make([]string, 0, len(body.Data.Logs))
	for _, row := range body.Data.Logs {
		out = append(out, row.Action)
	}
	return out
}

// TestAuditOperationsFilters covers the operation-log contract: category,
// result, search, time range and the default exclusion of beacon transport
// noise. Every case also proves tenant 1's row stays invisible.
func TestAuditOperationsFilters(t *testing.T) {
	s := auditOpsServer(t)

	tasks := auditOpsQuery(s, t, "category=tasks&scope=operations")
	if got := strings.Join(auditActions(tasks), ","); got != "task_create" {
		t.Fatalf("category=tasks = %q, want task_create", got)
	}
	if tasks.Data.Total != 1 || tasks.Data.Failed != 0 {
		t.Fatalf("tasks total/failed = %d/%d, want 1/0", tasks.Data.Total, tasks.Data.Failed)
	}

	agents := auditOpsQuery(s, t, "category=agents&scope=operations")
	if got := strings.Join(auditActions(agents), ","); got != "operator:agent_delete" {
		t.Fatalf("category=agents = %q", got)
	}
	if agents.Data.Failed != 1 {
		t.Fatalf("agents failed = %d, want 1", agents.Data.Failed)
	}

	// Default operations scope hides implant_checkin, but an explicit system
	// category must still return it.
	ops := auditOpsQuery(s, t, "scope=operations&since=24h")
	if got := strings.Join(auditActions(ops), ","); got != "task_create,operator:agent_delete" {
		t.Fatalf("scope=operations = %q, want the two operator rows", got)
	}
	system := auditOpsQuery(s, t, "category=system&scope=operations")
	if got := strings.Join(auditActions(system), ","); got != "implant_checkin" {
		t.Fatalf("category=system = %q, want implant_checkin", got)
	}

	failed := auditOpsQuery(s, t, "scope=operations&result=failed")
	if got := strings.Join(auditActions(failed), ","); got != "operator:agent_delete" {
		t.Fatalf("result=failed = %q", got)
	}

	search := auditOpsQuery(s, t, "scope=operations&search=permission")
	if got := strings.Join(auditActions(search), ","); got != "operator:agent_delete" {
		t.Fatalf("search=permission = %q", got)
	}

	// A 24h window must drop the 40-day-old settings change; all-time keeps it.
	recent := auditOpsQuery(s, t, "scope=operations&since=24h")
	if got := strings.Join(auditActions(recent), ","); strings.Contains(got, "settings_change") {
		t.Fatalf("since=24h still returned the old row: %q", got)
	}
	all := auditOpsQuery(s, t, "scope=operations&since=60d")
	if got := strings.Join(auditActions(all), ","); !strings.Contains(got, "settings_change") {
		t.Fatalf("since=60d missing the old row: %q", got)
	}

	// The catalogue's raw action value filters exactly, with either spelling.
	exact := auditOpsQuery(s, t, "scope=operations&action=agent_delete")
	if got := strings.Join(auditActions(exact), ","); got != "operator:agent_delete" {
		t.Fatalf("action=agent_delete = %q", got)
	}

	// The events tab's coarse verbs are substring filters: `create` must reach
	// task_create, and `delete` must reach operator:agent_delete.
	containsCreate := auditOpsQuery(s, t, "scope=operations&action=create&match=contains")
	if got := strings.Join(auditActions(containsCreate), ","); got != "task_create" {
		t.Fatalf("match=contains action=create = %q, want task_create", got)
	}
	containsDelete := auditOpsQuery(s, t, "scope=operations&action=delete&match=contains")
	if got := strings.Join(auditActions(containsDelete), ","); got != "operator:agent_delete" {
		t.Fatalf("match=contains action=delete = %q, want operator:agent_delete", got)
	}
}

// TestAuditOperationsRejectInvalidFilters proves a typo answers 400 instead of
// silently returning the whole table.
func TestAuditOperationsRejectInvalidFilters(t *testing.T) {
	s := auditOpsServer(t)
	for _, raw := range []string{
		"result=bogus",
		"category=bogus",
		"scope=bogus",
		"match=bogus",
		"since=yesterday",
		"until=yesterday",
		"since=2026-11-10T00:00:00Z&until=2026-11-09T00:00:00Z",
	} {
		c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
		c.Request.URL.RawQuery = raw
		s.handleGetAuditLogs(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: status=%d, want 400 (body=%s)", raw, w.Code, w.Body.String())
		}
	}
}

// TestAuditActionCatalogue proves the filter dropdown is built from the
// caller's tenant, with categories matching the classifier.
func TestAuditActionCatalogue(t *testing.T) {
	s := auditOpsServer(t)

	c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
	s.handleGetAuditActions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Actions []struct {
				Action   string `json:"action"`
				Category string `json:"category"`
				Count    int64  `json:"count"`
			} `json:"actions"`
			Categories []struct {
				ID    string `json:"id"`
				Count int64  `json:"count"`
			} `json:"categories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}

	byAction := map[string]int64{}
	category := map[string]string{}
	for _, a := range body.Data.Actions {
		byAction[a.Action] = a.Count
		category[a.Action] = a.Category
	}
	if byAction["task_create"] != 1 {
		t.Fatalf("task_create count = %d, want 1 (tenant 1 must not leak)", byAction["task_create"])
	}
	if byAction["operator:agent_delete"] != 1 || category["operator:agent_delete"] != "agents" {
		t.Fatalf("agent_delete catalogue row = %+v", byAction)
	}
	if category["implant_checkin"] != "system" {
		t.Fatalf("implant_checkin category = %q, want system", category["implant_checkin"])
	}

	counts := map[string]int64{}
	for _, c := range body.Data.Categories {
		counts[c.ID] = c.Count
	}
	if counts["tasks"] != 1 || counts["agents"] != 1 || counts["system"] != 1 || counts["settings"] != 1 {
		t.Fatalf("category counts = %+v, want tasks=1 agents=1 system=1 settings=1", counts)
	}
	if _, ok := counts["auth"]; !ok {
		t.Fatal("catalogue must publish every category id, including empty ones")
	}
}

// TestAuditActionCatalogueCountsTheSameWindow proves the catalogue counts the
// rows the list would show.
//
// The console prints a count next to every category ("Infrastructure 573").
// That number has to describe the window the table is showing: a catalogue
// counted over all time next to a seven-day table is a number the page cannot
// honour, and an operator comparing the two concludes the list is broken.
func TestAuditActionCatalogueCountsTheSameWindow(t *testing.T) {
	s := auditOpsServer(t)

	// One recent row and one outside the window.
	now := time.Now()
	rows := []db.AuditLog{
		{User: "alice", Action: "task_create", Resource: "task", TenantID: 2, Success: true, CreatedAt: now.Add(-2 * time.Hour)},
		{User: "alice", Action: "task_create", Resource: "task", TenantID: 2, Success: true, CreatedAt: now.Add(-40 * time.Hour)},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	count := func(query string) (int64, int) {
		c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
		c.Request = httptest.NewRequest(http.MethodGet, "/audit/actions"+query, nil)
		s.handleGetAuditActions(c)
		if w.Code != http.StatusOK {
			return -1, w.Code
		}
		var body struct {
			Data struct {
				Actions []struct {
					Action string `json:"action"`
					Count  int64  `json:"count"`
				} `json:"actions"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		for _, a := range body.Data.Actions {
			if a.Action == "task_create" {
				return a.Count, w.Code
			}
		}
		return 0, w.Code
	}

	// The fixture already holds one task_create row for this tenant.
	if got, code := count(""); got != 3 || code != http.StatusOK {
		t.Fatalf("unbounded catalogue count = %d (status %d), want 3 (every row)", got, code)
	}
	if got, code := count("?since=24h"); got != 2 || code != http.StatusOK {
		t.Fatalf("24h catalogue count = %d (status %d), want 2 (the rows inside the window)", got, code)
	}
	// An invalid window is rejected, not silently ignored: the catalogue would
	// otherwise count a window the list cannot show.
	if _, code := count("?since=bogus"); code != http.StatusBadRequest {
		t.Fatalf("an invalid window must be rejected, got status %d", code)
	}
}

// TestAuditOperationsCSVExport proves the export shares the list filters,
// neutralizes spreadsheet formulas and is tenant-scoped.
func TestAuditOperationsCSVExport(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()
	rows := []db.AuditLog{
		{User: "=HYPERLINK(\"https://evil.test\")", Action: "task_create", Resource: "task", TenantID: 2, Success: true, Details: "+cmd|' /C calc'!A0", CreatedAt: now},
		{User: "bob", Action: "implant_checkin", Resource: "beacon", TenantID: 2, Success: true, Details: "noise", CreatedAt: now},
		{User: "tenant1", Action: "task_create", Resource: "task", TenantID: 1, Success: true, Details: "other tenant", CreatedAt: now},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
	c.Request.URL.RawQuery = "category=tasks&scope=operations"
	s.handleExportAuditLogsCSV(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got := w.Header().Get("X-Audit-Export-Rows"); got != "1" {
		t.Fatalf("X-Audit-Export-Rows = %q, want 1", got)
	}

	raw := w.Body.Bytes()
	if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("CSV export is missing the UTF-8 BOM")
	}
	records, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("csv rows = %d, want header + 1: %v", len(records), records)
	}
	if records[0][0] != "Time" || records[0][4] != "Category" {
		t.Fatalf("header = %v", records[0])
	}
	if records[1][4] != "tasks" {
		t.Fatalf("category cell = %q, want tasks", records[1][4])
	}
	if !strings.HasPrefix(records[1][1], "'=") {
		t.Fatalf("user cell = %q, want a formula-neutralized value", records[1][1])
	}
	if !strings.HasPrefix(records[1][8], "'+") {
		t.Fatalf("details cell = %q, want a formula-neutralized value", records[1][8])
	}
}
