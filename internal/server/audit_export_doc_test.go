package server

import (
	"os"
	"strings"
	"testing"
)

// TestAuditExportDocMatchesTheImplementation keeps docs/AUDIT_EXPORT.md honest.
//
// The document is the third party's instructions for verifying a copy of the audit log, so a renamed parameter, a new
// format or a different CLI would leave them following steps that no longer work - and the failure would look like
// tampering. A comment cannot enforce that; reading the file can.
func TestAuditExportDocMatchesTheImplementation(t *testing.T) {
	raw, err := os.ReadFile("../../docs/AUDIT_EXPORT.md")
	if err != nil {
		t.Fatalf("the audit export document is missing: %v", err)
	}
	doc := string(raw)

	// 1. The verifier they are told to run must exist and be named exactly.
	if _, err := os.Stat("../../cmd/verify-audit/main.go"); err != nil {
		t.Fatalf("the document names cmd/verify-audit but it is missing: %v", err)
	}
	for _, needle := range []string{"cmd/verify-audit", "/api/audit/export"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the document does not mention %q", needle)
		}
	}

	// 2. Every query parameter the handler honours is documented, so a reader can build the request they need.
	for _, parameter := range []string{"since_id", "since", "until", "format"} {
		if !strings.Contains(doc, parameter) {
			t.Fatalf("the document does not document the %q parameter", parameter)
		}
	}

	// 3. The headers a reader must inspect are named.
	for _, header := range []string{"X-Audit-Export-Truncated", "X-Audit-Export-Filtered", "X-Audit-Export-Rows"} {
		if !strings.Contains(doc, header) {
			t.Fatalf("the document does not mention the %s header", header)
		}
	}

	// 4. The limitation section is the point of the document: if these words disappear, the doc is claiming more than
	// the implementation can do.
	for _, needle := range []string{"unkeyed", "partial", "prev_hash"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the document no longer states the limit (%q missing)", needle)
		}
	}

	// 5. The purge marker is what makes an id gap explainable, and the verifier reports gaps separately.
	if !strings.Contains(doc, "audit_purge") || !strings.Contains(doc, "discontinuities") {
		t.Fatal("the document does not explain id gaps and the purge marker")
	}
}
