package db

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// hotQuery is one query the console or a beacon runs constantly, with the reason it
// must not degrade. The list exists because two rounds of hand-auditing found the same
// defect twice: a tenant filter plus an ORDER BY on a column the index did not cover,
// which makes SQLite read every row of the tenant and sort it in memory for one page.
type hotQuery struct {
	name string
	sql  string
	// allowTempSort is for queries over tiny tables (config, listeners), where an
	// in-memory sort costs nothing and an index would be noise.
	allowTempSort bool
	// allowRowidScan is for "ORDER BY id DESC LIMIT n" over a rowid table: SQLite
	// reports that as a plain SCAN even though it walks the rowid B-tree backwards and
	// stops at the limit. Measured on 50000 audit rows: 75µs, against 80µs for the
	// equivalent WHERE id > … seek — a real scan would cost milliseconds.
	allowRowidScan bool
}

// TestHotQueriesHaveNoUnindexedSortOrScan walks EXPLAIN QUERY PLAN for the queries the
// console loads on its busiest pages and fails if any of them sorts in a TEMP B-TREE
// or scans a large table.
//
// This is the executable version of the last three rounds' findings: without it the
// next list page can reintroduce the same shape and only a fresh audit would notice.
func TestHotQueriesHaveNoUnindexedSortOrScan(t *testing.T) {
	database, err := InitDB(filepath.Join(t.TempDir(), "plans.db"), slog.LevelError)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	queries := []hotQuery{
		{
			name: "task list page (tenant filter, newest first)",
			sql:  "SELECT * FROM tasks WHERE tenant_id = 1 ORDER BY created_at DESC LIMIT 50",
		},
		{
			name: "agent list page (tenant filter, most recently seen first)",
			sql:  "SELECT * FROM implants WHERE tenant_id = 1 ORDER BY last_seen DESC LIMIT 100",
		},
		{
			name: "beacon task claim (agent + status, priority order)",
			sql:  "SELECT * FROM tasks WHERE agent_id = 'x' AND status = 'pending' ORDER BY priority DESC, created_at ASC LIMIT 10",
		},
		{
			name: "stale sweep (status + claimed_at)",
			sql:  "SELECT * FROM tasks WHERE status = 'running' AND claimed_at < '2000-01-01' AND acknowledged_at IS NULL AND delivery_attempts < 3 LIMIT 1000",
		},
		{
			name:           "audit window (newest rows, then reversed in memory)",
			sql:            "SELECT * FROM audit_logs ORDER BY id DESC LIMIT 200",
			allowRowidScan: true,
		},
		{
			name: "task types dropdown (distinct)",
			sql:  "SELECT DISTINCT type FROM tasks WHERE type NOT IN ('ls')",
		},
		{
			name: "task count by agent",
			sql:  "SELECT count(*) FROM tasks WHERE agent_id = 'x'",
		},
		{
			name: "failed task count",
			sql:  "SELECT count(*) FROM tasks WHERE status = 'failed'",
		},
		{
			name: "online agent count",
			sql:  "SELECT count(*) FROM implants WHERE status = 'online'",
		},
		{
			// Tiny table; a temp sort here is cheaper than the index that would avoid it.
			name:          "listener picker",
			sql:           "SELECT * FROM listeners ORDER BY name ASC",
			allowTempSort: true,
		},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			var plan []struct{ Detail string }
			if err := database.Raw("EXPLAIN QUERY PLAN " + q.sql).Scan(&plan).Error; err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
			}
			if len(plan) == 0 {
				t.Fatal("empty query plan; the probe did not run")
			}
			var details []string
			for _, p := range plan {
				details = append(details, p.Detail)
			}
			joined := strings.Join(details, " | ")

			for _, detail := range details {
				if strings.Contains(detail, "TEMP B-TREE") && !q.allowTempSort {
					t.Errorf("sorts in memory for one page (%s)\nplan: %s", detail, joined)
				}
				// A bare "SCAN <table>" means every row is read; an index-backed scan
				// says "USING ... INDEX".
				if strings.HasPrefix(detail, "SCAN ") && !strings.Contains(detail, "USING") && !q.allowTempSort && !q.allowRowidScan {
					t.Errorf("full table scan (%s)\nplan: %s", detail, joined)
				}
			}
		})
	}
}
