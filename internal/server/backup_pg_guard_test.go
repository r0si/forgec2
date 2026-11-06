package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// newFakePostgresDB builds a *gorm.DB whose dialector reports "postgres"
// without a live server (automatic ping disabled). The maintenance guards
// must key off driver identity, not connectivity.
func newFakePostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=none dbname=none sslmode=disable connect_timeout=1"),
		&gorm.Config{
			Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
			DisableAutomaticPing:   true,
			SkipDefaultTransaction: true,
		})
	if err != nil {
		t.Skipf("postgres dialector unavailable: %v", err)
	}
	return conn
}

func operatorCtxFor(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, nil)
	c.Request.Header.Set("Accept", "application/json")
	c.Set("user", "pg-admin")
	c.Set("user_role", "admin")
	return c, w
}

// TestSQLiteOnlyMaintenanceRefusedOnPostgres proves backup and restore answer
// 501 with actionable guidance on a non-SQLite driver instead of failing with a
// cryptic SQL error (or "restoring" a file nothing reads). VACUUM is NOT in this
// list: the maintenance endpoint was rewritten to run PostgreSQL's ANALYZE and
// report pg_database_size, so refusing it on the only production driver would
// make the button dead in every real deployment (it is covered by
// TestDBVacuumRunsAnalyzeOnPostgres instead).
func TestSQLiteOnlyMaintenanceRefusedOnPostgres(t *testing.T) {
	ginSetTestMode(t)
	s := &Server{db: newFakePostgresDB(t), cfg: config.DefaultConfig()}

	c, w := operatorCtxFor(http.MethodPost, "/settings/db/backup")
	s.handleDBBackup(c)
	assertNotImplemented(t, "backup", w)

	c, w = operatorCtxFor(http.MethodPost, "/settings/db/restore")
	s.handleDBRestore(c)
	assertNotImplemented(t, "restore", w)
}

// TestDBVacuumRunsAnalyzeOnPostgres pins the intended behavior: the maintenance
// endpoint works on PostgreSQL (the production driver), running ANALYZE and
// reporting a real size, instead of answering 501 like a driver it no longer
// supports. Skipped when no PostgreSQL is reachable.
func TestDBVacuumRunsAnalyzeOnPostgres(t *testing.T) {
	ginSetTestMode(t)
	gdb, err := db.InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelWarn, 1, 1, time.Minute)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	s := &Server{db: gdb, cfg: config.DefaultConfig()}

	c, w := operatorCtxFor(http.MethodPost, "/settings/maintenance/vacuum")
	s.handleDBVacuum(c)
	if w.Code != http.StatusOK {
		t.Fatalf("vacuum on postgres = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "SQLite only") {
		t.Fatalf("vacuum still refuses postgres: %s", w.Body.String())
	}
	// size is advisory but must be reported as a field.
	if !strings.Contains(w.Body.String(), "\"size\"") {
		t.Fatalf("vacuum response missing size field: %s", w.Body.String())
	}
}

func assertNotImplemented(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("%s: status=%d, want 501; body=%s", name, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SQLite only") {
		t.Fatalf("%s: body lacks the SQLite-only explanation: %s", name, w.Body.String())
	}
}

// TestBackupManagerRefusesNonSQLite proves the scheduled path refuses too,
// instead of logging a cryptic VACUUM failure on every tick.
func TestBackupManagerRefusesNonSQLite(t *testing.T) {
	bm := &BackupManager{db: newFakePostgresDB(t), backupDir: t.TempDir()}
	if _, _, err := bm.createBackup(); !errors.Is(err, errNonSQLiteBackup) {
		t.Fatalf("createBackup error = %v, want errNonSQLiteBackup", err)
	}
	if err := bm.Start("daily"); !errors.Is(err, errNonSQLiteBackup) {
		t.Fatalf("Start error = %v, want errNonSQLiteBackup", err)
	}
}

// TestSQLiteMaintenanceStillWorks guards the guard: SQLite deployments keep the
// full backup path.
func TestSQLiteMaintenanceStillWorks(t *testing.T) {
	ginSetTestMode(t)
	database := testutil.SetupTestDB(t)
	if !isSQLiteDB(database) {
		t.Skip("test database is not SQLite")
	}
	cfg := config.DefaultConfig()
	cfg.Server.DataDir = t.TempDir()
	s := &Server{db: database, cfg: cfg}

	c, w := operatorCtxFor(http.MethodPost, "/settings/db/backup")
	if !s.requireSQLiteDB(c) {
		t.Fatalf("SQLite deployment refused maintenance: %s", w.Body.String())
	}
}

// TestIsSQLiteDBHandlesNil guards the helper against a nil handle (callers
// include bare test servers).
func TestIsSQLiteDBHandlesNil(t *testing.T) {
	if isSQLiteDB(nil) {
		t.Fatal("nil db must not be treated as SQLite")
	}
}

// keep time imported for the gorm open options above on all build paths.
var _ = time.Second
