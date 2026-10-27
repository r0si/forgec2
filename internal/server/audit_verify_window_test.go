package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestAuditVerifyChecksTheNewestWindow pins a defect that made the verifier look at
// the wrong end of the table: it queried ORDER BY id ASC LIMIT n, which returns the
// OLDEST n rows. On any server with more than one window of history (200 rows is
// minutes of operation) /ready re-checked the same ancient block forever, so tampering
// with recent rows - the ones an attacker would actually touch - was never detected.
func TestAuditVerifyChecksTheNewestWindow(t *testing.T) {
	s := anchorTestServer(t)

	// More rows than the window, so the oldest-N query and the newest-N query see
	// different rows.
	const total = auditReadyChainWindow + 150
	seedAuditChain(t, s, total)

	verdict := s.verifyAuditChain(auditReadyChainWindow)
	if !verdict.OK {
		t.Fatalf("an intact chain was reported broken: %+v", verdict)
	}
	if verdict.Checked != auditReadyChainWindow {
		t.Fatalf("checked %d rows, want the full window %d", verdict.Checked, auditReadyChainWindow)
	}

	// Tamper with the NEWEST row: content changed, hash left alone.
	var newest db.AuditLog
	if err := s.db.Order("id DESC").First(&newest).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", newest.ID).Update("details", "rewritten").Error; err != nil {
		t.Fatal(err)
	}

	verdict = s.verifyAuditChain(auditReadyChainWindow)
	if verdict.OK {
		t.Fatalf("tampering with the newest row went unnoticed; the verifier is still reading the oldest window")
	}
	if verdict.FirstBadID != newest.ID {
		t.Fatalf("first bad row = %d, want the tampered newest row %d", verdict.FirstBadID, newest.ID)
	}
}

// TestAuditVerifySeesDeletionNearTheHead is the same property expressed the other way:
// removing a row near the head must break the link, which only a newest-window read
// can notice.
func TestAuditVerifySeesDeletionNearTheHead(t *testing.T) {
	s := anchorTestServer(t)
	const total = auditReadyChainWindow + 120
	seedAuditChain(t, s, total)

	var newest db.AuditLog
	if err := s.db.Order("id DESC").First(&newest).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Where("id = ?", newest.ID-1).Delete(&db.AuditLog{}).Error; err != nil {
		t.Fatal(err)
	}

	verdict := s.verifyAuditChain(auditReadyChainWindow)
	if verdict.OK {
		t.Fatal("deleting a row near the head went unnoticed")
	}
	if verdict.FirstBadID != newest.ID {
		t.Fatalf("first bad row = %d, want %d (the row that lost its predecessor)", verdict.FirstBadID, newest.ID)
	}
}
