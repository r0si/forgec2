package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"gorm.io/gorm"
)

const (
	backupKeySize   = 32
	backupIVSize    = 12
	backupSaltSize  = 16
	backupTagSize   = 16
	backupTimestamp = "20060102_150405"
)

type BackupManager struct {
	db        *gorm.DB
	dbPath    string
	backupDir string
	key       []byte
	keyID     string
	running   bool
	mu        sync.Mutex
	// runMu serialises snapshot creation (scheduled vs manual).
	runMu  sync.Mutex
	ticker *time.Ticker
	stopCh chan struct{}
	// done closes when the schedule goroutine has returned. Stop waits on it so a
	// caller (Shutdown, a test cleaning up its temp dir) is not racing a backup that is
	// still writing files.
	done chan struct{}
	// retain is how many encrypted .fbk snapshots to keep (>=1). The previous
	// hard-coded 7 made retention a non-operational decision.
	retain int
	// lastSuccess / lastFailure back the freshness metrics, so a silently
	// failing backup job is visible in Prometheus and /ready.
	lastSuccess time.Time
	lastFailure time.Time
	lastError   string
	// Sidecar, when set, supplies non-secret snapshot metadata written
	// alongside each .fbk (key fingerprint, server identity). The restore
	// path compares key_id up front for an early, explicit mismatch error.
	Sidecar func() backupSidecar
}

// defaultBackupRetention keeps the historical behaviour when the operator has
// not chosen a retention count.
const defaultBackupRetention = 7

// SetRetention configures how many encrypted snapshots are kept.
func (bm *BackupManager) SetRetention(n int) {
	if n < 1 {
		return
	}
	bm.mu.Lock()
	bm.retain = n
	bm.mu.Unlock()
}

func (bm *BackupManager) retention() int {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if bm.retain < 1 {
		return defaultBackupRetention
	}
	return bm.retain
}

// BackupHealth is the operator-facing view of backup freshness.
type BackupHealth struct {
	Enabled     bool      `json:"enabled"`
	Retain      int       `json:"retain"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastFailure time.Time `json:"last_failure,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	AgeSeconds  float64   `json:"age_seconds"`
	SnapshotDir string    `json:"snapshot_dir"`
}

// Health reports the current backup freshness. Before the first successful run
// it reports a zero age, which callers treat as "not yet proven".
func (bm *BackupManager) Health() BackupHealth {
	bm.mu.Lock()
	last := bm.lastSuccess
	bm.mu.Unlock()
	h := BackupHealth{
		Retain:      bm.retention(),
		LastSuccess: last,
		SnapshotDir: bm.backupDir,
	}
	bm.mu.Lock()
	h.LastFailure = bm.lastFailure
	h.LastError = bm.lastError
	bm.mu.Unlock()
	if !last.IsZero() {
		h.AgeSeconds = time.Since(last).Seconds()
	}
	return h
}

func (bm *BackupManager) noteBackupResult(err error) {
	bm.mu.Lock()
	if err != nil {
		bm.lastFailure = time.Now()
		bm.lastError = err.Error()
	} else {
		bm.lastSuccess = time.Now()
		bm.lastError = ""
	}
	bm.mu.Unlock()
}

