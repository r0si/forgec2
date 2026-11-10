package server

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestLoginLogCSVExportSharesFilters proves the export is the same selection as
// the page: auth actions only, the caller's tenant, user/result filters, the
// resolved country/city, and formula-neutralized cells.
func TestLoginLogCSVExportSharesFilters(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()
	rows := []db.AuditLog{
		{User: "alice", Action: "login", IP: "203.0.113.9", TenantID: 2, Success: true, Details: "Login successful", CreatedAt: now},
		{User: "mallory", Action: "login_failed", IP: "198.51.100.4", TenantID: 2, Success: false, Details: "=cmd|' /C calc'!A0", CreatedAt: now.Add(-time.Minute)},
		{User: "bob", Action: "logout", IP: "203.0.113.10", TenantID: 2, Success: true, Details: "Logout", CreatedAt: now.Add(-2 * time.Minute)},
		{User: "tenant1", Action: "login", IP: "203.0.113.11", TenantID: 1, Success: true, Details: "other tenant", CreatedAt: now},
		{User: "alice", Action: "task_create", IP: "203.0.113.9", TenantID: 2, Success: true, Details: "not a login", CreatedAt: now},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	for _, geo := range []db.LoginGeo{
		{IP: "203.0.113.9", Country: "德国", City: "法兰克福", ResolvedAt: now},
		{IP: "198.51.100.4", Country: "美国", City: "洛杉矶", ResolvedAt: now},
		{IP: "203.0.113.10", Country: "中国", City: "北京", ResolvedAt: now},
	} {
		if err := s.db.Create(&geo).Error; err != nil {
			t.Fatalf("seed geo %s: %v", geo.IP, err)
		}
	}

	export := func(query string) [][]string {
		t.Helper()
		c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
		c.Request.URL.RawQuery = query
		s.handleExportLoginEventsCSV(c)
		if w.Code != http.StatusOK {
			t.Fatalf("query %q: status=%d body=%s", query, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
			t.Fatalf("Content-Type = %q", ct)
		}
		raw := w.Body.Bytes()
		if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
			t.Fatal("CSV export is missing the UTF-8 BOM")
		}
		if query == "" {
			if got := w.Header().Get("X-Audit-Export-Rows"); got != "3" {
				t.Fatalf("X-Audit-Export-Rows = %q, want 3", got)
			}
		}
		records, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))).ReadAll()
		if err != nil {
			t.Fatalf("parse csv: %v", err)
		}
		return records
	}

	all := export("")
	if len(all) != 4 {
		t.Fatalf("csv rows = %d, want header + 3: %v", len(all), all)
	}
	if all[0][0] != "Time" || all[0][6] != "Result" {
		t.Fatalf("header = %v", all[0])
	}
	if all[1][1] != "alice" || all[1][4] != "德国" || all[1][5] != "法兰克福" {
		t.Fatalf("newest row = %v, want alice/德国/法兰克福", all[1])
	}
	if all[2][6] != "failed" {
		t.Fatalf("failed row result = %q", all[2][6])
	}
	if !strings.HasPrefix(all[2][7], "'=") {
		t.Fatalf("details = %q, want a formula-neutralized value", all[2][7])
	}
	if all[3][2] != "logout" {
		t.Fatalf("third row action = %q, want logout", all[3][2])
	}
	for _, r := range all[1:] {
		if r[1] == "tenant1" {
			t.Fatal("tenant 1 row leaked into tenant 2's export")
		}
		if r[2] == "task_create" {
			t.Fatal("a non-auth action leaked into the login export")
		}
	}

	// The export honours the same filters as the page.
	failed := export("result=failed")
	if len(failed) != 2 || failed[1][1] != "mallory" {
		t.Fatalf("result=failed rows = %v, want header + mallory", failed)
	}
	alice := export("user=alice")
	if len(alice) != 2 || alice[1][1] != "alice" || alice[1][2] != "login" {
		t.Fatalf("user=alice rows = %v, want header + alice's login", alice)
	}
}
