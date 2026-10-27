package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func backupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live.db")
	conn, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&db.User{}, &db.Implant{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&db.User{Username: "backup-probe", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := conn.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return conn
}

// TestCreateBackupProducesEncryptedRestorableSnapshot covers the core promise:
// the .fbk is encrypted (no SQLite header in the file), decrypts back to a
// database that still contains the data, and carries a sidecar naming the key.
func TestCreateBackupProducesEncryptedRestorableSnapshot(t *testing.T) {
	conn := backupTestDB(t)
	backupDir := t.TempDir()
	key := strings.Repeat("ab", 32)
	bm, err := NewBackupManager(conn, "", backupDir, key)
	if err != nil {
		t.Fatal(err)
	}
	bm.Sidecar = func() backupSidecar {
		return backupSidecar{KeyID: bm.keyID, ServerVersion: "test"}
	}

	name, size, err := bm.createBackup()
	if err != nil {
		t.Fatalf("createBackup: %v", err)
	}
	if !strings.HasPrefix(name, "forgec2_backup_") || !strings.HasSuffix(name, ".fbk") {
		t.Fatalf("backup name = %q", name)
	}
	if size <= 0 {
		t.Fatalf("backup size = %d", size)
	}
	path := filepath.Join(backupDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != int(size) {
		t.Fatalf("size mismatch: file %d, reported %d", len(raw), size)
	}
	// Not a plaintext SQLite file.
	if strings.HasPrefix(string(raw), "SQLite format 3") {
		t.Fatal("backup was written in plaintext")
	}

	// It decrypts back and still holds the row.
	plain, err := bm.decrypt(raw)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !strings.HasPrefix(string(plain), "SQLite format 3") {
		t.Fatalf("decrypted payload is not a sqlite database: %q", string(plain[:16]))
	}
	sidecarPath := path + ".meta.json"
	if _, err := os.Stat(sidecarPath); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
	meta, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), bm.keyID) {
		t.Fatalf("sidecar does not name the key id: %s", meta)
	}

	// No snapshot temp file is left behind.
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".forgec2-snapshot-") {
			t.Fatalf("snapshot temp file left behind: %s", e.Name())
		}
	}
}

// TestNewBackupManagerRequiresKey pins the fail-closed constructor: without a
// configured key, backups refuse to start rather than write .fbk files that
// become unrestorable after the next restart.
func TestNewBackupManagerRequiresKey(t *testing.T) {
	conn := backupTestDB(t)
	if _, err := NewBackupManager(conn, "", t.TempDir(), ""); err == nil {
		t.Fatal("an empty backup key was accepted")
	}
	if _, err := NewBackupManager(conn, "", t.TempDir(), "not-hex"); err == nil {
		t.Fatal("a non-hex key was accepted")
	}
	if _, err := NewBackupManager(conn, "", t.TempDir(), strings.Repeat("ab", 8)); err == nil {
		t.Fatal("a short key was accepted")
	}
	bm, err := NewBackupManager(conn, "", t.TempDir(), strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if len(bm.keyID) != 8 {
		t.Fatalf("key id = %q", bm.keyID)
	}
}

// TestCleanupOldBackupsKeepsRetention covers the retention sweep: the oldest
// snapshots go, the newest `retain` stay, and non-backup files are untouched.
func TestCleanupOldBackupsKeepsRetention(t *testing.T) {
	conn := backupTestDB(t)
	backupDir := t.TempDir()
	bm, err := NewBackupManager(conn, "", backupDir, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	bm.SetRetention(2)

	// Five backups with increasing mtimes, plus a stray file.
	for i := 0; i < 5; i++ {
		name := filepath.Join(backupDir, "forgec2_backup_2026010"+string(rune('0'+i))+"_000000.fbk")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	stray := filepath.Join(backupDir, "notes.txt")
	if err := os.WriteFile(stray, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	bm.cleanupOldBackups()

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	var fbk []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".fbk" {
			fbk = append(fbk, e.Name())
		}
	}
	if len(fbk) != 2 {
		t.Fatalf("backups kept = %d (%v), want 2", len(fbk), fbk)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("a non-backup file was removed")
	}
}

// TestParseCronScheduleBackup pins the accepted backup schedule spellings.
func TestParseCronScheduleBackup(t *testing.T) {
	cases := map[string]time.Duration{
		"hourly":  time.Hour,
		"daily":   24 * time.Hour,
		"weekly":  7 * 24 * time.Hour,
		"monthly": 30 * 24 * time.Hour,
		"15m":     15 * time.Minute,
	}
	for in, want := range cases {
		got, err := parseCronSchedule(in)
		if err != nil {
			t.Errorf("parseCronSchedule(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseCronSchedule(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseCronSchedule("whenever"); err == nil {
		t.Error("an invalid schedule was accepted")
	}
}

// TestBackupManagerLifecycle covers Start/Stop/PerformBackup: starting twice is
// idempotent, PerformBackup is a no-op while stopped, and Stop releases the
// ticker.
func TestBackupManagerLifecycle(t *testing.T) {
	conn := backupTestDB(t)
	bm, err := NewBackupManager(conn, "", t.TempDir(), strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	// Not running yet: PerformBackup does nothing.
	if err := bm.PerformBackup(); err != nil {
		t.Fatalf("PerformBackup while stopped: %v", err)
	}

	if err := bm.Start("1h"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Second start is a no-op success.
	if err := bm.Start("1h"); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	bm.mu.Lock()
	running := bm.running
	bm.mu.Unlock()
	if !running {
		t.Fatal("Start did not mark the manager running")
	}

	// A manual run while running produces a snapshot.
	if err := bm.PerformBackup(); err != nil {
		t.Fatalf("PerformBackup: %v", err)
	}
	// Start() kicks off an immediate backup in its goroutine, so the manual run
	// and the scheduled one race to be first; poll rather than assume which one
	// produced the file.
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		entries, readErr := os.ReadDir(bm.backupDir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".fbk") {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("PerformBackup produced no snapshot")
	}

	bm.Stop()
	bm.mu.Lock()
	stillRunning := bm.running
	tickerNil := bm.ticker == nil
	bm.mu.Unlock()
	if stillRunning || !tickerNil {
		t.Fatalf("Stop left running=%v tickerNil=%v", stillRunning, tickerNil)
	}
	bm.Stop() // idempotent

	// An invalid schedule fails before the manager starts ticking.
	bm2, err := NewBackupManager(conn, "", t.TempDir(), strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := bm2.Start("nope"); err == nil {
		t.Fatal("an invalid schedule started the manager")
	}
}
