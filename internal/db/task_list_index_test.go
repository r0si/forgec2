package db

import (
	"log/slog"
	"path/filepath"
	"testing"
)

// TestTaskListIndexExists pins the index that makes the operator task list cheap.
//
// Measured on 4000 tasks across two tenants: "WHERE tenant_id = ? ORDER BY
// created_at DESC LIMIT 50" took 983µs per query without it (SQLite read the whole
// tenant and sorted it in a TEMP B-TREE) and 555µs with it, where the index supplies
// the order and the limit stops the scan. A future model edit that drops the tag
// would silently restore the scan, so the index's existence is asserted.
func TestTaskListIndexExists(t *testing.T) {
	database, err := InitDB(filepath.Join(t.TempDir(), "idx.db"), slog.LevelError)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	rows, err := sqlDB.Query("PRAGMA index_list('tasks')")
	if err != nil {
		t.Fatalf("index_list: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "idx_tasks_tenant_created" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("idx_tasks_tenant_created is missing: the task list falls back to a full tenant scan plus a TEMP B-TREE sort")
	}
}
