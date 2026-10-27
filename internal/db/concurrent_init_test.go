package db

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentInitDBIsSafe pins the ops scenario the deploy scripts can produce:
// a restart racing a start, so two processes run the migrations against the same
// file at the same time. SQLite will make one of them wait or fail, but the outcome
// must be a usable database with the seed applied exactly once - never a panic, a
// half-applied migration or a duplicated admin row.
func TestConcurrentInitDBIsSafe(t *testing.T) {
	// os.MkdirTemp rather than t.TempDir: closing every pool is best effort on
	// Windows (a lingering handle makes RemoveAll fail), and losing the assertion
	// because cleanup could not unlink a file would be the wrong failure.
	dir, err := os.MkdirTemp("", "forgec2-race-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "race.db")

	const racers = 3
	var wg sync.WaitGroup
	errs := make([]error, racers)
	conns := make([]interface{ Close() error }, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			conn, err := InitDBWithDriver("", "", path, slog.LevelError, 5, 2, time.Minute)
			errs[n] = err
			if err == nil && conn != nil {
				if sqlDB, derr := conn.DB(); derr == nil {
					conns[n] = sqlDB
				}
			}
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatalf("every concurrent InitDB failed: %v", errs)
	}
	// Close the racers' pools before reopening: Windows cannot remove a file that
	// is still open.
	for _, c := range conns {
		if c != nil {
			_ = c.Close()
		}
	}

	// Reopen cleanly and assert the outcome.
	conn, err := InitDB(path, slog.LevelError)
	if err != nil {
		t.Fatalf("reopen after the race: %v", err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// The migrations are applied - not half-applied.
	for _, table := range []string{"users", "implants", "tasks", "audit_logs"} {
		if !conn.Migrator().HasTable(table) {
			t.Fatalf("table %q is missing after the race", table)
		}
	}
	// The seed ran exactly once: no duplicate admin from two racers seeding.
	var admins int64
	if err := conn.Table("users").Where("username = ?", "admin").Count(&admins).Error; err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("admin rows = %d after %d concurrent starts, want 1", admins, racers)
	}
}
