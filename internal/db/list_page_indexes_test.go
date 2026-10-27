package db

import (
	"log/slog"
	"path/filepath"
	"testing"
)

// TestListPageIndexesExist pins the two composite indexes that make the console's
// most-loaded pages cheap.
//
// Measured with EXPLAIN QUERY PLAN over realistic tables: without them SQLite read
// every row of the caller's tenant and sorted it in a TEMP B-TREE just to produce one
// page.
//
//	tasks    WHERE tenant_id = ? ORDER BY created_at DESC LIMIT 50   983µs -> 555µs
//	implants WHERE tenant_id = ? ORDER BY last_seen  DESC LIMIT 100  also lost its
//	                                                                 TEMP B-TREE
//
// A model edit that drops one of these tags would silently restore the scan, so their
// existence is asserted rather than assumed.
func TestListPageIndexesExist(t *testing.T) {
	database, err := InitDB(filepath.Join(t.TempDir(), "idx.db"), slog.LevelError)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	indexes := func(table string) map[string]bool {
		rows, err := sqlDB.Query("PRAGMA index_list('" + table + "')")
		if err != nil {
			t.Fatalf("index_list(%s): %v", table, err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var seq, unique, partial int
			var name, origin string
			if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	for _, want := range []struct{ table, index, why string }{
		{"tasks", "idx_tasks_tenant_created", "the task list filters by tenant and orders by created_at"},
		{"implants", "idx_implants_tenant_seen", "the agent list filters by tenant and orders by last_seen"},
	} {
		if !indexes(want.table)[want.index] {
			t.Errorf("%s is missing on %s: %s, which falls back to a full tenant scan plus a TEMP B-TREE sort",
				want.index, want.table, want.why)
		}
	}
}
