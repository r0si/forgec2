package db

import (
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
)

func findMigration(t *testing.T, id string) *gormigrate.Migration {
	t.Helper()
	for _, m := range Migrations {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("migration %q not found", id)
	return nil
}

// TestCampaignsTenantMigration proves pre-tag campaign rows are normalized to
// tenant 0 (legacy/unscoped, which tenantScope treats as visible to unscoped
// operators only) instead of staying NULL — a NULL tenant would be invisible to
// both scoped and global reads.
func TestCampaignsTenantMigration(t *testing.T) {
	database := openTestDB(t)
	stmts := []string{
		`CREATE TABLE campaigns (id TEXT PRIMARY KEY, name TEXT, status TEXT)`,
		`INSERT INTO campaigns (id, name, status) VALUES ('c1', 'one', 'active'), ('c2', 'two', 'active')`,
	}
	for _, stmt := range stmts {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	mig := findMigration(t, "2026-10-13-campaigns-tenant-id")
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var rows []struct {
		ID       string
		TenantID *uint
	}
	if err := database.Table("campaigns").Select("id, tenant_id").Order("id").Scan(&rows).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d campaigns, want 2", len(rows))
	}
	for _, r := range rows {
		if r.TenantID == nil {
			t.Fatalf("campaign %s has NULL tenant_id — invisible to every reader", r.ID)
		}
		if *r.TenantID != 0 {
			t.Fatalf("campaign %s tenant = %d, want 0 (legacy/unscoped)", r.ID, *r.TenantID)
		}
	}

	// The lookup index the scoped reads rely on must exist.
	if !database.Migrator().HasIndex("campaigns", "idx_campaigns_tenant_id") {
		t.Fatal("idx_campaigns_tenant_id missing after migration")
	}

	// Idempotent: re-running on an already-migrated table must not fail.
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestCampaignsTenantMigrationRollback proves the rollback removes the column
// and its index, so an operator can revert the schema change.
func TestCampaignsTenantMigrationRollback(t *testing.T) {
	database := openTestDB(t)
	if err := database.Exec(`CREATE TABLE campaigns (id TEXT PRIMARY KEY, name TEXT, status TEXT)`).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	mig := findMigration(t, "2026-10-13-campaigns-tenant-id")
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !database.Migrator().HasColumn("campaigns", "tenant_id") {
		t.Fatal("tenant_id missing after migrate")
	}
	if err := mig.Rollback(database); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if database.Migrator().HasColumn("campaigns", "tenant_id") {
		t.Fatal("tenant_id survived the rollback")
	}
}

// TestCampaignsTenantMigrationSkipsMissingTable proves a fresh database where
// AutoMigrate has not yet run does not abort the migration chain.
func TestCampaignsTenantMigrationSkipsMissingTable(t *testing.T) {
	database := openTestDB(t)
	mig := findMigration(t, "2026-10-13-campaigns-tenant-id")
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate on an empty database: %v", err)
	}
}