// backupSidecar is the non-secret companion of a .fbk file. It deliberately
// carries no key material or credentials — only identity needed to validate
// a restore before attempting decryption.
type backupSidecar struct {
	Version       string `json:"version"`
	CreatedAt     string `json:"created_at"`
	KeyID         string `json:"key_id"`
	ServerVersion string `json:"server_version"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	TLSEnabled    bool   `json:"tls_enabled"`
	DBDriver      string `json:"db_driver"`
}

func NewBackupManager(db *gorm.DB, dbPath, backupDir, key string) (*BackupManager, error) {
	var backupKey []byte
	if key != "" {
		parsedKey, err := hex.DecodeString(key)
		if err != nil {
			return nil, err
		}
		if len(parsedKey) != backupKeySize {
			return nil, fmt.Errorf("backup key must be %d bytes (64 hex chars)", backupKeySize)
		}
		backupKey = parsedKey
	} else {
		// Refuse to run scheduled encrypted backups without a configured
		// key: the old random-key fallback produced .fbk files that became
		// permanently unrestorable after the next restart, discovered only
		// at disaster time. Set crypto.backup_key explicitly.
		return nil, fmt.Errorf("crypto.backup_key is not configured: scheduled encrypted backups refuse to start without it")
	}

	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}

	// keyID fingerprints the actual key (not a date): restores and sidecars
	// can name exactly which key generation they belong to.
	sum := sha256.Sum256(backupKey)
	return &BackupManager{
		db:        db,
		dbPath:    dbPath,
		backupDir: backupDir,
		key:       backupKey,
		keyID:     hex.EncodeToString(sum[:])[:8],
	}, nil
}

// errNonSQLiteBackup is returned by every backup/restore/vacuum path when the
// server runs on a non-SQLite driver. Those paths are implemented on top of
// SQLite's VACUUM INTO plus file replacement: on PostgreSQL they would either
// fail with a confusing SQL error or, worse, "restore" by writing a file that
// nothing reads. Refusing loudly is the honest behaviour until a driver-aware
// logical dump path exists.
var errNonSQLiteBackup = errors.New("database backup/restore/vacuum are implemented for SQLite only; use an external PostgreSQL backup tool (pg_dump/pgBackRest) for this deployment")

// isSQLiteDB reports whether the live connection is SQLite.
func isSQLiteDB(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil {
		return false
	}
	return db.Dialector.Name() == "sqlite"
}

func (bm *BackupManager) Start(cronSchedule string) error {
	if !isSQLiteDB(bm.db) {
		slog.Warn("Scheduled backups disabled: non-SQLite driver", "driver", bm.db.Dialector.Name())
		return errNonSQLiteBackup
	}
	bm.mu.Lock()
	if bm.running {
		bm.mu.Unlock()
		return nil
	}
	bm.running = true
	bm.mu.Unlock()

	duration, err := parseCronSchedule(cronSchedule)
	if err != nil {
		return err
	}

	slog.Info("Backup manager started", "schedule", cronSchedule, "interval", duration)

	bm.ticker = time.NewTicker(duration)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	bm.mu.Lock()
	bm.stopCh = stopCh
	bm.done = done
	bm.mu.Unlock()
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		bm.PerformBackup()
		for {
			select {
			case <-bm.ticker.C:
				bm.PerformBackup()
			case <-stopCh:
				return
			}
		}
	}()

	return nil
}

func (bm *BackupManager) Stop() {
	bm.mu.Lock()
	bm.running = false
	if bm.ticker != nil {
		bm.ticker.Stop()
		bm.ticker = nil
	}
	if bm.stopCh != nil {
		close(bm.stopCh)
		bm.stopCh = nil
	}
	done := bm.done
	bm.done = nil
	bm.mu.Unlock()

	// Wait for the goroutine to actually return: it may be inside PerformBackup, and a
	// caller that proceeds immediately (Shutdown closing the DB, a test removing its
	// temp dir) would race the files it is still writing.
	if done != nil {
		select {
		case <-done:
		case <-time.After(backupStopDrainTimeout):
			slog.Warn("Backup manager stopped with a run still in flight", "timeout", backupStopDrainTimeout)
		}
	}
	slog.Info("Backup manager stopped")
}

// backupStopDrainTimeout bounds how long Stop waits for an in-flight backup.
const backupStopDrainTimeout = 30 * time.Second

func parseCronSchedule(schedule string) (time.Duration, error) {
	switch schedule {
	case "hourly":
		return 1 * time.Hour, nil
	case "daily":
		return 24 * time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	case "monthly":
		return 30 * 24 * time.Hour, nil
	default:
		duration, err := time.ParseDuration(schedule)
		if err != nil {
			return 0, fmt.Errorf("invalid backup schedule: %s", schedule)
		}
		return duration, nil
	}
}

func (bm *BackupManager) PerformBackup() error {
	bm.mu.Lock()
	if !bm.running {
		bm.mu.Unlock()
		return nil
	}
	bm.mu.Unlock()

	_, _, err := bm.createBackup()
	bm.noteBackupResult(err)
	return err
}

// createBackup runs one encrypted backup immediately, independent of the
// schedule. Used by PerformBackup (scheduled) and the manual backup
// endpoint. Returns the .fbk filename and size.
func (bm *BackupManager) createBackup() (string, int64, error) {
	if !isSQLiteDB(bm.db) {
		return "", 0, errNonSQLiteBackup
	}
	// One backup at a time: a scheduled run and a manual one can overlap, and two
	// concurrent VACUUM INTO snapshots would fight over the writer and the output
	// directory.
	bm.runMu.Lock()
	defer bm.runMu.Unlock()
	start := time.Now()
	slog.Info("Starting database backup")

	// Snapshot inside the backup dir (same filesystem for the later rename,
	// 0600 from creation). os.TempDir() may live on another volume and with
	// looser ACLs.
	snapshotPath := filepath.Join(bm.backupDir, fmt.Sprintf(".forgec2-snapshot-%d.tmp", time.Now().UnixNano()))
	defer os.Remove(snapshotPath)

	// VACUUM INTO is the only snapshot path: the old BEGIN IMMEDIATE +
	// raw file-copy fallback held the write lock for the whole copy and
	// risked WAL inconsistency. Retry briefly on busy, then fail loudly.
	var vacErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		}
		if vacErr = bm.db.Exec("VACUUM INTO ?", snapshotPath).Error; vacErr == nil {
			break
		}
	}
	if vacErr != nil {
		slog.Error("VACUUM INTO backup failed after retries", "error", vacErr)
		return "", 0, vacErr
	}

	timestamp := time.Now().Format(backupTimestamp)
	backupFile := filepath.Join(bm.backupDir, fmt.Sprintf("forgec2_backup_%s.fbk", timestamp))
	// Two backups can land in the same second (the scheduler fires on Start and a
	// manual run arrives immediately after). Without a suffix the second rename
	// overwrites the first on Unix and fails outright on Windows.
	for i := 2; ; i++ {
		if _, statErr := os.Stat(backupFile); os.IsNotExist(statErr) {
			break
		}
		backupFile = filepath.Join(bm.backupDir, fmt.Sprintf("forgec2_backup_%s_%d.fbk", timestamp, i))
	}

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		slog.Error("Failed to read backup file", "error", err)
		return "", 0, err
	}

	encryptedData, err := bm.encrypt(data)
	if err != nil {
		slog.Error("Failed to encrypt backup", "error", err)
		return "", 0, err
	}

	// Write atomically via temp + rename so a crash never leaves a half .fbk.
	tmpOut := backupFile + ".tmp"
	if err := os.WriteFile(tmpOut, encryptedData, 0600); err != nil {
		slog.Error("Failed to write backup file", "error", err)
		return "", 0, err
	}
	if err := os.Rename(tmpOut, backupFile); err != nil {
		os.Remove(tmpOut)
		slog.Error("Failed to finalize backup file", "error", err)
		return "", 0, err
	}

	slog.Info("Backup completed", "file", backupFile, "size", len(encryptedData), "duration", time.Since(start))

	if bm.Sidecar != nil {
		if meta, err := json.MarshalIndent(bm.Sidecar(), "", "  "); err != nil {
			slog.Warn("Failed to encode backup sidecar", "err", err)
		} else if err := os.WriteFile(backupFile+".meta.json", meta, 0600); err != nil {
			slog.Warn("Failed to write backup sidecar", "err", err)
		}
	}

	bm.cleanupOldBackups()

	return filepath.Base(backupFile), int64(len(encryptedData)), nil
}

func (bm *BackupManager) encrypt(data []byte) ([]byte, error) {
	return bm.encryptWithKey(data, bm.key)
}

func (bm *BackupManager) decrypt(encryptedData []byte) ([]byte, error) {
	return bm.decryptWithKey(encryptedData, bm.key)
}

func (bm *BackupManager) cleanupOldBackups() {
	files, err := os.ReadDir(bm.backupDir)
	if err != nil {
		return
	}

	bm.removeStaleTempFiles(files)

	keepCount := bm.retention()
	var backupFiles []os.FileInfo

	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".fbk" {
			info, _ := file.Info()
			backupFiles = append(backupFiles, info)
		}
	}

	if len(backupFiles) <= keepCount {
		return
	}

	sort.Slice(backupFiles, func(i, j int) bool {
		return backupFiles[i].ModTime().Before(backupFiles[j].ModTime())
	})

	for i := 0; i < len(backupFiles)-keepCount; i++ {
		name := backupFiles[i].Name()
		if err := os.Remove(filepath.Join(bm.backupDir, name)); err != nil {
			slog.Warn("Failed to remove old backup", "file", name, "error", err)
		}
		// Prune its sidecar with it so stale metadata cannot mislead a
		// future restore's key_id check.
		os.Remove(filepath.Join(bm.backupDir, name+".meta.json"))
	}

	slog.Info("Cleaned up old backups", "deleted", len(backupFiles)-keepCount, "remaining", keepCount)
}

// backupTempMaxAge bounds how long a half-written backup artefact may linger.
// A crash (or a Stop during shutdown) can leave the VACUUM snapshot or the
// ".fbk.tmp" output behind, and nothing else ever removes them: retention only
// looks at ".fbk", so they accumulated until an operator noticed the disk. The
// age gate keeps an in-flight backup safe.
const backupTempMaxAge = time.Hour

// removeStaleTempFiles deletes leftover snapshot/temp artefacts older than
// backupTempMaxAge. Takes the directory listing so the caller reads it once.
func (bm *BackupManager) removeStaleTempFiles(files []os.DirEntry) {
	cutoff := time.Now().Add(-backupTempMaxAge)
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if !strings.HasSuffix(name, ".fbk.tmp") && !strings.HasPrefix(name, ".forgec2-snapshot-") {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(bm.backupDir, name)); err != nil {
			slog.Warn("Failed to remove stale backup temp file", "file", name, "error", err)
			continue
		}
		slog.Info("Removed stale backup temp file", "file", name, "age", time.Since(info.ModTime()).Round(time.Minute))
	}
}

func (bm *BackupManager) RotateKey(newKeyHex string) error {
	parsedKey, err := hex.DecodeString(newKeyHex)
	if err != nil {
		return fmt.Errorf("invalid hex key: %w", err)
	}
	if len(parsedKey) != backupKeySize {
		return fmt.Errorf("key must be %d bytes", backupKeySize)
	}

	files, err := os.ReadDir(bm.backupDir)
	if err != nil {
		return err
	}

	oldKey := bm.key
	reencrypted := 0
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".fbk" {
			continue
		}
		path := filepath.Join(bm.backupDir, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("Failed to read backup for re-encryption", "file", file.Name(), "error", err)
			continue
		}
		plaintext, err := bm.decryptWithKey(data, oldKey)
		if err != nil {
			slog.Warn("Failed to decrypt backup with old key", "file", file.Name(), "error", err)
			continue
		}
		enc, err := bm.encryptWithKey(plaintext, parsedKey)
		if err != nil {
			slog.Warn("Failed to re-encrypt backup", "file", file.Name(), "error", err)
			continue
		}
		if err := os.WriteFile(path, enc, 0600); err != nil {
			slog.Error("Failed to write re-encrypted backup, aborting key rotation", "file", file.Name(), "error", err)
			return fmt.Errorf("key rotation aborted: failed to write re-encrypted backup %s: %w", file.Name(), err)
		}
		reencrypted++
		// Best-effort hygiene: the plaintext existed only for this loop.
		crypto.Wipe(plaintext)
	}

	bm.key = parsedKey
	bm.keyID = fmt.Sprintf("k%s", time.Now().Format("20060102"))
	// The old key is genuinely dropped here (no fallback reads against it).
	crypto.Wipe(oldKey)
	slog.Info("Backup key rotated", "reencrypted", reencrypted)
	return nil
}

func (bm *BackupManager) encryptWithKey(data []byte, key []byte) ([]byte, error) {
	salt := make([]byte, backupSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	iv := make([]byte, backupIVSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext := append(salt, data...)
	ciphertext := aead.Seal(nil, iv, plaintext, nil)
	result := make([]byte, 0, len(iv)+len(ciphertext))
	result = append(result, iv...)
	result = append(result, ciphertext...)
	return result, nil
}

func (bm *BackupManager) decryptWithKey(encryptedData []byte, key []byte) ([]byte, error) {
	return decryptBackupData(encryptedData, key)
}

// decryptBackupData decrypts an encrypted .fbk backup blob with a 32-byte key.
// It accepts both the newer HMAC-authenticated format and the legacy format.
func decryptBackupData(encryptedData []byte, key []byte) ([]byte, error) {
	// Try HMAC-SHA256 verification first (new format)
	hasHMAC := false
	if len(encryptedData) >= backupIVSize+backupTagSize+32 {
		dataWithIV := encryptedData[:len(encryptedData)-32]
		receivedHMAC := encryptedData[len(encryptedData)-32:]

		mac := hmac.New(sha256.New, key)
		mac.Write(dataWithIV)
		expectedHMAC := mac.Sum(nil)

		if hmac.Equal(receivedHMAC, expectedHMAC) {
			hasHMAC = true
			encryptedData = dataWithIV
		}
	}

	if !hasHMAC {
		if len(encryptedData) < backupIVSize+backupTagSize {
			return nil, fmt.Errorf("backup data too short")
		}
	}

	iv := encryptedData[:backupIVSize]
	ciphertext := encryptedData[backupIVSize:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, iv, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	if len(plaintext) < backupSaltSize {
		return nil, fmt.Errorf("invalid backup format")
	}
	return plaintext[backupSaltSize:], nil
}

// backupKeyHex returns the hex key used to encrypt/decrypt .fbk backup files.
// crypto.backup_key is REQUIRED (validated at startup): the legacy derivation
// cascade (crypto.key, then the JWT secret) was removed so backups are
// cryptographically independent of other secrets. Backups created by older
// versions that re-used the derived key can no longer be restored.
func (s *Server) backupKeyHex() string {
	s.configMu.RLock()
	key := s.cfg.Crypto.BackupKey
	s.configMu.RUnlock()
	return key
}

// backupKey decodes backupKeyHex into the 32 raw key bytes used for .fbk
// encryption/decryption. Strict: an invalid value is an error, never a
// silent SHA-256 derivation — the old fallback masked misconfiguration and
// produced unrestorable backups discovered only at disaster time.
func (s *Server) backupKey() ([]byte, error) {
	b, err := hex.DecodeString(s.backupKeyHex())
	if err != nil || len(b) != backupKeySize {
		return nil, fmt.Errorf("crypto.backup_key must be %d bytes (64 hex chars)", backupKeySize)
	}
	return b, nil
}

// backupKeyID fingerprints the configured backup key for sidecar metadata.
func (s *Server) backupKeyID() string {
	b, err := hex.DecodeString(s.backupKeyHex())
	if err != nil || len(b) != backupKeySize {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}
