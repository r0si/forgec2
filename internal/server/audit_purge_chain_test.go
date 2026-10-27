package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestPurgeAuditLogsRecordsItself pins the forensic rule the code comments already
// claim: destroying audit history must leave a record of its own. Before this, a
// purge silently removed rows and left nothing behind saying who did it or how many.
func TestPurgeAuditLogsRecordsItself(t *testing.T) {
	s := mustTenantServer(t)

	// Old rows that are eligible (the handler floors the window at 90 days).
	old := time.Now().AddDate(0, 0, -200)
	for i := 0; i < 3; i++ {
		row := db.AuditLog{User: "admin1", Action: "old_probe", Details: "x", Success: true, CreatedAt: old}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A recent row that must survive.
	keep := db.AuditLog{User: "admin1", Action: "recent_probe", Details: "y", Success: true, CreatedAt: time.Now()}
	if err := s.db.Create(&keep).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"days": {"90"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/settings/purge/audit", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handlePurgeAuditLogs(c)

	if w.Code != http.StatusOK {
		t.Fatalf("purge = %d (%s)", w.Code, w.Body.String())
	}

	var oldLeft int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "old_probe").Count(&oldLeft)
	if oldLeft != 0 {
		t.Fatalf("%d old rows survived the purge", oldLeft)
	}
	var keepLeft int64
	s.db.Model(&db.AuditLog{}).Where("action = ?", "recent_probe").Count(&keepLeft)
	if keepLeft != 1 {
		t.Fatal("the recent row was purged")
	}

	// The purge itself is recorded, with the count.
	var markers []db.AuditLog
	if err := s.db.Where("action = ?", "audit_purge").Find(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if len(markers) != 1 {
		t.Fatalf("purge markers = %d, want 1", len(markers))
	}
	if !strings.Contains(markers[0].Details, "3") || !strings.Contains(markers[0].Details, "90") {
		t.Fatalf("the marker does not describe the purge: %q", markers[0].Details)
	}
}

// TestPurgeDoesNotTripTheChainVerifier pins the interaction with the verifier: a
// legitimate purge removes old rows, which breaks the previous-hash link at the
// new head. Reporting that as tampering would make /ready cry wolf after the
// server's own maintenance.
func TestPurgeDoesNotTripTheChainVerifier(t *testing.T) {
	s := mustTenantServer(t)

	// Old rows must go through the real write path so they carry a valid hash;
	// created_at is not part of the hash, so backdating them is chain-neutral.
	for i := 0; i < 3; i++ {
		s.LogAuditRecord(nil, "old_probe", "audit", "", "old", true, nil)
	}
	old := time.Now().AddDate(0, 0, -200)
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "old_probe").Update("created_at", old).Error; err != nil {
		t.Fatal(err)
	}
	recent := seedAuditChain(t, s, 3)

	// Baseline: intact.
	if v := s.verifyAuditChain(100); !v.OK {
		t.Fatalf("baseline failed: %+v", v)
	}

	// Purge everything older than 90 days through the real handler.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	form := url.Values{"days": {"90"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/settings/purge/audit", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handlePurgeAuditLogs(c)

	verdict := s.verifyAuditChain(100)
	if !verdict.OK {
		t.Fatalf("a legitimate purge was reported as tampering: %+v", verdict)
	}
	if verdict.Checked == 0 {
		t.Fatal("nothing was verified after the purge")
	}
	// The surviving recent rows are still chained to each other.
	var survivors []db.AuditLog
	if err := s.db.Where("id >= ?", recent[0].ID).Order("id asc").Find(&survivors).Error; err != nil {
		t.Fatal(err)
	}
	if len(survivors) == 0 {
		t.Fatal("the recent rows were purged")
	}
}

// TestVerifyAuditChainStillCatchesInteriorDeletion is the control: tolerating a
// purge boundary must not tolerate a row removed from the MIDDLE of the chain.
func TestVerifyAuditChainStillCatchesInteriorDeletion(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 4)

	// Delete an interior row (its neighbours survive, so ids stay non-contiguous
	// but the gap is not at the window start).
	if err := s.db.Unscoped().Delete(&db.AuditLog{}, rows[1].ID).Error; err != nil {
		t.Fatal(err)
	}

	verdict := s.verifyAuditChain(100)
	if verdict.OK {
		t.Fatal("an interior deletion passed verification")
	}
	if verdict.FirstBadID != rows[2].ID {
		t.Fatalf("first bad id = %d, want %d", verdict.FirstBadID, rows[2].ID)
	}
}
