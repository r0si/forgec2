package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		if hits := countVariableSuccessEnvelopes(text); len(hits) > 0 {
			t.Fatalf("%s builds the envelope into a variable (%s) and passes it to respond(), "+
				"nesting a second {\"success\":true,…} inside data; drop the \"success\" key from "+
				"that payload — no consumer reads it, and the outer envelope already carries it",
				path, strings.Join(hits, ", "))
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

// assignRe captures the variable a map literal is bound to, from the text
// leading up to the opening brace on the same line: `resp := gin.H{`, `out =
// gin.H{`, or a `var` form handled by the caller.
// Handles: `var x = gin.H{`, `x := gin.H{`, `x = gin.H{`, with optional indentation.
var assignRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*(?::=|=)\s*gin\.H\{`)

// firstKeyIs reports whether the first key of the gin.H literal whose opening
// brace has already been consumed is the given quoted key, in either the
// inline form or the multi-line form the envelope migration left behind.
func firstKeyIs(rest, key string) bool {
	if strings.HasPrefix(strings.TrimLeft(rest, " \t"), key) {
		return true
	}
	firstNL := strings.IndexByte(rest, '\n')
	if firstNL < 0 {
		return false
	}
	if strings.TrimSpace(rest[:firstNL]) != "" {
		return false
	}
	afterNL := rest[firstNL+1:]
	nextNL := strings.IndexByte(afterNL, '\n')
	if nextNL < 0 {
		nextNL = len(afterNL)
	}
	return strings.HasPrefix(strings.TrimSpace(afterNL[:nextNL]), key)
}

// envelopeVarNames returns the variables assigned a gin.H literal whose FIRST
// key is "success" — the shape of the envelope that respond() emits.
func envelopeVarNames(text string) map[string]bool {
	const open = "gin.H{"
	out := map[string]bool{}
	for i := 0; i < len(text); {
		idx := strings.Index(text[i:], open)
		if idx < 0 {
			break
		}
		pos := i + idx + len(open)
		i = pos
		if !firstKeyIs(text[pos:], `"success":`) {
			continue
		}
		// Find end of the current line
		lineEnd := strings.IndexByte(text[i:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text) - i
		}
		// The variable is assigned on the same line as gin.H{.
		// Get the FULL line containing the gin.H{ assignment.
		lineStart := strings.LastIndexByte(text[:i], '\n') + 1
		line := text[lineStart : i+lineEnd]
		if m := assignRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// countVariableSuccessEnvelopes finds the double wraps the literal marker
// cannot see.
//
// The nestedMarker above matches respond(c, gin.H{"success"…). Three endpoints
// shipped the identical defect written a different way: the payload was built
// into a named variable first (resp := gin.H{"success": true, ...}) and that
// variable was passed to respond(). The marker is a string comparison against
// the call site, so it saw nothing, TestSuccessEnvelope stayed green, and the
// clients received {"success":true,"data":{"success":true,…}} with every field
// one level deeper than the contract — which is also what silently hollowed
// TestHandleKillSwitchStatus. A ratchet that only checks the spelling the last
// bug happened to use is not a ratchet.
//
// "First key" is the discriminator, mirroring countFirstKeyDataWraps: a map
// whose first key is "success" is envelope-shaped, while a payload that merely
// has a success field among its data (gin.H{"task": t, "success": ok}) is
// legitimate and stays invisible to this gate.
func countVariableSuccessEnvelopes(text string) []string {
	vars := envelopeVarNames(text)
	if len(vars) == 0 {
		return nil
	}
	var hits []string
	for name := range vars {
		if strings.Contains(text, "respond(c, "+name+")") {
			hits = append(hits, name)
		}
	}
	sort.Strings(hits)
	return hits
}

// countFirstKeyDataWraps counts respond(c, gin.H{ whose FIRST key is "data",
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

		// Skip comment lines
		rest := text[i:]
		if strings.HasPrefix(strings.TrimLeft(rest, " \t"), "//") {
			// Skip to end of line
			nl := strings.IndexByte(rest, '\n')
			if nl >= 0 {
				i += nl + 1
			} else {
				i = len(text)
			}
			continue
		}

		if strings.HasPrefix(strings.TrimLeft(rest, " \t"), `"data":`) {
			count++
			continue
		}
		// Also handle multi-line:
		firstNL := strings.IndexByte(rest, '\n')
		if firstNL >= 0 {
			if strings.TrimSpace(rest[:firstNL]) == "" {
				if strings.HasPrefix(strings.TrimLeft(rest[firstNL+1:], " \t"), `"data":`) {
					count++
				}
			}
		}
		i++
	}
	return count
}

// readEnvelopeBaseline parses "<file.go> <count>" lines, ignoring blanks and
// '#' comments.
func readEnvelopeBaseline(path string) (map[string]int, error) {
	fmt.Printf("DEBUG: readEnvelopeBaseline called with path=%q\n", path)
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("DEBUG: readEnvelopeBaseline error: %v\n", err)
		return nil, err
	}
	base := make(map[string]int)
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: bad baseline line %q", path, i+1, line)
		}
		n, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("bad baseline value in %q", line)
		}
		base[fields[0]] = n
	}
	return base, nil
}
