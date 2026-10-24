package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

type backupEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

// sqliteMagic is the file header signature for SQLite database files.
var sqliteMagic = []byte("SQLite format 3\x00")

// isSQLiteFile peeks at the first 16 bytes to check the SQLite magic header.
// The caller must use a bufio.Reader to avoid consuming bytes.
func isSQLiteFile(br *bufio.Reader) bool {
	header, err := br.Peek(16)
	if err != nil {
		return false
	}
	return string(header) == string(sqliteMagic)
}

func (s *Server) backupDir() string {
	s.configMu.RLock()
	dataDir := s.cfg.Server.DataDir
	s.configMu.RUnlock()
	return filepath.Join(dataDir, "backups")
}

// removeStaleDBArtifacts deletes WAL/SHM sidecar files that belonged to the
// pre-restore database. Without this, SQLite would try to replay the old
// write-ahead log against the newly restored file on the next startup.
func removeStaleDBArtifacts(dbPath string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			slog.Warn("Failed to remove stale database sidecar", "path", dbPath+suffix, "error", err)
		}
	}
}

// verifyRestoredDB performs post-restore integrity checks on the SQLite database.
func verifyRestoredDB(dbPath string) error {
	f, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("cannot open restored file: %w", err)
	}
	defer f.Close()

	br := bufio.NewReader(f)
	if !isSQLiteFile(br) {
		return fmt.Errorf("restored file is not a valid SQLite database")
	}

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cannot stat restored file: %w", err)
	}
	if fi.Size() < 1024 {
		return fmt.Errorf("restored file too small (%d bytes) — likely truncated", fi.Size())
	}

	return nil
}

// verifyRestoredDBDeep runs the full gate on a restore candidate: magic,
// size, SQLite open, PRAGMA integrity_check and schema census. A header-valid
// but truncated/corrupt file must never replace the live database.
func verifyRestoredDBDeep(dbPath string) error {
	if err := verifyRestoredDB(dbPath); err != nil {
		return err
	}
	return db.VerifySQLiteFile(dbPath)
}

// prepareRestoreSource returns a reader over the raw SQLite bytes of an upload
// or on-disk backup. Plain SQLite files pass through untouched; encrypted .fbk
// files (produced by the scheduled BackupManager) are decrypted with the
// configured backup key and verified before use.
func (s *Server) prepareRestoreSource(src io.Reader, ext string) (io.Reader, error) {
	br := bufio.NewReader(src)
	if isSQLiteFile(br) {
		return br, nil
	}
	if ext != ".fbk" {
		return nil, errors.New("file is not a valid SQLite database")
	}
	data, err := io.ReadAll(br)
	if err != nil {
		return nil, errors.New("failed to read backup file")
	}
	key, err := s.backupKey()
	if err != nil {
		return nil, err
	}
	plaintext, err := decryptBackupData(data, key)
	if err != nil {
		return nil, errors.New("failed to decrypt backup (key mismatch or corrupt file)")
	}
	if !isSQLiteFile(bufio.NewReader(bytes.NewReader(plaintext))) {
		return nil, errors.New("decrypted backup is not a valid SQLite database")
	}
	return bytes.NewReader(plaintext), nil
}

// writeRestoredDB copies the source stream to a temp file in the database
// directory, verifies it (magic, size, open, integrity_check, schema), saves
// a timestamped copy of the LIVE database first, then atomically swaps.
// The live database is left untouched if any step fails, and the pre-restore
// copy allows manual rollback.
func writeRestoredDB(src io.Reader, dbPath string) error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("prepare database directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".forgec2-restore-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return fmt.Errorf("write database file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := verifyRestoredDBDeep(tmpPath); err != nil {
		return err
	}

	// Pre-restore safety copy of the live DB (pruned to the newest 3).
	if _, err := os.Stat(dbPath); err == nil {
		prePath := dbPath + ".pre-restore-" + time.Now().Format("20060102_150405")
		if err := copyFile(dbPath, prePath); err != nil {
			return fmt.Errorf("save pre-restore copy: %w", err)
		}
		prunePreRestores(dbPath, 3)
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("swap database file: %w", err)
	}

	removeStaleDBArtifacts(dbPath)
	return nil
}

