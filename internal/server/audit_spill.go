package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// auditSpillSeq disambiguates spill files written inside the same nanosecond.
var auditSpillSeq atomic.Uint64

// auditSpillRoot is where batches that could not be written to the database
// wait for the next attempt. It lives under the configured data dir: a relative
// path would scatter the audit trail next to the process, which is the same
// defect class the uploads path had.
func (s *Server) auditSpillRoot() (string, error) {
	dir := s.rawDataDir()
	if dir == "" {
		return "", errNoDataDir
	}
	return filepath.Join(dir, "audit-spill"), nil
}

// spillAuditBatch parks one batch on disk. One file per batch, named so the
// lexical order is the write order, and the file is removed only after the
// batch has landed in the database: a crash mid-drain costs a retry, not a
// lost row.
//
// Returns false when the batch could not be parked (no data dir, read-only
// volume); the caller then records a real drop.
func (s *Server) spillAuditBatch(batch []db.AuditLog) bool {
	if len(batch) == 0 {
		return true
	}
	root, err := s.auditSpillRoot()
	if err != nil {
		return false
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		slog.Error("Failed to create audit spill dir", "dir", root, "error", err)
		return false
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		slog.Error("Failed to encode spilled audit batch", "count", len(batch), "error", err)
		return false
	}
	name := fmt.Sprintf("spill-%020d-%04d.json", time.Now().UnixNano(), auditSpillSeq.Add(1)%10000)
	final := filepath.Join(root, name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0600); err != nil {
		slog.Error("Failed to write spilled audit batch", "file", name, "error", err)
		return false
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		slog.Error("Failed to publish spilled audit batch", "file", name, "error", err)
		return false
	}
	s.auditSpilled.Add(int64(len(batch)))
	slog.Warn("Parked an audit batch on disk for a later write", "file", name, "count", len(batch))
	return true
}

// drainAuditSpill persists the batches that earlier could not be written and
// returns how many rows it recovered. A batch that still cannot be written
// stays on disk for the next attempt; a file that cannot be parsed is moved
// aside so it does not block the queue forever.
func (s *Server) drainAuditSpill() int {
	root, err := s.auditSpillRoot()
	if err != nil {
		return 0
	}
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	names := make([]string, 0, len(dirEntries))
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	recovered := 0
	for _, name := range names {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var batch []db.AuditLog
		if err := json.Unmarshal(data, &batch); err != nil {
			slog.Error("Unreadable spilled audit batch moved aside", "file", name, "error", err)
			if renameErr := os.Rename(path, path+".bad"); renameErr != nil {
				_ = os.Remove(path)
			}
			s.auditNoteDropped(len(batch))
			continue
		}
		if len(batch) == 0 {
			_ = os.Remove(path)
			continue
		}
		if err := s.persistAuditBatch(batch, s.auditLastHash); err != nil {
			// Still cannot write: leave it for the next tick.
			continue
		}
		s.auditLastHash = batch[len(batch)-1].EntryHash
		s.sendToSIEM(batch)
		_ = os.Remove(path)
		recovered += len(batch)
	}
	return recovered
}

// auditSpillPending counts the batches still waiting on disk, so a probe can
// report "the audit trail has a backlog" even when the database is healthy.
func (s *Server) auditSpillPending() int {
	root, err := s.auditSpillRoot()
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	pending := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			pending++
		}
	}
	return pending
}
