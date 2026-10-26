package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestSnapshotDatabaseForUpdate covers the rollback snapshot: a sqlite database
// is copied with VACUUM INTO and the copy is a real, queryable database.
func TestSnapshotDatabaseForUpdate(t *testing.T) {
	dir := t.TempDir()

	// Non-sqlite driver: no snapshot.
	cfg := &config.Config{}
	cfg.Database.Driver = "postgres"
	s := &Server{cfg: cfg, db: &gorm.DB{}}
	if _, backup := s.snapshotDatabaseForUpdate(dir); backup != "" {
		t.Fatal("a non-sqlite driver produced a snapshot")
	}

	// sqlite but no configured path: no snapshot.
	cfg2 := &config.Config{}
	cfg2.Database.Driver = "sqlite"
	s2 := &Server{cfg: cfg2, db: &gorm.DB{}}
	if _, backup := s2.snapshotDatabaseForUpdate(dir); backup != "" {
		t.Fatal("an empty database path produced a snapshot")
	}

	// A real sqlite file gets a snapshot that contains the data.
	dbPath := filepath.Join(t.TempDir(), "live.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	if err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t (name) VALUES ('row-one')").Error; err != nil {
		t.Fatal(err)
	}

	cfg3 := &config.Config{}
	cfg3.Database.Driver = "sqlite"
	cfg3.Database.Path = dbPath
	s3 := &Server{cfg: cfg3, db: db}
	abs, backup := s3.snapshotDatabaseForUpdate(dir)
	if backup == "" {
		t.Fatal("no snapshot was taken for a real sqlite database")
	}
	if abs != dbPath && filepath.Base(abs) != filepath.Base(dbPath) {
		t.Fatalf("returned database path = %q, want %q", abs, dbPath)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
	backupDB, err := gorm.Open(sqlite.Open(backup), &gorm.Config{})
	if err != nil {
		t.Fatalf("snapshot is not a usable database: %v", err)
	}
	// Windows cannot delete the temp dir while the snapshot connection is
	// open, so close both handles before the test returns.
	if sqlBackup, err := backupDB.DB(); err == nil {
		defer sqlBackup.Close()
	}
	var name string
	if err := backupDB.Raw("SELECT name FROM t WHERE id = 1").Scan(&name).Error; err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if name != "row-one" {
		t.Fatalf("snapshot row = %q", name)
	}
	_ = sqlDB.Close()
}

// TestDownloadUpdateFile covers the download step: a 200 writes the exact bytes
// and reaches 100% progress; a non-200 fails without leaving a file.
func TestDownloadUpdateFile(t *testing.T) {
	payload := []byte("fake-binary-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Length", "19")
			_, _ = w.Write(payload)
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := mustTenantServer(t)

	dest := filepath.Join(t.TempDir(), "beacon.new")
	if err := s.downloadUpdateFile(srv.URL+"/ok", dest, "9.9.9"); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %q, want %q", got, payload)
	}
	s.updateState.mu.RLock()
	stage, percent := s.updateState.Stage, s.updateState.Progress
	s.updateState.mu.RUnlock()
	if stage != updateStageDownloading || percent != 100 {
		t.Fatalf("progress = %s/%d, want downloading/100", stage, percent)
	}

	// A 404 fails and leaves nothing behind.
	missing := filepath.Join(t.TempDir(), "beacon.missing")
	if err := s.downloadUpdateFile(srv.URL+"/nope", missing, "9.9.9"); err == nil {
		t.Fatal("a 404 download succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a failed download left a file behind: %v", err)
	}
}