// copyFile copies src to dst (0600). Used for pre-restore safety copies.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prunePreRestores keeps the newest keep pre-restore copies next to dbPath.
func prunePreRestores(dbPath string, keep int) {
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath) + ".pre-restore-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var matches []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), base) {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, name := range matches[:len(matches)-keep] {
		os.Remove(filepath.Join(dir, name))
	}
}

func (s *Server) handleDBBackupList(c *gin.Context) {
	dir := s.backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			respond(c, []backupEntry{})
			return
		}
		respondError(c, http.StatusInternalServerError, "failed to read backup directory")
		return
	}

	var backups []backupEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".db" && ext != ".fbk" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupEntry{
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ModTime > backups[j].ModTime
	})

	respond(c, backups)
}

// requireSQLiteDB rejects SQLite-only maintenance endpoints when the server
// runs on another driver, so operators get an explicit reason instead of an
// opaque SQL failure (or a restore that silently writes an unreadable file).
func (s *Server) requireSQLiteDB(c *gin.Context) bool {
	if isSQLiteDB(s.db) {
		return true
	}
	driver := "unknown"
	if s.db != nil && s.db.Dialector != nil {
		driver = s.db.Dialector.Name()
	}
	slog.Warn("SQLite-only maintenance endpoint refused", "endpoint", c.Request.URL.Path, "driver", driver)
	s.LogAuditRecord(c, "db_maintenance_unsupported", "system", "", "driver="+driver, false, errNonSQLiteBackup)
	respondError(c, http.StatusNotImplemented, errNonSQLiteBackup.Error())
	return false
}

