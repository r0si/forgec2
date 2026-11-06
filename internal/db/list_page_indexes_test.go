package db

import (
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestListPageIndexesExist pins the two composite indexes that make the console's
// most-loaded pages cheap.
//
// Measured with EXPLAIN over realistic tables: without them PostgreSQL read
// every row of the caller's tenant and sorted it just to produce one page.
//
// A model edit that drops one of these tags would silently restore the scan, so their
// existence is asserted rather than assumed.
func TestListPageIndexesExist(t *testing.T) {
	database, err := gorm.Open(postgres.Open(testPostgresDSN), &gorm.Config{})
	if err != nil {
		t.Skipf("PostgreSQL unavailable for test: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	indexes := func(table string) map[string]bool {
		rows, err := sqlDB.Query("SELECT indexname FROM pg_indexes WHERE tablename = $1", table)
		if err != nil {
			t.Fatalf("pg_indexes(%s): %v", table, err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
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
			t.Errorf("%s is missing on %s: %s, which falls back to a full tenant scan plus a sort",
				want.index, want.table, want.why)
		}
	}
}
