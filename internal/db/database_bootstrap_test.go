package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestRedactDSN pins the redaction used when a DSN is logged. A DSN carries the
// database password, so a miss here writes a live credential into the log.
func TestRedactDSN(t *testing.T) {
	cases := []struct {
		in       string
		mustHide []string
		mustKeep []string
	}{
		{
			in:       "host=db.internal user=forge password=s3cr3t dbname=forgec2",
			mustHide: []string{"s3cr3t"},
			mustKeep: []string{"host=db.internal", "user=forge", "dbname=forgec2"},
		},
		{
			in:       "host=localhost pwd=hunter2 dbname=x",
			mustHide: []string{"hunter2"},
			mustKeep: []string{"host=localhost", "dbname=x"},
		},
		{
			in:       "PASSWORD=UPPER port=5432",
			mustHide: []string{"UPPER"},
			mustKeep: []string{"port=5432"},
		},
		{
			in:       "host=localhost dbname=x",
			mustKeep: []string{"host=localhost", "dbname=x"},
		},
		{
			in: "password=",
			// A blank password stays blank; the key is still present.
			mustKeep: []string{"password="},
		},
	}
	for _, tc := range cases {
		got := redactDSN(tc.in)
		for _, secret := range tc.mustHide {
			if strings.Contains(got, secret) {
				t.Errorf("redactDSN(%q) leaked %q: %s", tc.in, secret, got)
			}
		}
		for _, keep := range tc.mustKeep {
			if !strings.Contains(got, keep) {
				t.Errorf("redactDSN(%q) dropped %q: %s", tc.in, keep, got)
			}
		}
	}
}

// TestInitDBCreatesSchemaAndSeedsAdmin covers the real bootstrap against a temp
// file: the migrations run, a schema is created, the default admin exists, and
// calling it twice on the same file is safe (every startup does exactly that).
func TestInitDBCreatesSchemaAndSeedsAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap.db")

	conn, err := InitDB(path, 0 /* info */)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if conn == nil {
		t.Fatal("InitDB returned no connection")
	}

	// The core tables exist.
	for _, table := range []string{"users", "implants", "tasks", "audit_logs", "plugins"} {
		if !conn.Migrator().HasTable(table) {
			t.Errorf("table %q was not created", table)
		}
	}

	// A default admin is seeded and can be found by name.
	var count int64
	if err := conn.Table("users").Where("username = ?", "admin").Count(&count).Error; err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if count != 1 {
		t.Fatalf("default admin rows = %d, want 1", count)
	}

	// Close before reopening: Windows cannot remove a file that is still open,
	// and t.TempDir cleanup would fail on a leaked connection.
	if sqlDB, dbErr := conn.DB(); dbErr == nil {
		_ = sqlDB.Close()
	}

	// Re-running the bootstrap on the same file must not error or duplicate the
	// seed (the server does this on every restart).
	again, err := InitDB(path, 0)
	if err != nil {
		t.Fatalf("second InitDB: %v", err)
	}
	var after int64
	if err := again.Table("users").Where("username = ?", "admin").Count(&after).Error; err != nil {
		t.Fatalf("count admins after restart: %v", err)
	}
	if after != 1 {
		t.Fatalf("default admin rows after restart = %d, want 1", after)
	}
	sqlDB, err := again.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

// TestWithMigrationTxRollsBackOnError pins the migration transaction contract: a
// failing step leaves nothing behind. A half-applied migration without its ID
// row made the next startup replay the step and crash on duplicate keys.
func TestWithMigrationTxRollsBackOnError(t *testing.T) {
	conn := setupTestDB(t)
	type rollbackProbe struct {
		ID   uint `gorm:"primaryKey"`
		Name string
	}
	if err := conn.AutoMigrate(&rollbackProbe{}); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("migration step failed")
	err := withMigrationTx(conn, func(tx *gorm.DB) error {
		if err := tx.Create(&rollbackProbe{Name: "should-not-survive"}).Error; err != nil {
			return err
		}
		return boom
	})
	if err != boom {
		t.Fatalf("withMigrationTx returned %v, want %v", err, boom)
	}
	var count int64
	if err := conn.Table("rollback_probes").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a failed migration left %d row(s) behind", count)
	}

	// A successful step commits.
	if err := withMigrationTx(conn, func(tx *gorm.DB) error {
		return tx.Create(&rollbackProbe{Name: "survives"}).Error
	}); err != nil {
		t.Fatalf("commit path: %v", err)
	}
	if err := conn.Table("rollback_probes").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed rows = %d, want 1", count)
	}
}

// TestMigrationRowExists pins the idempotency probe used by data migrations.
func TestMigrationRowExists(t *testing.T) {
	conn := setupTestDB(t)
	type idemProbe struct {
		ID   uint `gorm:"primaryKey"`
		Code string
	}
	if err := conn.AutoMigrate(&idemProbe{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&idemProbe{Code: "m-1"}).Error; err != nil {
		t.Fatal(err)
	}
	if !migrationRowExists(conn, "idem_probes", "code", "m-1") {
		t.Fatal("an existing row was not found")
	}
	if migrationRowExists(conn, "idem_probes", "code", "m-2") {
		t.Fatal("a missing row was reported as present")
	}
}