func (s *Server) handleDBBackup(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	if !s.requireSQLiteDB(c) {
		return
	}
	s.configMu.RLock()
	dbFile := s.cfg.Database.Path
	s.configMu.RUnlock()
	if _, err := os.Stat(dbFile); err != nil {
		respondError(c, http.StatusInternalServerError, "database file not found")
		return
	}

	// Manual backups are encrypted (.fbk) by default: a plaintext database
	// copy concentrates every secret the loot layer encrypts at rest
	// (bcrypt hashes, host inventory, task output, webhook URLs, AI chats).
	// Pass ?encrypt=false for an explicit plaintext snapshot (audited).
	if c.Query("encrypt") == "false" {
		s.handleDBBackupPlaintext(c)
		return
	}
	if s.backupManager == nil {
		respondError(c, http.StatusInternalServerError, "encrypted backups unavailable: set crypto.backup_key (64 hex chars), or pass ?encrypt=false for an explicit plaintext snapshot")
		return
	}
	name, size, err := s.backupManager.createBackup()
	s.backupManager.noteBackupResult(err)
	if err != nil {
		if s.metrics != nil {
			if s.metrics.BackupFailuresTotal != nil {
				s.metrics.BackupFailuresTotal.Inc()
			}
		}
		slog.Error("Failed to create encrypted backup", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to create backup")
		return
	}
	if s.metrics != nil && s.metrics.BackupSuccessTotal != nil {
		s.metrics.BackupSuccessTotal.Inc()
	}
	slog.Info("Database backup created", "file", name, "size", size)
	s.LogAuditRecord(c, "db_backup", "system", "", "encrypted database backup "+name, true, nil)
	respond(c, backupEntry{
		Name:    name,
		Size:    size,
		ModTime: time.Now().UTC().Format(time.RFC3339),
	})
}

// handleDBBackupPlaintext is the explicit plaintext opt-out (?encrypt=false).
// Same VACUUM INTO snapshot discipline, audited as plaintext.
func (s *Server) handleDBBackupPlaintext(c *gin.Context) {
	dir := s.backupDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create backup directory")
		return
	}

	ts := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("forgec2_%s.db", ts)
	backupPath := filepath.Join(dir, backupName)

	// Use SQLite's VACUUM INTO to produce a consistent snapshot. A raw file
	// copy is unsafe while the database runs in WAL mode: it would omit the
	// -wal file and yield an incomplete/corrupt backup.
	if err := s.db.Exec("VACUUM INTO ?", backupPath).Error; err != nil {
		slog.Error("Failed to create database backup", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to create backup")
		return
	}

	fi, err := os.Stat(backupPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to stat backup file")
		return
	}

	slog.Info("Database backup created", "path", backupPath, "size", fi.Size(), "encrypted", false)
	s.LogAuditRecord(c, "db_backup", "system", "", "plaintext database backup "+backupName+" (explicit ?encrypt=false opt-out)", true, nil)
	respond(c, backupEntry{
		Name:    backupName,
		Size:    fi.Size(),
		ModTime: time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleDBRestore(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	if !s.requireSQLiteDB(c) {
		return
	}
	restoreType := c.PostForm("type")

	switch restoreType {
	case "upload":
		s.handleRestoreFromUpload(c)
	case "file":
		s.handleRestoreFromFile(c)
	default:
		respondError(c, http.StatusBadRequest, "restore type must be 'upload' or 'file'")
	}
}

func (s *Server) handleRestoreFromUpload(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		respondError(c, http.StatusBadRequest, "no file uploaded")
		return
	}
	defer file.Close()

	if header.Size == 0 {
		respondError(c, http.StatusBadRequest, "uploaded file is empty")
		return
	}

	if header.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("file too large (max %d bytes)", MaxUploadSize))
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".db" && ext != ".fbk" {
		respondError(c, http.StatusBadRequest, "only .db or .fbk files are accepted")
		return
	}

	src, err := s.prepareRestoreSource(file, ext)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	s.configMu.RLock()
	dbPath := s.cfg.Database.Path
	s.configMu.RUnlock()
	if err := writeRestoredDB(src, dbPath); err != nil {
		slog.Error("Database restore from upload failed", "error", err)
		respondError(c, http.StatusInternalServerError, "restore failed")
		return
	}

	slog.Info("Database restored from uploaded file", "filename", header.Filename, "size", header.Size)

	respond(c, gin.H{
		"message": "Database restored. Server will restart to apply changes.",
		"restart": true,
	})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		time.Sleep(500 * time.Millisecond)
		s.Shutdown()
	}()
}

func (s *Server) handleRestoreFromFile(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	name := c.PostForm("name")
	if name == "" {
		respondError(c, http.StatusBadRequest, "backup name is required")
		return
	}

	name = filepath.Base(name)
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		respondError(c, http.StatusBadRequest, "invalid backup name")
		return
	}

	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".db" && ext != ".fbk" {
		respondError(c, http.StatusBadRequest, "invalid backup file type")
		return
	}

	backupPath := filepath.Join(s.backupDir(), name)
	if _, err := os.Stat(backupPath); err != nil {
		respondError(c, http.StatusNotFound, "backup file not found")
		return
	}

	// Sidecar pre-check for .fbk: a key_id mismatch means decryption is
	// certain to fail — say so explicitly instead of the generic
	// "key mismatch or corrupt file" after a wasted read.
	if ext == ".fbk" {
		if metaBytes, err := os.ReadFile(backupPath + ".meta.json"); err == nil {
			var meta backupSidecar
			if err := json.Unmarshal(metaBytes, &meta); err == nil && meta.KeyID != "" {
				if active := s.backupKeyID(); active != "" && meta.KeyID != active {
					respondError(c, http.StatusBadRequest, "backup was sealed with a different key (key_id "+meta.KeyID+", active "+active+")")
					return
				}
			}
		}
	}

	srcFile, err := os.Open(backupPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to open backup file")
		return
	}
	defer srcFile.Close()

	fi, err := srcFile.Stat()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to stat backup file")
		return
	}

	if fi.Size() > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("backup file too large (max %d bytes)", MaxUploadSize))
		return
	}

	src, err := s.prepareRestoreSource(srcFile, ext)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	s.configMu.RLock()
	dbPath := s.cfg.Database.Path
	s.configMu.RUnlock()
	if err := writeRestoredDB(src, dbPath); err != nil {
		slog.Error("Database restore from backup failed", "error", err)
		respondError(c, http.StatusInternalServerError, "restore failed")
		return
	}

	slog.Info("Database restored from backup", "backup", name, "size", fi.Size())

	respond(c, gin.H{
		"message": "Database restored. Server will restart to apply changes.",
		"restart": true,
	})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		time.Sleep(500 * time.Millisecond)
		s.Shutdown()
	}()
}

func (s *Server) handleDBBackupDownload(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		respondError(c, http.StatusBadRequest, "backup name is required")
		return
	}
	name = filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".db" && ext != ".fbk" {
		respondError(c, http.StatusBadRequest, "invalid backup file type")
		return
	}
	backupPath := filepath.Join(s.backupDir(), name)
	if _, err := os.Stat(backupPath); err != nil {
		respondError(c, http.StatusNotFound, "backup file not found")
		return
	}
	serveFileSafe(c, backupPath, s.backupDir(), name)
}
