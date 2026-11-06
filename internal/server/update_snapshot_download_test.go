package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"gorm.io/gorm"
)

// TestSnapshotDatabaseForUpdate pins the v2.8 contract: the hot-update path no
// longer produces an inline database snapshot. SQLite (whose VACUUM INTO made
// this cheap) was removed; a PostgreSQL rollback must come from a real external
// snapshot (pg_dump/pgBackRest), never a half-restorable file the update script
// would silently try to restore. So every driver returns an empty snapshot, and
// a snapshot is only ever reported by an operator's own tooling.
func TestSnapshotDatabaseForUpdate(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name   string
		driver string
		path   string
	}{
		{"postgres dsn (no inline snapshot)", "postgres", ""},
		{"postgres with stale path", "postgres", filepath.Join(dir, "should_be_ignored")},
		{"legacy sqlite driver rejected", "sqlite", filepath.Join(dir, "legacy.db")},
		{"empty driver", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Database.Driver = tc.driver
			cfg.Database.Path = tc.path
			// A nil-safe db handle is enough: the function must refuse without
			// dereferencing it (no snapshot means no EXEC on the real database).
			s := &Server{cfg: cfg, db: &gorm.DB{}}
			dbPath, backup := s.snapshotDatabaseForUpdate(dir)
			if backup != "" {
				t.Fatalf("driver %q produced a snapshot %q; hot-update must never inline-snapshot PostgreSQL", tc.driver, backup)
			}
			// The database path is only surfaced when a snapshot actually exists,
			// so a caller cannot restore a file that was never written.
			if dbPath != "" {
				t.Fatalf("driver %q returned a db path %q with no snapshot", tc.driver, dbPath)
			}
		})
	}

	// nil config / nil db are safe no-ops.
	if _, b := (&Server{}).snapshotDatabaseForUpdate(dir); b != "" {
		t.Fatal("nil-everything server produced a snapshot")
	}
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
