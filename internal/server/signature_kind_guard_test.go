package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSignatureFieldsCarryTheirKind is the enumeration guard for the product's overloaded word.
//
// Four different objects here are called a "signature", and the expensive mistake is taking the unkeyed audit chain for a keyed
// one. A response that reports a signature without saying which kind leaves that inference to the reader, so this guard walks
// the Go sources and requires every FILE that declares a `json:"signature…"` field to also declare a kind - with an explicit
// allowlist for the places where the field is the thing itself rather than a verdict.
func TestSignatureFieldsCarryTheirKind(t *testing.T) {
	// Deliberate exceptions, each with the reason it is not a verdict:
	//   - the manifest and the implant wire struct CARRY the signature; they do not report on one;
	//   - a request body is input, so the caller already knows what they sent;
	//   - the test files assert on these fields rather than exposing them.
	allowed := map[string]string{
		"internal/bundle/bundle.go":                   "the manifest carries the signature object it is about",
		"internal/plugin/manifest.go":                 "the plugin manifest carries its own signature field",
		"internal/payload/agent/agent_lifecycle.go":   "implant wire struct, not an operator response",
		"internal/server/tasks_rerun_by_signature.go": "request body: the caller supplies the shape",
	}

	roots := []string{"../../internal", "../../cmd"}
	var offenders []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			text := string(raw)
			if !strings.Contains(text, `json:"signature`) {
				return nil
			}
			// The kind may be a struct tag (the verdict types) or a map key (the gin.H responses), and it may be called
			// signature_kind or kind - the bundle report nests its verdict under a `kind` key.
			if strings.Contains(text, `signature_kind"`) || strings.Contains(text, `"kind"`) {
				return nil
			}
			slashed := filepath.ToSlash(path)
			for prefix := range allowed {
				if strings.HasSuffix(slashed, prefix) {
					return nil
				}
			}
			offenders = append(offenders, slashed)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("these files report a signature without saying which kind: %s\n"+
			"add a signature_kind field, or add the file to the allowlist with the reason it is not a verdict",
			strings.Join(offenders, ", "))
	}

	// The guard must also keep working: at least one file per sense is found, or the pattern stopped matching.
	found := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr == nil && strings.Contains(string(raw), `signature_kind"`) {
				found++
			}
			return nil
		})
	}
	if found < 3 {
		t.Fatalf("only %d files carry a signature_kind; the guard is not looking at the sources", found)
	}
}
