package server

import (
	"os"
	"strings"
	"testing"
)

// TestVerifierGuideMatchesTheCommands keeps docs/VERIFIERS.md honest.
//
// The page promises a command per artefact and one shared exit-code contract. Both are easy to break silently: a renamed
// cmd directory, a new exit code, or a fourth verifier nobody documented. A reader who follows a stale instruction sees
// a failure and cannot tell it from a real one.
func TestVerifierGuideMatchesTheCommands(t *testing.T) {
	raw, err := os.ReadFile("../../docs/VERIFIERS.md")
	if err != nil {
		t.Fatalf("the verifier guide is missing: %v", err)
	}
	doc := string(raw)

	// 1. Every documented command exists on disk, and every command on disk is documented.
	commands := map[string]string{
		"verify-bundle": "cmd/verify-bundle/main.go",
		"verify-audit":  "cmd/verify-audit/main.go",
		"verify-plugin": "cmd/verify-plugin/main.go",
	}
	for name, path := range commands {
		if _, err := os.Stat("../../" + path); err != nil {
			t.Fatalf("%s is documented but %s is missing: %v", name, path, err)
		}
		if !strings.Contains(doc, "cmd/"+name) {
			t.Fatalf("docs/VERIFIERS.md does not document cmd/%s", name)
		}
	}
	if entries, err := os.ReadDir("../../cmd"); err == nil {
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "verify-") {
				continue
			}
			if _, documented := commands[entry.Name()]; !documented {
				t.Fatalf("cmd/%s exists but docs/VERIFIERS.md does not describe it", entry.Name())
			}
		}
	}

	// 2. The exit-code contract names every code the commands actually return, including the trust-decision code that
	// only verify-bundle produces.
	for _, code := range []string{"`0`", "`1`", "`2`", "`3`", "`4`"} {
		if !strings.Contains(doc, code) {
			t.Fatalf("the exit-code table does not mention %s", code)
		}
	}
	if !strings.Contains(doc, "-require-trusted") {
		t.Fatal("the guide does not explain which flag produces exit 3")
	}

	// 3. The two output modes every command shares, and the truncation guard that only verify-audit needs.
	for _, needle := range []string{"-quiet", "-json", "-min-rows", "-require-complete", "-require-contiguous", "-require-trusted"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the guide does not mention %q", needle)
		}
	}

	// 4. The limits section is the reason the page exists; losing it would turn three honest tools into three
	// over-promising ones.
	if !strings.Contains(doc, "unkeyed") || !strings.Contains(doc, "prev_hash") {
		t.Fatal("the guide no longer states what the verifiers cannot prove")
	}
}
