package db

import (
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrationTargetTable pins the parser that lets a migration failure be classified: an
// ALTER aimed at a table AutoMigrate has not created yet is expected on a legacy upgrade,
// while the same error against an existing table is a genuine ordering bug.
func TestMigrationTargetTable(t *testing.T) {
	cases := map[string]string{
		"ALTER TABLE implants ADD COLUMN pid INTEGER DEFAULT 0": "implants",
		"CREATE INDEX IF NOT EXISTS idx_x ON tasks(status)":     "tasks",
		"INSERT INTO audit_logs (id) VALUES (1)":                "audit_logs",
		"CREATE TABLE `weird` (id INTEGER)":                     "",
		"SELECT 1":                                              "",
		"UPDATE opsrc SET a = 1":                                "",
		"ALTER TABLE \"quoted\" ADD COLUMN x TEXT":              "quoted",
	}
	for sql, want := range cases {
		if got := migrationTargetTable(sql); got != want {
			t.Errorf("migrationTargetTable(%q) = %q, want %q", sql, got, want)
		}
	}
}

// TestMigrationFailuresFailTheRun proves the runner refuses to record a broken migration:
// the failure is surfaced as an error naming the statement, so gormigrate leaves the
// migration unapplied and the next boot retries instead of the index being absent forever.
func TestMigrationFailuresFailTheRun(t *testing.T) {
	resetMigrationFailures()
	if err := migrationFailureError("index migrations"); err != nil {
		t.Fatalf("a clean run reported a failure: %v", err)
	}

	noteMigrationFailure("idx_missing", errTestMigration)
	err := migrationFailureError("index migrations")
	if err == nil {
		t.Fatal("a recorded failure did not fail the run")
	}
	if got := err.Error(); !containsAll(got, "idx_missing", "1 migration statement") {
		t.Fatalf("error does not name the statement: %v", err)
	}
	// The counter resets, so one failure cannot be reported twice.
	if err := migrationFailureError("index migrations"); err != nil {
		t.Fatalf("the failure was reported again: %v", err)
	}
}

// TestMigrationSkipsTablesAutoMigrateCreates covers the legacy-upgrade path the change
// exposed: a schema migration that ALTERs a table this installation does not have yet must
// not fail the run, because AutoMigrate creates it (columns included) immediately after.
func TestMigrationSkipsTablesAutoMigrateCreates(t *testing.T) {
	database, err := InitDB(filepath.Join(t.TempDir(), "legacy.db"), slog.LevelError)
	if err != nil {
		t.Fatalf("fresh InitDB: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// A table that does not exist: skipped, not recorded.
	resetMigrationFailures()
	execMigration(database, "ALTER TABLE table_that_never_existed ADD COLUMN x TEXT", "ghost")
	if err := migrationFailureError("schema migrations"); err != nil {
		t.Fatalf("a not-yet-created table failed the run: %v", err)
	}

	// A statement that fails for a real reason is still recorded. (Adding an existing
	// column would be "duplicate column" and therefore ignorable, so use SQL that is not.)
	resetMigrationFailures()
	execMigration(database, "ALTER TABLE tasks ADD COLUMN", "bad_alter")
	if err := migrationFailureError("schema migrations"); err == nil {
		t.Fatal("a genuinely failed statement was not recorded")
	}
}

var errTestMigration = errTestMigrationType{}

type errTestMigrationType struct{}

func (errTestMigrationType) Error() string { return "boom" }

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
