package server

import (
	"strings"
	"testing"
)

// TestSignatureKindsAreDistinctAndPublished pins the disambiguation at the source rather than only in the docs.
//
// Four different objects in this system are called a "signature". The expensive confusion is taking the audit chain's unkeyed
// hash for a keyed signature - that turns "partially tamper-evident" into "authenticated" - so each response that reports one
// names its kind, and the kinds must stay distinguishable text.
func TestSignatureKindsAreDistinctAndPublished(t *testing.T) {
	kinds := map[string]string{
		"audit":   auditSignatureKind,
		"bundle":  bundleSignatureKind,
		"plugin":  pluginSignatureKind,
		"failure": failureSignatureKind,
	}
	seen := map[string]string{}
	for name, text := range kinds {
		if strings.TrimSpace(text) == "" {
			t.Fatalf("the %s signature kind is not described", name)
		}
		if other, clash := seen[text]; clash {
			t.Fatalf("%s and %s share one description; a reader still cannot tell them apart", name, other)
		}
		seen[text] = name
	}

	// The two properties the descriptions exist to carry, stated where a client will read them.
	if !strings.Contains(auditSignatureKind, "unkeyed") {
		t.Fatalf("the audit kind does not say it is unkeyed: %q", auditSignatureKind)
	}
	if !strings.Contains(strings.ToLower(auditSignatureKind), "does not resist") {
		t.Fatalf("the audit kind does not state its limit: %q", auditSignatureKind)
	}
	if !strings.Contains(failureSignatureKind, "not a cryptographic signature") {
		t.Fatalf("the failure-shape kind does not deny being cryptographic: %q", failureSignatureKind)
	}
	for _, keyed := range []string{bundleSignatureKind, pluginSignatureKind} {
		if !strings.Contains(keyed, "ed25519") {
			t.Fatalf("a keyed signature is not named as such: %q", keyed)
		}
	}
}
