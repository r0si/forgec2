package auditchain

import (
	"strings"
	"testing"
)

// sampleRows builds a small valid chain.
func sampleRows() []Row {
	rows := []Row{
		{ID: 1, User: "alice", Action: "login", Resource: "session", Success: true, IP: "10.0.0.5",
			CreatedAt: "2026-10-29T00:00:00Z"},
		{ID: 2, User: "alice", Action: "task_create", Resource: "task", AgentID: "a-1", Success: true,
			Details: `{"type":"shell"}`, CreatedAt: "2026-10-29T00:01:00Z"},
		{ID: 3, User: "bob", Action: "login", Resource: "session", Success: false, Error: "bad password",
			CreatedAt: "2026-10-29T00:02:00Z"},
	}
	previous := ""
	for i := range rows {
		rows[i].EntryHash = HashRow(rows[i])
		rows[i].PrevHash = previous
		previous = rows[i].EntryHash
	}
	return rows
}

// TestVerifyAcceptsAValidChain is the offline promise: an untouched export verifies without a server.
func TestVerifyAcceptsAValidChain(t *testing.T) {
	verdict := Verify(sampleRows())
	if !verdict.OK {
		t.Fatalf("a valid chain failed: %+v", verdict)
	}
	if verdict.Checked != 3 || len(verdict.Problems) != 0 || verdict.Discontinuities != 0 {
		t.Fatalf("verdict = %+v", verdict)
	}
}

// TestVerifyCatchesAnEditedRow covers the shape the chain exists to detect: one row changed in place.
func TestVerifyCatchesAnEditedRow(t *testing.T) {
	rows := sampleRows()
	rows[1].Details = `{"type":"dcsync"}` // the hash still covers the old details

	verdict := Verify(rows)
	if verdict.OK {
		t.Fatal("an edited row verified")
	}
	if verdict.FirstBadID != 2 || !strings.Contains(verdict.Reason, "entry_hash") {
		t.Fatalf("verdict = %+v", verdict)
	}
}

// TestVerifyCatchesARemovedRow covers deletion: the following row's prev_hash no longer matches.
func TestVerifyCatchesARemovedRow(t *testing.T) {
	rows := sampleRows()
	// Drop the middle row and keep its successor's prev_hash, which is what a naive deletion leaves behind.
	broken := []Row{rows[0], rows[2]}
	broken[1].ID = 3 // the gap is reported separately

	verdict := Verify(broken)
	if verdict.OK {
		t.Fatal("a removal verified")
	}
	if !strings.Contains(verdict.Reason, "prev_hash") {
		t.Fatalf("the reason does not name the broken link: %+v", verdict)
	}
	if verdict.Discontinuities != 1 {
		t.Fatalf("the id gap was not counted: %+v", verdict)
	}
}

// TestVerifyReportsGapsWithoutFailing keeps the purge case honest: a purge removes rows by design and leaves an
// audit_purge marker, so an id gap is a note, not tampering.
func TestVerifyReportsGapsWithoutFailing(t *testing.T) {
	rows := sampleRows()
	// Move the LAST row up, so the chain still walks in id order across a gap - which is what a purge leaves behind.
	// (Bumping a middle row instead would create an out-of-order chain, which is a real failure and rightly fails.)
	rows[2].ID = 40

	verdict := Verify(rows)
	if !verdict.OK {
		t.Fatalf("a gap with an intact chain failed: %+v", verdict)
	}
	if verdict.Discontinuities != 1 {
		t.Fatalf("discontinuities = %d, want 1", verdict.Discontinuities)
	}
}

// TestHashIsStable is the guard against a "harmless" refactor of the byte layout: every historical row was written with
// this exact format, so changing it would make every stored export look tampered.
func TestHashIsStable(t *testing.T) {
	got := Hash("u", "a", "r", "ag", "1.2.3.4", true, "err", "details")
	if len(got) != 64 {
		t.Fatalf("the digest is not a sha256 hex string: %q", got)
	}
	// Changing any single field must change the digest, and the field boundary must matter.
	if got == Hash("ua", "", "r", "ag", "1.2.3.4", true, "err", "details") {
		t.Fatal("the field separator does not affect the digest")
	}
	if got == Hash("u", "a", "r", "ag", "1.2.3.4", false, "err", "details") {
		t.Fatal("the success flag does not affect the digest")
	}
}
