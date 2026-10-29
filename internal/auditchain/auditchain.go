// Package auditchain verifies an exported audit log offline.
//
// The export endpoint has promised since it was written that "a third party can verify offline", but the hash and the
// linkage only existed in the server: anyone holding the file needed a running console to check it, which for a third
// party means not checking it at all. The hash lives here so the server and `cmd/verify-audit` compute the SAME digest -
// two implementations would produce a false tampering alarm on one side.
//
// What this can and cannot prove is the same thing the server's verifier says: the hash is unkeyed and the previous hash
// is not an input, so it detects PARTIAL edits (one row changed, a row removed, a link rewritten) and not an adversary
// who recomputes every row. Saying so is part of the package, not a footnote.
package auditchain

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// Row is one exported audit entry, in the shape the export writes.
type Row struct {
	ID        uint   `json:"id"`
	TenantID  uint   `json:"tenant_id"`
	User      string `json:"user"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	AgentID   string `json:"agent_id"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
	Error     string `json:"error"`
	Details   string `json:"details"`
	PrevHash  string `json:"prev_hash"`
	EntryHash string `json:"entry_hash"`
	CreatedAt string `json:"created_at"`
}

// Hash is the digest of one entry's fields.
//
// The exact byte layout is load-bearing: it must match what every historical row was written with, or every export
// appears tampered. Fields are joined with "|" and the boolean rendered by %t, exactly as the server has always done.
func Hash(user, action, resource, agentID, ip string, success bool, errText, details string) string {
	var buf []byte
	buf = fmt.Appendf(buf, "%s|%s|%s|%s|%s|%t|%s|%s",
		user, action, resource, agentID, ip, success, errText, details)
	sum := sha256.Sum256(buf)
	return fmt.Sprintf("%x", sum)
}

// HashRow renders the digest the row claims to have.
func HashRow(row Row) string {
	return Hash(row.User, row.Action, row.Resource, row.AgentID, row.IP, row.Success, row.Error, row.Details)
}

// Problem is one row that does not check out.
type Problem struct {
	ID      uint   `json:"id"`
	Problem string `json:"problem"`
}

// Verdict is the result of walking an export.
type Verdict struct {
	OK         bool      `json:"ok"`
	Checked    int       `json:"checked"`
	Problems   []Problem `json:"problems"`
	FirstBadID uint      `json:"first_bad_id,omitempty"`
	// Discontinuities counts id gaps, which a purge legitimately leaves behind (it writes a marker row).
	Discontinuities int `json:"discontinuities"`
	// Reason summarises the first failure for a one-line answer.
	Reason string `json:"reason,omitempty"`
	// SelectionKnown reports whether the file itself said whether it is a filtered selection. The newline-delimited
	// export keeps that in response headers, which a file does not have, so "unknown" is a real answer here.
	SelectionKnown bool `json:"selection_known"`
	// Filtered reports the file's own admission that the rows are a selection rather than the whole chain.
	Filtered bool `json:"filtered"`
	// SignatureKind says which of this product's four "signatures" this verdict is about. The word is overloaded enough that a
	// reader can take this chain - an UNKEYED hash - for a keyed signature, and then believe the log is authenticated when it
	// is only partially tamper-evident. Carried in the verdict so the correction travels with the data.
	SignatureKind string `json:"signature_kind"`
}

// SignatureKind is the description every audit verdict carries.
//
// It lives here, beside the verifier, because both the server and cmd/verify-audit report this verdict and neither should have
// to import the other to say the same sentence.
const SignatureKind = "audit hash chain entry: unkeyed, detects partial edits, does not resist a full rewrite"

// Verify walks the rows in id order and checks every digest and every link.
//
// The rows are sorted by id rather than trusted in file order: an export is a list, and a verifier that followed the
// order of the file could be walked backwards.
func Verify(rows []Row) Verdict {
	ordered := append([]Row(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	verdict := Verdict{OK: true, Problems: []Problem{}}
	var previous *Row
	for i := range ordered {
		row := &ordered[i]
		verdict.Checked++

		if got := HashRow(*row); got != row.EntryHash {
			verdict.record(row.ID, "row content does not match its entry_hash: "+got+" != "+row.EntryHash)
			continue
		}
		if previous != nil {
			// A gap in ids is what a legitimate purge leaves; a broken link is not. Both are reported, only the
			// second fails.
			if row.ID != previous.ID+1 {
				verdict.Discontinuities++
			}
			if row.PrevHash != previous.EntryHash {
				verdict.record(row.ID, "prev_hash does not match the previous entry")
				continue
			}
		}
		previous = row
	}
	return verdict
}

// record notes a failure without stopping the walk, so a report lists what is wrong rather than only the first.
func (v *Verdict) record(id uint, problem string) {
	v.OK = false
	v.Problems = append(v.Problems, Problem{ID: id, Problem: problem})
	if v.FirstBadID == 0 {
		v.FirstBadID = id
		v.Reason = problem
	}
}
