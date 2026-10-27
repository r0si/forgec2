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

// TestHandleExportAuditChainSinceID pins the incremental export: an investigator
// who already holds a verified copy fetches only the rows appended since it, which
// is what makes periodic anchoring practical on a long engagement.
func TestHandleExportAuditChainSinceID(t *testing.T) {
	s := mustTenantServer(t)
	seedAuditChain(t, s, 4)

	var rows []db.AuditLog
	if err := s.db.Where("action = ?", "probe").Order("id asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("seeded %d rows", len(rows))
	}
	anchor := rows[1].ID // everything after the second row

	export := func(query string) []auditChainExportRow {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export"+query, nil)
		c.Set("user", "admin1")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		s.handleExportAuditChain(c)
		if w.Code != http.StatusOK {
			t.Fatalf("export%s = %d (%s)", query, w.Code, w.Body.String())
		}
		var out []auditChainExportRow
		scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var row auditChainExportRow
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("bad line: %v", err)
			}
			out = append(out, row)
		}
		return out
	}

	incremental := export(fmt.Sprintf("?since_id=%d", anchor))
	if len(incremental) != 2 {
		t.Fatalf("incremental export returned %d rows, want 2", len(incremental))
	}
	if incremental[0].ID != rows[2].ID {
		t.Fatalf("first incremental row = %d, want %d", incremental[0].ID, rows[2].ID)
	}
	// The link to the row the investigator already holds is still present, so the
	// two copies can be stitched together.
	if incremental[0].PrevHash != rows[1].EntryHash {
		t.Fatal("the incremental export does not link to the anchor row")
	}

	// An anchor past the end yields an empty (not failing) export.
	if got := export(fmt.Sprintf("?since_id=%d", rows[3].ID+100)); len(got) != 0 {
		t.Fatalf("an over-anchored export returned %d rows", len(got))
	}
	// No anchor keeps the full export.
	if got := export(""); len(got) != 4 {
		t.Fatalf("full export returned %d rows, want 4", len(got))
	}
}
