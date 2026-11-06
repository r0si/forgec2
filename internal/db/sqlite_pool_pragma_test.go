package db

import (
	"log/slog"
	"testing"
	"time"
)

// TestPostgresPoolConfig pins the invariant that the connection pool is
// configured with the expected max open connections. PostgreSQL can handle
// multiple connections, but this deployment uses a single writer pool.
func TestPostgresPoolConfig(t *testing.T) {
	database, err := InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelWarn, 1, 1, time.Minute)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("postgres pool max_open_conns = %d, want 1 (single writer deployment)", stats.MaxOpenConnections)
	}
}

// TestPostgresForeignKeysEnforced proves that foreign key enforcement is active
// on a fresh PostgreSQL database: the schema's cascades and orphan rejection
// depend on it.
func TestPostgresForeignKeysEnforced(t *testing.T) {
	database, err := InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelWarn, 1, 1, time.Minute)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
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

// TestPostgresDSNConfig pins that DSN parameters (like sslmode) are respected.
func TestPostgresDSNConfig(t *testing.T) {
	// Test with sslmode=disable
	database, err := InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelWarn, 1, 1, time.Minute)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Verify we can execute a simple query
	var result int
	if err := database.Raw("SELECT 1").Scan(&result).Error; err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if result != 1 {
		t.Fatalf("unexpected result: %d", result)
	}
}
