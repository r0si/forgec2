package db_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestInitDBWithDriverPostgresErrorDoesNotLeakDSN pins the failure contract of the
// branch that cannot be exercised without a real server: a bad PostgreSQL DSN must
// return an error, not panic - and the error must not echo the DSN, which carries
// the database password.
func TestInitDBWithDriverPostgresErrorDoesNotLeakDSN(t *testing.T) {
	const dsn = "postgres://forge:SUPERSECRET@127.0.0.1:1/none"
	_, err := db.InitDBWithDriver("postgres", dsn, "unused.db", slog.LevelWarn, 5, 2, 0)
	if err == nil {
		t.Fatal("a broken PostgreSQL DSN reported success")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("the DSN password leaked into the error: %v", err)
	}
}
