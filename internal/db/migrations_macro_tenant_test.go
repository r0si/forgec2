package db

import (
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
)

// TestMacroRunsTenantBackfillMigration proves pre-tag macro-run rows inherit
// their parent macro's tenant, and that the migration is idempotent.
func TestMacroRunsTenantBackfillMigration(t *testing.T) {
	database := openTestDB(t)
	// Legacy shape: macro_runs without tenant_id, command_macros with it.
	stmts := []string{
		`CREATE TABLE command_macros (id INTEGER PRIMARY KEY, name TEXT, tenant_id INTEGER DEFAULT 0)`,
		`CREATE TABLE macro_runs (id INTEGER PRIMARY KEY, macro_id INTEGER, macro_name TEXT, status TEXT)`,
		`INSERT INTO command_macros (id, name, tenant_id) VALUES (1, 'm-t1', 1), (2, 'm-t2', 2)`,
		`INSERT INTO macro_runs (id, macro_id, macro_name, status) VALUES (10, 1, 'm-t1', 'running'), (11, 2, 'm-t2', 'completed')`,
	}
	for _, stmt := range stmts {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var mig *gormigrate.Migration
	for _, m := range Migrations {
		if m.ID == "2026-10-10-macro-runs-tenant-id" {
			mig = m
			break
		}
	}
	if mig == nil {
		t.Fatal("macro-runs-tenant-id migration not found")
	}
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var rows []struct {
		ID       uint `gorm:"column:id"`
		TenantID uint `gorm:"column:tenant_id"`
	}
	if err := database.Table("macro_runs").Select("id, tenant_id").Order("id").Scan(&rows).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 runs, got %d", len(rows))
	}
	if rows[0].TenantID != 1 || rows[1].TenantID != 2 {
		t.Fatalf("backfill wrong: run %d -> tenant %d, run %d -> tenant %d",
			rows[0].ID, rows[0].TenantID, rows[1].ID, rows[1].TenantID)
	}

	// Idempotent rerun must not error or double-apply.
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("rerun migrate: %v", err)
	}
	if err := database.Table("macro_runs").Select("id, tenant_id").Order("id").Scan(&rows).Error; err != nil {
		t.Fatalf("read back rerun: %v", err)
	}
	if rows[0].TenantID != 1 || rows[1].TenantID != 2 {
		t.Fatalf("rerun changed values: %+v", rows)
	}
}

// TestMacroRunsTenantBackfillOrphanRunStaysZero proves runs whose macro was
// deleted keep tenant 0 (legacy/unscoped) instead of failing the migration.
func TestMacroRunsTenantBackfillOrphanRunStaysZero(t *testing.T) {
	database := openTestDB(t)
	stmts := []string{
		`CREATE TABLE command_macros (id INTEGER PRIMARY KEY, name TEXT, tenant_id INTEGER DEFAULT 0)`,
		`CREATE TABLE macro_runs (id INTEGER PRIMARY KEY, macro_id INTEGER, macro_name TEXT, status TEXT)`,
		`INSERT INTO macro_runs (id, macro_id, macro_name, status) VALUES (20, 999, 'orphan', 'running')`,
	}
	for _, stmt := range stmts {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var mig *gormigrate.Migration
	for _, m := range Migrations {
		if m.ID == "2026-10-10-macro-runs-tenant-id" {
			mig = m
			break
		}
	}
	if mig == nil {
		t.Fatal("macro-runs-tenant-id migration not found")
	}
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var tenantID int
	if err := database.Table("macro_runs").Select("tenant_id").Where("id = ?", 20).Scan(&tenantID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if tenantID != 0 {
		t.Fatalf("orphan run tenant=%d, want 0 (legacy unscoped)", tenantID)
	}
}
