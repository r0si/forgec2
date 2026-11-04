package db

import (
	"log/slog"
	"testing"
)

// TestForeignKeysSurviveStartup proves that foreign key enforcement is active on a
// fresh PostgreSQL database: the schema's cascades and orphan rejection depend on it.
func TestForeignKeysSurviveStartup(t *testing.T) {
	database, err := InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelError, 1, 1, 0)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Create a parent table and a child table with a foreign key
	_ = database.Exec(`CREATE TABLE fk_parent (id INTEGER PRIMARY KEY)`).Error
	_ = database.Exec(`CREATE TABLE fk_child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES fk_parent(id))`).Error
	_ = database.Exec(`INSERT INTO fk_parent (id) VALUES (1)`).Error

	// The referenced parent does not exist: foreign key must reject this
	if res := database.Exec(`INSERT INTO fk_child (id, parent_id) VALUES (1, 999)`); res.Error == nil {
		t.Fatal("an orphan row was accepted: foreign key enforcement is not active")
	}
}

// TestMigrationFailureTracking pins the behavior that failed migrations are counted
// and failed pragmas/non-DDL statements are not.
func TestMigrationFailureTracking(t *testing.T) {
	database, err := InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelError, 1, 1, 0)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	resetMigrationFailures()
	execMigration(database, "INVALID SQL STATEMENT", "bad_alter")
	if err := migrationFailureError("schema migrations"); err == nil {
		t.Fatal("a failed migration statement stopped being counted")
	}

	// Non-migration statements should not be counted
	resetMigrationFailures()
	execMigration(database, "SELECT 1", "not_a_migration")
	if err := migrationFailureError("schema migrations"); err != nil {
		t.Fatalf("a SELECT was counted as a failed migration: %v", err)
	}
}