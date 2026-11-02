package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestSuccessEnvelope is the ratchet for the Option-A response contract:
//
//	success -> {"success": true, "data": <payload>}   (respond)
//	failure -> {"success": false, "error": "<msg>"}   (respondError)
//
// Two things are enforced:
//
//  1. No handler may hand-build the envelope. respond() already wraps exactly
//     once, so `respond(c, gin.H{"success": ...})` would emit a nested
//     {"data": {"success": ...}} body.
//  2. The number of raw `c.JSON(http.StatusOK, ...)` sites per file may only
//     go down. The migration runs in batches; envelope_baseline.txt pins the
//     remainder so a new raw success cannot slip in, and a migrated file must
//     lower its number in the same commit (otherwise the list goes stale and
//     the gate stops meaning anything).
func TestSuccessEnvelope(t *testing.T) {
	const marker = "c.JSON(http.StatusOK"
	const nestedMarker = `respond(c, gin.H{"success"`
	// A bare "data" key is the same defect with a different spelling: respond
	// already emits the data envelope, so respond(c, gin.H{"data": ...}) sends
	// {"success":true,"data":{"data":...}} and every client reading
	// data.<field> sees undefined. That shipped in /api/me, where it silently
	// disabled permission-based navigation for read-only roles.
	//
	// The marker must follow the call across lines: the envelope migration
	// reformatted several sites as
	//
	//	respond(c, gin.H{
	//
	//		"data": ...,
	//	})
	//
	// and a one-line literal marker saw none of them.
	const nestedDataMarker = `respond(c, gin.H{"data"`
	// Three endpoints legitimately name a payload field "data": the artifact
	// and delivery builders return the base64 bytes under exactly that key,
	// and the console reads res.data. The ratchet freezes the count — a new
	// double-wrap fails, and removing one must lower the number.
	dataFieldAllowlist := map[string]int{
		"handlers_missing_api.go": 2,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(files)

	actual := make(map[string]int)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(src)
		if n := strings.Count(text, nestedMarker); n > 0 {
			t.Fatalf("%s hand-builds the success envelope (%d site(s)); call respond(c, payload) "+
				"with the bare payload instead — respond wraps it exactly once", path, n)
		}
		if strings.Contains(text, "func respondSuccess(") {
			t.Fatalf("%s re-declares respondSuccess; it was folded into respond", path)
		}
		if n := strings.Count(text, nestedDataMarker); n > 0 {
			t.Fatalf("%s nests a second data envelope (%d site(s)); pass the payload itself to "+
				"respond(c, payload) — respond already emits {\"success\":true,\"data\":...}", path, n)
		}
		// Count every respond(c, gin.H{ whose FIRST key is "data" }, whether
		// written inline or across lines, then subtract the allowlisted
		// byte-payload fields. Anything left is a double envelope.
		if n := countFirstKeyDataWraps(text); n != dataFieldAllowlist[path] {
			if n > dataFieldAllowlist[path] {
				t.Fatalf("%s has %d double data envelope(s), allowlist says %d; pass the payload itself "+
					"to respond(c, payload) — respond already emits {\"success\":true,\"data\":...}",
					path, n, dataFieldAllowlist[path])
			}
			t.Fatalf("%s improved (%d double-data site(s), allowlist says %d); lower the allowlist in this commit",
				path, n, dataFieldAllowlist[path])
		}
		if n := strings.Count(text, marker); n > 0 {
			actual[path] = n
		}
	}

	baseline, err := readEnvelopeBaseline("envelope_baseline.txt")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	var grew, shrank []string
	for path, n := range actual {
		want, ok := baseline[path]
		if !ok {
			grew = append(grew, fmt.Sprintf("%s: %d raw site(s), not in envelope_baseline.txt", path, n))
			continue
		}
		if n > want {
			grew = append(grew, fmt.Sprintf("%s: %d raw site(s), baseline says %d", path, n, want))
		}
		if n < want {
			shrank = append(shrank, fmt.Sprintf("%s: %d raw site(s), baseline says %d", path, n, want))
		}
	}
	for path, want := range baseline {
		if _, ok := actual[path]; !ok {
			shrank = append(shrank, fmt.Sprintf("%s: 0 raw site(s), baseline says %d", path, want))
		}
	}
	sort.Strings(grew)
	sort.Strings(shrank)

	if len(grew) > 0 {
		t.Fatalf("new raw JSON 200 responses (use respond(c, payload)):\n  %s", strings.Join(grew, "\n  "))
	}
	if len(shrank) > 0 {
		t.Fatalf("envelope_baseline.txt is stale — lower or remove these counts in the same commit:\n  %s",
			strings.Join(shrank, "\n  "))
	}
}

// countFirstKeyDataWraps counts respond(c, gin.H{ whose FIRST key is "data",
// in either the inline form or the multi-line form the envelope migration
// left behind:
//
//	respond(c, gin.H{"data": x})          → counted
//	respond(c, gin.H{                     → counted when the first non-blank
//	                                          body line starts with "data":
//	        "data": x,
//	})
//
// A "data" key that is NOT first (gin.H{"filename": ..., "data": bytes}) is a
// legitimate payload field, not a second envelope, so it is not counted.
func countFirstKeyDataWraps(text string) int {
	const open = "respond(c, gin.H{"
	count := 0
	for i := 0; i < len(text); {
		idx := strings.Index(text[i:], open)
		if idx < 0 {
			break
		}
		pos := i + idx + len(open)
		i = pos
		rest := text[pos:]
		if strings.HasPrefix(strings.TrimLeft(rest, " \t"), "\"data\":") {
			count++
			continue
		}
		// Multi-line form: the literal opened with nothing before the first
		// newline (possibly just a blank line), and the first key is "data".
		firstNL := strings.IndexByte(rest, '\n')
		if firstNL < 0 {
			continue
		}
		if strings.TrimSpace(rest[:firstNL]) != "" {
			continue
		}
		afterNL := rest[firstNL+1:]
		nextNL := strings.IndexByte(afterNL, '\n')
		if nextNL < 0 {
			nextNL = len(afterNL)
		}
		if strings.HasPrefix(strings.TrimSpace(afterNL[:nextNL]), "\"data\":") {
			count++
		}
	}
	return count
}

// readEnvelopeBaseline parses "<file.go> <count>" lines, ignoring blanks and
// '#' comments.
func readEnvelopeBaseline(path string) (map[string]int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int)
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want `<file> <count>`, got %q", path, i+1, line)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad count %q", path, i+1, fields[1])
		}
		out[fields[0]] = n
	}
	return out, nil
}
