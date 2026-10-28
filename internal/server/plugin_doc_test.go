package server

import (
	"os"
	"strings"
	"testing"
)

// TestPluginGuideCoversReceiving keeps the "verifying a package you received" section of docs/PLUGIN_AUTHORING.md honest.
//
// The guide's main audience is authors, and its trust section originally ended at "install it". A receiver - the person
// who was handed a zip - had no entry point in it, and would reach for plugin-check, which takes a directory and treats
// signatures as optional: exactly the two properties that do not apply to a package you did not build.
func TestPluginGuideCoversReceiving(t *testing.T) {
	raw, err := os.ReadFile("../../docs/PLUGIN_AUTHORING.md")
	if err != nil {
		t.Fatalf("the plugin guide is missing: %v", err)
	}
	doc := string(raw)

	if _, err := os.Stat("../../cmd/verify-plugin/main.go"); err != nil {
		t.Fatalf("the guide names cmd/verify-plugin but it is missing: %v", err)
	}
	for _, needle := range []string{"## Trust and installation", "Verifying a package you received", "cmd/verify-plugin"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the plugin guide does not mention %q", needle)
		}
	}

	// The three differences from plugin-check are the reason the command exists; if any stops being stated, a receiver
	// is left with the wrong instructions.
	for _, needle := range []string{"archive", "-key", "RequireSigned"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the receiving section no longer states %q as a difference from plugin-check", needle)
		}
	}

	// The escape guard is a security property, not a detail: it must stay visible to whoever maintains the extractor.
	if !strings.Contains(doc, "outside the") {
		t.Fatal("the guide no longer mentions that archive entries escaping the destination are refused")
	}

	// The exit-code contract is shared with the other verifiers, and the guide points there rather than restating it
	// differently.
	if !strings.Contains(doc, "docs/VERIFIERS.md") {
		t.Fatal("the receiving section does not point at the shared exit-code contract")
	}
}
