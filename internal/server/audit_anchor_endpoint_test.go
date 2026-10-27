package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func anchorCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/anchor", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestAuditChainAnchorEndpoint pins the out-of-band archiving workflow: the endpoint
// returns the newest row's fingerprint so an operator can paste it into a report,
// and says whether the server-side anchor agrees with the table.
func TestAuditChainAnchorEndpoint(t *testing.T) {
	s := anchorTestServer(t)
	seedAuditChain(t, s, 3)

	var newest db.AuditLog
	if err := s.db.Where("action = ?", "probe").Order("id DESC").First(&newest).Error; err != nil {
		t.Fatal(err)
	}

	c, w := anchorCtx()
	s.handleAuditChainAnchor(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var head struct {
		ID             uint   `json:"id"`
		EntryHash      string `json:"entry_hash"`
		CreatedAt      string `json:"created_at"`
		AnchorMatches  bool   `json:"anchor_matches"`
		AnchorRecorded bool   `json:"anchor_recorded"`
	}
	unmarshalData(t, w.Body.Bytes(), &head)
	if head.ID != newest.ID || head.EntryHash != newest.EntryHash {
		t.Fatalf("head = %+v, want id %d hash %s", head, newest.ID, newest.EntryHash)
	}
	if !head.AnchorRecorded || !head.AnchorMatches {
		t.Fatalf("the anchor state is wrong: %+v", head)
	}
	if head.CreatedAt == "" {
		t.Fatal("the head carries no timestamp")
	}

	// A rewritten table keeps internal consistency but stops matching the anchor.
	tampered := newest
	tampered.Details = "rewritten"
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", newest.ID).Update("details", tampered.Details).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", newest.ID).Update("entry_hash", auditEntryHash(&tampered)).Error; err != nil {
		t.Fatal(err)
	}
	c, w = anchorCtx()
	s.handleAuditChainAnchor(c)
	unmarshalData(t, w.Body.Bytes(), &head)
	if head.AnchorMatches {
		t.Fatal("a rewritten head still matched the anchor file")
	}
	if head.EntryHash == "" {
		t.Fatal("the head lost its hash after the rewrite")
	}
}

// TestAuditChainAnchorWithoutAnchorFile covers a fresh install: no anchor exists
// yet, which must read as "not recorded", not as a mismatch.
func TestAuditChainAnchorWithoutAnchorFile(t *testing.T) {
	s := mustTenantServer(t) // no data dir, so no anchor file
	seedAuditChain(t, s, 2)

	c, w := anchorCtx()
	s.handleAuditChainAnchor(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var head struct {
		ID             uint `json:"id"`
		AnchorRecorded bool `json:"anchor_recorded"`
		AnchorMatches  bool `json:"anchor_matches"`
	}
	unmarshalData(t, w.Body.Bytes(), &head)
	if head.ID == 0 {
		t.Fatal("no head was reported")
	}
	if head.AnchorRecorded {
		t.Fatal("an anchor was reported without an anchor file")
	}
	if !head.AnchorMatches {
		t.Fatal("a missing anchor file was reported as a mismatch")
	}

	// A server without a database answers 503 instead of panicking.
	bare := &Server{}
	c, w = anchorCtx()
	bare.handleAuditChainAnchor(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("bare server = %d, want 503", w.Code)
	}
}
