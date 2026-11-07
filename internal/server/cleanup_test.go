package server

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"gorm.io/gorm"
)

// testPostgresDSN matches the internal/db test harness: the default local
// PostgreSQL instance used when a test must exercise the real migration path.
const testPostgresDSN = "host=localhost user=forgec2 password=forgec2 dbname=forgec2 port=5432 sslmode=disable"

// newCleanupTestDB returns a migrated database for the cleanup/monitor tests.
// The cleanup and status-sweep logic is plain gorm ORM (portable across
// drivers), so it runs on the shared in-memory SQLite harness like the rest of
// the server suite rather than requiring a live PostgreSQL — the old
// InitDBWithDriver call resolved an empty driver to "postgres" and made every
// test that touched this helper fail with a connection error on machines with
// no PostgreSQL, while asserting nothing driver-specific.
func newCleanupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupTestDB(t)
}

func TestCleanupOldDataPurgesStaleSystemMetrics(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.CleanupRetentionDays = 30
	cfg.Server.DataDir = t.TempDir()
	s := &Server{db: newCleanupTestDB(t), cfg: cfg}

	now := time.Now()
	stale := db.SystemMetric{CPULoad: 1, CreatedAt: now.AddDate(0, 0, -120)}
	fresh := db.SystemMetric{CPULoad: 2, CreatedAt: now.Add(-time.Minute)}
	if err := s.db.Create(&stale).Error; err != nil {
		t.Fatalf("seed stale metric: %v", err)
	}
	if err := s.db.Create(&fresh).Error; err != nil {
		t.Fatalf("seed fresh metric: %v", err)
	}

	s.cleanupOldData()

	var remaining int64
	if err := s.db.Model(&db.SystemMetric{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count metrics: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected 1 metric to survive cleanup, got %d", remaining)
	}

	var kept db.SystemMetric
	if err := s.db.Order("id").First(&kept).Error; err != nil {
		t.Fatalf("read surviving metric: %v", err)
	}
	if int(kept.CPULoad) != 2 {
		t.Errorf("expected fresh metric to survive, got CPULoad=%v", kept.CPULoad)
	}
}

func TestCleanupOldDataPurgesTerminalTasks(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.CleanupRetentionDays = 30
	cfg.Server.DataDir = t.TempDir()
	s := &Server{db: newCleanupTestDB(t), cfg: cfg}

	if err := s.db.Create(&db.Implant{ID: "a", LastSeen: time.Now()}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	old := time.Now().AddDate(0, 0, -120)
	fresh := time.Now().Add(-time.Minute)
	seed := []db.Task{
		{AgentID: "a", Type: "shell", Status: "cancelled", CreatedAt: old},
		{AgentID: "a", Type: "shell", Status: "sent", CreatedAt: old},
		{AgentID: "a", Type: "shell", Status: "completed", CreatedAt: old},
		{AgentID: "a", Type: "shell", Status: "cancelled", CreatedAt: fresh},
		{AgentID: "a", Type: "shell", Status: "pending", CreatedAt: old},
	}
	for i := range seed {
		if err := s.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}

	s.cleanupOldData()

	var remaining []db.Task
	if err := s.db.Find(&remaining).Error; err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining=%d, want 2 (fresh cancelled + old pending)", len(remaining))
	}
	for _, task := range remaining {
		if task.Status != "cancelled" && task.Status != "pending" {
			t.Fatalf("unexpected survivor: %+v", task)
		}
	}
}

// newMigratedDB runs the REAL schema migration against PostgreSQL and returns
// the handle, skipping when PostgreSQL is unavailable. Tests that assert a
// migration created an index must exercise the migration itself: AutoMigrate on
// the shared test harness does not run the raw CREATE INDEX statements in
// migrations.go, so an index-only guarantee can only be checked this way.
func newMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := db.InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelWarn, 1, 1, time.Minute)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return gdb
}

// newMigratedDBB is the benchmark twin of newMigratedDB: same migrated
// handle, but *testing.B has no Skipf. Callers skip themselves when the
// returned error is non-nil.
func newMigratedDBB(b *testing.B) (*gorm.DB, error) {
	b.Helper()
	gdb, err := db.InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelWarn, 1, 1, time.Minute)
	if err != nil {
		return nil, err
	}
	b.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return gdb, nil
}

func TestSystemMetricsCreatedAtIndexApplied(t *testing.T) {
	gdb := newMigratedDB(t)

	// PostgreSQL exposes indexes through pg_indexes, not SQLite's PRAGMA.
	var count int64
	if err := gdb.Raw(
		"SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'system_metrics' AND indexname = 'idx_system_metrics_created_at'",
	).Scan(&count).Error; err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	if count == 0 {
		t.Fatal("expected idx_system_metrics_created_at index to exist after migration")
	}
}

// TestOfflineThresholdWarnIsRateLimited pins the log-volume fix: the getter
// runs on every agent list, dashboard poll, metrics scrape and status
// recompute, so an unthrottled Warn drowned a misconfigured server in its own
// diagnostic (thousands of lines per minute). One warning per gap, and the
// default is still returned.
func TestOfflineThresholdWarnIsRateLimited(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := &config.Config{}
	cfg.Server.OfflineThreshold = 0 // invalid → warn path
	s := &Server{cfg: cfg}

	const calls = 5000
	for i := 0; i < calls; i++ {
		if got := s.offlineThreshold(); got != time.Duration(DefaultOfflineThresholdSec)*time.Second {
			t.Fatalf("offlineThreshold() = %v, want the default", got)
		}
	}

	warns := bytes.Count(buf.Bytes(), []byte("OfflineThreshold is invalid"))
	if warns != 1 {
		t.Fatalf("got %d warnings across %d calls, want exactly 1 (throttled)", warns, calls)
	}
}
