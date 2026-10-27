package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// seedAuditChain writes a small chain through the real persistence path so the
// hashes and links are exactly what production writes.
func seedAuditChain(t *testing.T, s *Server, n int) []db.AuditLog {
	t.Helper()
	for i := 0; i < n; i++ {
		s.LogAuditRecord(nil, "probe", "agent", "a1", "step "+string(rune('a'+i)), true, nil)
	}
	var rows []db.AuditLog
	if err := s.db.Where("action = ?", "probe").Order("id asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != n {
		t.Fatalf("seeded %d rows, want %d", len(rows), n)
	}
	return rows
}

// TestVerifyAuditChainAcceptsIntactChain is the baseline: what the server writes
// must verify.
func TestVerifyAuditChainAcceptsIntactChain(t *testing.T) {
	s := mustTenantServer(t)
	seedAuditChain(t, s, 4)

	verdict := s.verifyAuditChain(100)
	if !verdict.OK {
		t.Fatalf("an intact chain failed verification: %+v", verdict)
	}
	if verdict.Checked != 4 {
		t.Fatalf("checked = %d, want 4", verdict.Checked)
	}

	// An empty table verifies trivially.
	empty := mustTenantServer(t)
	if v := empty.verifyAuditChain(10); !v.OK || v.Checked != 0 {
		t.Fatalf("empty chain = %+v", v)
	}
	// A nil server must not panic.
	var bare *Server
	if v := bare.verifyAuditChain(10); !v.OK {
		t.Fatalf("nil server = %+v", v)
	}
}

// TestVerifyAuditChainDetectsEditedRow pins the property the chain exists for: a
// row whose content was changed after the fact no longer matches its own hash.
func TestVerifyAuditChainDetectsEditedRow(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 3)

	// Edit the middle row's details behind the server's back (no hash update).
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[1].ID).
		Update("details", "tampered").Error; err != nil {
		t.Fatal(err)
	}

	verdict := s.verifyAuditChain(100)
	if verdict.OK {
		t.Fatal("an edited row passed verification")
	}
	if verdict.FirstBadID != rows[1].ID {
		t.Fatalf("first bad id = %d, want %d (%+v)", verdict.FirstBadID, rows[1].ID, verdict)
	}
	if verdict.Reason == "" {
		t.Fatal("no reason was reported")
	}
}

// TestVerifyAuditChainDetectsDeletedRow pins the other half: removing a row breaks
// the previous-hash link even though every remaining row is internally consistent.
func TestVerifyAuditChainDetectsDeletedRow(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 4)

	if err := s.db.Unscoped().Delete(&db.AuditLog{}, rows[1].ID).Error; err != nil {
		t.Fatal(err)
	}

	verdict := s.verifyAuditChain(100)
	if verdict.OK {
		t.Fatal("a deleted row passed verification")
	}
	if verdict.FirstBadID != rows[2].ID {
		t.Fatalf("first bad id = %d, want %d (%+v)", verdict.FirstBadID, rows[2].ID, verdict)
	}
}

// TestVerifyAuditChainDetectsReordering pins the third tamper shape: two rows with
// swapped hashes no longer link up.
func TestVerifyAuditChainDetectsReordering(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 3)

	// Swap the EntryHash of rows 1 and 2 without touching their content.
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[1].ID).
		Update("entry_hash", rows[2].EntryHash).Error; err != nil {
		t.Fatal(err)
	}

	if verdict := s.verifyAuditChain(100); verdict.OK {
		t.Fatal("a swapped hash passed verification")
	}
}

// TestHandleVerifyAuditChainEndpoint covers the read-only endpoint: an intact chain
// answers ok=true, a broken one answers 200 with ok=false (a finding, not a 500).
func TestHandleVerifyAuditChainEndpoint(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 3)

	call := func() (*httptest.ResponseRecorder, auditChainVerdict) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/verify", nil)
		c.Set("user", "admin1")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		s.handleVerifyAuditChain(c)
		var out struct {
			Data auditChainVerdict `json:"data"`
		}
		if w.Code == http.StatusOK {
			unmarshalData(t, w.Body.Bytes(), &out.Data)
		}
		return w, out.Data
	}

	w, verdict := call()
	if w.Code != http.StatusOK || !verdict.OK {
		t.Fatalf("intact chain = %d %+v", w.Code, verdict)
	}

	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[0].ID).Update("success", false).Error; err != nil {
		t.Fatal(err)
	}
	w, verdict = call()
	if w.Code != http.StatusOK {
		t.Fatalf("a broken chain answered %d, want 200", w.Code)
	}
	if verdict.OK {
		t.Fatal("the endpoint reported an edited chain as intact")
	}

	// The failure itself is recorded, so the finding is in the log.
	var count int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "audit_chain_verify").Count(&count)
	if count == 0 {
		t.Fatal("the failed verification was not audited")
	}
	_ = time.Now()
}
