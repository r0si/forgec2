package server

import (
	"os"
	"strings"
	"testing"
)

// TestHandoverDocMatchesTheImplementation keeps docs/HANDOVER_BUNDLE.md honest.
//
// The document tells a receiver who was not there what the archive contains and how to check it - so a member added to
// the bundle without being documented, or a renamed environment variable, would leave the reader with instructions that
// no longer describe the artefact. A comment cannot enforce that; this can.
func TestHandoverDocMatchesTheImplementation(t *testing.T) {
	raw, err := os.ReadFile("../../docs/HANDOVER_BUNDLE.md")
	if err != nil {
		t.Fatalf("the handover document is missing: %v", err)
	}
	doc := string(raw)

	// 1. Every member the handler can write is named.
	for _, name := range bundleMemberNames {
		if !strings.Contains(doc, name) {
			t.Fatalf("docs/HANDOVER_BUNDLE.md does not mention the %s member", name)
		}
	}
	// The manifest is always written and must be documented too.
	if !strings.Contains(doc, "MANIFEST.json") {
		t.Fatal("the document does not mention MANIFEST.json")
	}

	// 2. The signing variable is named exactly, because a reader will copy it.
	if !strings.Contains(doc, bundleSigningKeyEnv) {
		t.Fatalf("the document does not name %s", bundleSigningKeyEnv)
	}

	// 3. Both verification paths are named, including the CLI and the query parameter that turns validity into trust.
	for _, needle := range []string{"cmd/verify-bundle", "/bundle/verify", "public_key", "-key"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the document does not mention %q", needle)
		}
	}

	// 4. The distinction the implementation makes is the distinction the document explains in words.
	for _, needle := range []string{"valid", "trusted", "unsigned"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the document does not explain %q", needle)
		}
	}

	// 5. The selection parameter is documented, along with the rule that the manifest always travels.
	if !strings.Contains(doc, "include=") {
		t.Fatal("the document does not explain ?include=")
	}
}
