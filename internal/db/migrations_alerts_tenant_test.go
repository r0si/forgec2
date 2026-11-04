package db

import (
	"testing"

	"gorm.io/gorm"
)

// alertMigrationID is the migration that stamps the alerting tables.
const alertMigrationID = "2026-10-14-alerts-tenant-id"

func runAlertMigration(t *testing.T, database *gorm.DB) {
	t.Helper()
	mig := findMigration(t, alertMigrationID)
	if err := mig.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func tenantOf(t *testing.T, database *gorm.DB, table, id string) uint {
	t.Helper()
	var row struct {
		TenantID *uint
	}
	if err := database.Table(table).Select("id, tenant_id").Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("read %s/%s: %v", table, id, err)
	}
	if row.TenantID == nil {
		t.Fatalf("%s/%s has NULL tenant_id — invisible to every reader", table, id)
	}
	return *row.TenantID
}

// TestAlertsTenantBackfillFromAgentMigration proves a pre-tag alert inherits the
// alerting implant's tenant, and that alerts with no live agent (server-wide
// metrics, or an agent that was deleted) are normalized to tenant 0 rather than
// left NULL — a NULL would be invisible to both scoped and global readers.
func TestAlertsTenantBackfillFromAgentMigration(t *testing.T) {
	database := openTestDB(t)
	stmts := []string{
		`CREATE TABLE implants (id TEXT PRIMARY KEY, tenant_id INTEGER DEFAULT 0)`,
		`CREATE TABLE alert_rules (id INTEGER PRIMARY KEY, name TEXT, type TEXT)`,
		`CREATE TABLE alerts (id INTEGER PRIMARY KEY, rule_id INTEGER, source TEXT)`,
		`INSERT INTO implants (id, tenant_id) VALUES ('a-7', 7), ('a-9', 9)`,
		`INSERT INTO alert_rules (id, name, type) VALUES (1, 'offline', 'agent_offline')`,
		`INSERT INTO alerts (id, rule_id, source) VALUES
			(10, 1, 'a-7'),
			(11, 1, 'a-9'),
			(12, 1, 'deleted-agent'),
			(13, 1, 'system')`,
	}
	for _, stmt := range stmts {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	runAlertMigration(t, database)

	if got := tenantOf(t, database, "alerts", "10"); got != 7 {
		t.Fatalf("alert for a-7 tenant = %d, want 7", got)
	}
	if got := tenantOf(t, database, "alerts", "11"); got != 9 {
		t.Fatalf("alert for a-9 tenant = %d, want 9", got)
	}
	if got := tenantOf(t, database, "alerts", "12"); got != 0 {
		t.Fatalf("alert for a deleted agent = %d, want 0 (legacy/unscoped)", got)
	}
	if got := tenantOf(t, database, "alerts", "13"); got != 0 {
		t.Fatalf("server-wide alert = %d, want 0", got)
	}
	// Rules predate stamping and have no parent row, so they stay 0.
	if got := tenantOf(t, database, "alert_rules", "1"); got != 0 {
		t.Fatalf("rule tenant = %d, want 0", got)
	}

	// Both scoped lookup indexes must exist.
	for _, index := range []string{"idx_alerts_tenant_id", "idx_alert_rules_tenant_id"} {
		table := "alerts"
		if index == "idx_alert_rules_tenant_id" {
			table = "alert_rules"
		}
		if !database.Migrator().HasIndex(table, index) {
			t.Fatalf("%s missing after migration", index)
		}
	}

	// Idempotent: re-running must not fail or change the values.
	runAlertMigration(t, database)
	if got := tenantOf(t, database, "alerts", "10"); got != 7 {
		t.Fatalf("second migrate moved alert 10 to tenant %d", got)
	}
}

// TestAlertsTenantMigrationRollback proves the migration can be reverted.
func TestAlertsTenantMigrationRollback(t *testing.T) {
	database := openTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE alert_rules (id INTEGER PRIMARY KEY, name TEXT, type TEXT)`,
		`CREATE TABLE alerts (id INTEGER PRIMARY KEY, rule_id INTEGER, source TEXT)`,
	} {
		if err := database.Exec(stmt).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	runAlertMigration(t, database)
	if !database.Migrator().HasColumn("alerts", "tenant_id") || !database.Migrator().HasColumn("alert_rules", "tenant_id") {
		t.Fatal("tenant_id missing after migrate")
	}

	mig := findMigration(t, alertMigrationID)
	if err := mig.Rollback(database); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if database.Migrator().HasColumn("alerts", "tenant_id") {
		t.Fatal("alerts.tenant_id survived the rollback")
	}
	if database.Migrator().HasColumn("alert_rules", "tenant_id") {
		t.Fatal("alert_rules.tenant_id survived the rollback")
	}
}

// TestAlertsTenantMigrationSkipsMissingTables proves the chain still runs on a
// fresh database where AutoMigrate has not created the tables yet, and on a
// database that has alert_rules but not alerts.
func TestAlertsTenantMigrationSkipsMissingTables(t *testing.T) {
	database := openTestDB(t)
	runAlertMigration(t, database)

	if database.Migrator().HasTable("alert_rules") {
		t.Fatal("the migration created alert_rules on a database that never had it")
	}
	if database.Migrator().HasTable("alerts") {
		t.Fatal("the migration created alerts on a database that never had it")
	}

	partial := openTestDB(t)
	if err := partial.Exec(`CREATE TABLE alert_rules (id INTEGER PRIMARY KEY, name TEXT, type TEXT)`).Error; err != nil {
		t.Fatalf("seed partial: %v", err)
	}
	runAlertMigration(t, partial)
	if !partial.Migrator().HasColumn("alert_rules", "tenant_id") {
		t.Fatal("alert_rules.tenant_id missing when alerts table is absent")
	}
	if partial.Migrator().HasTable("alerts") {
		t.Fatal("the migration created a missing alerts table")
	}
}
