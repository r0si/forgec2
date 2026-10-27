package db

import (
	"log/slog"

	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

// TestSQLitePoolIsPinnedToOne pins the invariant the whole transaction rule rests
// on: SQLite runs on a single writer connection no matter what the caller asks
// for. If this silently widened, every `s.db` query inside `s.db.Transaction`
// would wait for the connection the transaction itself holds (the bug the
// check:tx-queries gate exists to prevent).
func TestSQLitePoolIsPinnedToOne(t *testing.T) {
	database, err := InitDBWithDriver("", "", filepath.Join(t.TempDir(), "pool.db"),
		slog.LevelWarn, 25 /* requested max open */, 5, time.Minute)
	if err != nil {
		t.Fatalf("InitDBWithDriver: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Windows cannot remove a file that is still open, and t.TempDir cleans up
	// after this test.
	t.Cleanup(func() { _ = sqlDB.Close() })
	if got := sqlDB.Stats().MaxOpenConnections; got != SQLiteMaxOpenConns {
		t.Fatalf("sqlite pool = %d, want %d (the single-writer invariant)", got, SQLiteMaxOpenConns)
	}
}

// TestSQLiteDSNWithQueryKeepsPragmas pins the fix for a silent integrity loss: a
// path that already carries a query string (file:…?cache=shared, or a Windows
// path with a "?") used to skip the pragmas entirely, so foreign_keys was off and
// orphan rows were accepted without a word.
func TestSQLiteDSNWithQueryKeepsPragmas(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "query.db") + "?cache=shared"
	database, err := InitDBWithDriver("", "", dbPath, slog.LevelWarn, 5, 2, time.Minute)
	if err != nil {
		t.Fatalf("InitDBWithDriver: %v", err)
	}
	t.Cleanup(func() { closeTestDB(database) })
	if err := database.Exec(`CREATE TABLE q_parent (id INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`CREATE TABLE q_child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES q_parent(id))`).Error; err != nil {
		t.Fatal(err)
	}
	// The referenced parent does not exist: foreign_keys must reject this.
	if res := database.Exec(`INSERT INTO q_child (id, parent_id) VALUES (1, 999)`); res.Error == nil {
		t.Fatal("an orphan row was accepted: the pragmas were skipped for a DSN with a query string")
	}
}

// TestSQLiteForeignKeysAreAlwaysOn pins the contract on the other side: the server
// REQUIRES foreign-key enforcement, so an explicit `_pragma=foreign_keys(0)` in a
// DSN is overridden by the one-time Exec (the schema relies on cascades). Operator
// pragmas alone must not be able to turn integrity off.
func TestSQLiteForeignKeysAreAlwaysOn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "explicit.db") + "?_pragma=foreign_keys(0)"
	database, err := InitDBWithDriver("", "", dbPath, slog.LevelWarn, 5, 2, time.Minute)
	if err != nil {
		t.Fatalf("InitDBWithDriver: %v", err)
	}
	t.Cleanup(func() { closeTestDB(database) })
	var fk int
	if err := database.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1: enforcement must not be switchable off from a DSN", fk)
	}
}

// closeTestDB releases the pool so a Windows temp dir can be removed.
func closeTestDB(gdb *gorm.DB) {
	if sqlDB, err := gdb.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
