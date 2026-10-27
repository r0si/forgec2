package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestHandleExportAuditChainStreamsVerifiableRows pins the offline-verification
// promise: the export carries both hashes and every hashed field, so a verifier
// outside the server can recompute the chain from the file alone.
func TestHandleExportAuditChainStreamsVerifiableRows(t *testing.T) {
	s := mustTenantServer(t)
	seedAuditChain(t, s, 3)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleExportAuditChain(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Fatalf("content type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "forgec2-audit-chain.ndjson") {
		t.Fatalf("disposition = %q", cd)
	}

	var rows []auditChainExportRow
	scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row auditChainExportRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line is not valid JSON: %v (%s)", err, line)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("exported %d rows, want 3", len(rows))
	}

	// An out-of-process verifier: recompute each hash from the hashed fields and
	// check the linkage, using only the exported data.
	for i, row := range rows {
		buf := []byte(fmt.Sprintf("%s|%s|%s|%s|%s|%t|%s|%s",
			row.User, row.Action, row.Resource, row.AgentID, row.IP, row.Success, row.Error, row.Details))
		sum := sha256.Sum256(buf)
		if got := fmt.Sprintf("%x", sum); got != row.EntryHash {
			t.Fatalf("row %d does not verify from the export: %s != %s", row.ID, got, row.EntryHash)
		}
		if i > 0 && row.PrevHash != rows[i-1].EntryHash {
			t.Fatalf("row %d is not linked to the previous exported row", row.ID)
		}
		if row.CreatedAt == "" {
			t.Fatalf("row %d has no timestamp", row.ID)
		}
	}

	// A tampered export no longer verifies — that is the point of handing a copy
	// to a third party.
	rows[1].Details = "edited after export"
	buf := []byte(fmt.Sprintf("%s|%s|%s|%s|%s|%t|%s|%s",
		rows[1].User, rows[1].Action, rows[1].Resource, rows[1].AgentID, rows[1].IP, rows[1].Success, rows[1].Error, rows[1].Details))
	sum := sha256.Sum256(buf)
	if fmt.Sprintf("%x", sum) == rows[1].EntryHash {
		t.Fatal("an edited row still matched its hash")
	}
}

// TestHandleExportAuditChainGuards covers the degraded shapes: no database, and a
// limit that is out of range falling back to the default.
func TestHandleExportAuditChainGuards(t *testing.T) {
	bare := &Server{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export", nil)
	bare.handleExportAuditChain(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a server without a database answered %d, want 503", w.Code)
	}

	s := mustTenantServer(t)
	seedAuditChain(t, s, 2)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export?limit=not-a-number", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleExportAuditChain(c)
	if w.Code != http.StatusOK {
		t.Fatalf("bad limit answered %d", w.Code)
	}
	if lines := strings.Count(strings.TrimSpace(w.Body.String()), "\n") + 1; lines != 2 {
		t.Fatalf("exported %d lines, want 2", lines)
	}
}
