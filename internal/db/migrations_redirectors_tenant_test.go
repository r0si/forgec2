package db

import (
	"testing"
)

// TestRedirectorsTenantMigration proves pre-tag redirector rows are normalized
// to tenant 0 (legacy/unscoped, which tenantScope treats as visible to unscoped
// operators only) instead of staying NULL, and that the migration is idempotent.
func TestRedirectorsTenantMigration(t *testing.T) {
	database := openTestDB(t)
	stmts := []string{
		`CREATE TABLE redirectors (id INTEGER PRIMARY KEY, name TEXT, host TEXT)`,
		`INSERT INTO redirectors (id, name, host) VALUES (1, 'one', '10.0.0.1'), (2, 'two', '10.0.0.2')`,
	}
	for _, stmt := range stmts {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	mig := findMigration(t, "2026-10-15-redirectors-tenant-id")
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var rows []struct {
		ID       int
		TenantID *uint
	}
	if err := database.Table("redirectors").Select("id, tenant_id").Order("id").Scan(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d redirectors, want 2", len(rows))
	}
	for i, r := range rows {
		if r.TenantID == nil {
			t.Fatalf("redirector %d has NULL tenant_id — invisible to every reader", r.ID)
		}
		if *r.TenantID != 0 {
			t.Fatalf("redirector %d tenant = %d, want 0 (legacy/unscoped)", r.ID, *r.TenantID)
		}
		_ = i
	}

	// The lookup index the scoped reads rely on must exist.
	if !database.Migrator().HasIndex("redirectors", "idx_redirectors_tenant_id") {
		t.Fatal("idx_redirectors_tenant_id missing after migration")
	}

	// Idempotent: re-running on an already-migrated table must not fail.
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := tenantOf(t, database, "redirectors", "1"); got != 0 {
		t.Fatalf("second migrate moved redirector 1 to tenant %d", got)
	}
}

// TestRedirectorsTenantMigrationRollback proves the rollback removes the column
// and its index, so an operator can revert the schema change.
func TestRedirectorsTenantMigrationRollback(t *testing.T) {
	database := openTestDB(t)
	if err := database.Exec(`CREATE TABLE redirectors (id INTEGER PRIMARY KEY, name TEXT, host TEXT)`).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	mig := findMigration(t, "2026-10-15-redirectors-tenant-id")
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !database.Migrator().HasColumn("redirectors", "tenant_id") {
		t.Fatal("tenant_id missing after migrate")
	}
	if err := mig.Rollback(database); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if database.Migrator().HasColumn("redirectors", "tenant_id") {
		t.Fatal("tenant_id survived the rollback")
	}
}

// TestRedirectorsTenantMigrationSkipsMissingTable proves the chain still runs on
// a fresh database where AutoMigrate has not yet created the table.
func TestRedirectorsTenantMigrationSkipsMissingTable(t *testing.T) {
	database := openTestDB(t)
	mig := findMigration(t, "2026-10-15-redirectors-tenant-id")
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate on an empty database: %v", err)
	}
	if database.Migrator().HasTable("redirectors") {
		t.Fatal("the migration created a table it should have skipped")
	}
}
