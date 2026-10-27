package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// anchorTestServer is a tenant server whose data directory is a temp dir, so the
// anchor file has somewhere to live.
func anchorTestServer(t *testing.T) *Server {
	t.Helper()
	s := mustTenantServer(t)
	// The anchor path is resolved at startup, not read from the config on the audit
	// path (that would deadlock handlers that hold configMu while auditing).
	dir := t.TempDir()
	s.configMu.Lock()
	s.cfg.Server.DataDir = dir
	s.configMu.Unlock()
	s.initAuditAnchor()
	t.Cleanup(func() {
		s.auditAnchorFile = ""
		s.configMu.Lock()
		s.cfg.Server.DataDir = ""
		s.configMu.Unlock()
	})
	return s
}

// TestAuditAnchorRecordsChainHead pins the anchor write: every committed batch
// appends the new head to a file outside the audit table.
func TestAuditAnchorRecordsChainHead(t *testing.T) {
	s := anchorTestServer(t)
	if path := s.auditAnchorPath(); path == "" || !strings.Contains(path, "audit-anchor.log") {
		t.Fatalf("anchor path = %q", path)
	}

	seedAuditChain(t, s, 3)

	data, err := os.ReadFile(s.auditAnchorPath())
	if err != nil {
		t.Fatalf("the anchor file was not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		t.Fatal("the anchor file is empty")
	}

	var newest db.AuditLog
	if err := s.db.Where("action = ?", "probe").Order("id DESC").First(&newest).Error; err != nil {
		t.Fatal(err)
	}
	if head := s.auditAnchorHead(); head != newest.EntryHash {
		t.Fatalf("anchor head = %q, want %q", head, newest.EntryHash)
	}
	if !strings.HasPrefix(lines[len(lines)-1], newest.EntryHash) {
		t.Fatalf("the last anchor line does not name the head: %q", lines[len(lines)-1])
	}

	// An intact table verifies against its own anchor.
	if v := s.verifyAuditChain(100); !v.OK {
		t.Fatalf("an anchored chain failed verification: %+v", v)
	}
}

// TestAuditAnchorDetectsFullRewrite is the property the anchor exists for: an
// attacker who rewrites every row (recomputing each hash) keeps the table
// internally consistent, so only the out-of-band copy can catch it.
func TestAuditAnchorDetectsFullRewrite(t *testing.T) {
	s := anchorTestServer(t)
	rows := seedAuditChain(t, s, 3)

	// Rewrite the newest row AND its hash so the table stays self-consistent.
	tampered := rows[len(rows)-1]
	tampered.Details = "rewritten"
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", tampered.ID).
		Update("details", tampered.Details).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", tampered.ID).
		Update("entry_hash", auditEntryHash(&tampered)).Error; err != nil {
		t.Fatal(err)
	}

	var reloaded []db.AuditLog
	if err := s.db.Order("id asc").Find(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	// The last row's hash now matches its own content, so the in-table checks pass.
	for _, row := range reloaded {
		if want := auditEntryHash(&row); row.EntryHash != want {
			t.Fatalf("the rewrite left an inconsistent row %d", row.ID)
		}
	}

	verdict := s.verifyAuditChain(100)
	if verdict.OK {
		t.Fatal("a whole-table rewrite passed verification")
	}
	if !strings.Contains(verdict.Reason, "anchor") {
		t.Fatalf("the failure does not name the anchor: %q", verdict.Reason)
	}
}

// TestAuditAnchorAbsentIsNotAFailure covers fresh installs and test servers without
// a data directory: no anchor means nothing to compare, not a failure.
func TestAuditAnchorAbsentIsNotAFailure(t *testing.T) {
	s := mustTenantServer(t) // data dir empty
	seedAuditChain(t, s, 2)

	if head := s.auditAnchorHead(); head != "" {
		t.Fatalf("an anchor appeared without a data directory: %q", head)
	}
	if v := s.verifyAuditChain(100); !v.OK {
		t.Fatalf("no-anchor verification failed: %+v", v)
	}
}

// TestAuditAnchorSurvivesAPurge pins that maintenance does not trip the head check
// (deleting old rows leaves the newest hash untouched).
func TestAuditAnchorSurvivesAPurge(t *testing.T) {
	s := anchorTestServer(t)
	seedAuditChain(t, s, 3)

	if err := s.db.Exec("DELETE FROM audit_logs WHERE id <= ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	if v := s.verifyAuditChain(100); !v.OK {
		t.Fatalf("deleting old rows tripped the head check: %+v", v)
	}
}

// TestAuditUnderConfigWriteLockDoesNotDeadlock is the regression guard for the
// deadlock this anchor introduced: the audit path used to call rawDataDir(), which
// takes configMu.RLock, and several handlers audit while holding configMu for
// WRITE (config saves record themselves). Go RWMutex is not reentrant, so that
// pair hung the whole request - and the full suite timed out instead of failing.
func TestAuditUnderConfigWriteLockDoesNotDeadlock(t *testing.T) {
	s := anchorTestServer(t)

	s.configMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.LogAuditRecord(nil, "config_self_audit", "config", "", "written while holding the lock", true, nil)
	}()

	select {
	case <-done:
		s.configMu.Unlock()
	case <-time.After(5 * time.Second):
		// Do not unlock: the goroutine is wedged and unlocking would only hide it.
		t.Fatal("auditing under the config write lock deadlocked (the audit path must not take configMu)")
	}

	var count int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "config_self_audit").Count(&count)
	if count != 1 {
		t.Fatalf("the audit row was not written: %d", count)
	}
}
