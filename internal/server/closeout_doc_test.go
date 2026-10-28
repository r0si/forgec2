package server

import (
	"os"
	"strings"
	"testing"
)

// TestCloseoutGuideMatchesTheImplementation keeps docs/ENGAGEMENT_CLOSEOUT.md usable.
//
// The page is a procedure, so every step in it must be a request the server actually answers and a flag the verifier
// actually has. A closeout runbook that has drifted is worse than none: it is followed under time pressure, at the end of an
// exercise, by someone who will not re-check it.
func TestCloseoutGuideMatchesTheImplementation(t *testing.T) {
	raw, err := os.ReadFile("../../docs/ENGAGEMENT_CLOSEOUT.md")
	if err != nil {
		t.Fatalf("the closeout guide is missing: %v", err)
	}
	doc := string(raw)

	// 1. Every endpoint it tells the operator to call is registered and documented.
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	for _, path := range []string{
		"/api/engagements/{id}/close",
		"/api/agents/bulk/expiry",
		"/api/engagements/{id}/bundle",
	} {
		if !strings.Contains(doc, strings.ReplaceAll(path, "{id}", "7")) && !strings.Contains(doc, path) {
			t.Fatalf("the guide does not mention %s", path)
		}
		if !strings.Contains(string(spec), path+":") {
			t.Fatalf("%s is used by the guide but is not in api/openapi.yaml", path)
		}
	}

	// 2. The verifier invocation uses flags that exist.
	if _, err := os.Stat("../../cmd/verify-bundle/main.go"); err != nil {
		t.Fatalf("the guide names cmd/verify-bundle but it is missing: %v", err)
	}
	for _, flag := range []string{"-key", "-require-complete"} {
		if !strings.Contains(doc, flag) {
			t.Fatalf("the guide no longer uses %s", flag)
		}
	}

	// 3. The exit codes it explains are the ones the tool returns, including the two that are easy to conflate.
	for _, code := range []string{"`0`", "`1`", "`2`", "`3`", "`4`"} {
		if !strings.Contains(doc, code) {
			t.Fatalf("the guide does not explain exit %s", code)
		}
	}

	// 4. The two facts a handover must not get wrong: credentials stay behind, and the manifest always travels.
	for _, needle := range []string{"metadata only", "MANIFEST.json"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the guide does not state %q", needle)
		}
	}

	// 5. And the audit trail it promises exists: every action it names is one the handlers write.
	for _, action := range []string{"engagement_bundle", "engagement_bundle_verify", "engagement_close_stamp"} {
		if !strings.Contains(doc, action) {
			t.Fatalf("the guide does not mention the %s audit action", action)
		}
	}
}
