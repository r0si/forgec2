package db

import (
	"log/slog"
	"path/filepath"
	"testing"
)

// TestPragmaFailuresDoNotLookLikeMigrations pins the split introduced after migrations started
// failing closed: a pragma is not a migration, so a failing pragma must not enter the
// migration-failure counter (a later reader would hunt for broken DDL that does not exist) -
// and its own log line has to say what actually happened.
func TestPragmaFailuresDoNotLookLikeMigrations(t *testing.T) {
	database, err := InitDB(filepath.Join(t.TempDir(), "pragma.db"), slog.LevelError)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	resetMigrationFailures()
	execPragma(database, "PRAGMA this_pragma_does_not_exist = 1;", "pragma_bogus")
	if err := migrationFailureError("schema migrations"); err != nil {
		t.Fatalf("a failed pragma was counted as a failed migration: %v", err)
	}

	// A migration failure is still counted, so the split did not defang the earlier fix.
	resetMigrationFailures()
	execMigration(database, "ALTER TABLE tasks ADD COLUMN", "bad_alter")
	if err := migrationFailureError("schema migrations"); err == nil {
		t.Fatal("a failed migration statement stopped being counted")
	}
}

// TestForeignKeysSurviveStartup proves the pragma that matters most is actually applied on a
// fresh database: the schema's cascades and orphan rejection depend on it, and it used to be a
// bare Exec whose error was discarded.
func TestForeignKeysSurviveStartup(t *testing.T) {
	database, err := InitDB(filepath.Join(t.TempDir(), "fk.db"), slog.LevelError)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	var fk int
	if err := database.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d on a fresh database, want 1", fk)
	}
}
