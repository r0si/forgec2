package server

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// errorEnvelopeBaselineFile holds the per-file count of raw (non-envelope)
// error JSON responses. The success envelope has had a ratchet for a while;
// this is the mirror for failures, so a new handler cannot answer
// c.JSON(4xx/5xx, gin.H{"error": ...}) without the envelope and leave the
// frontend reading a body shape the API contract does not promise.
//
// Format: "<file> <count>" per line, plus "#" comments.
const errorEnvelopeBaselineFile = "envelope_error_baseline.txt"

var rawErrorJSONMarker = "c.JSON(http.Status"

// countRawErrorResponses counts c.JSON(...) calls whose status argument is a
// 4xx/5xx constant. Success responses are the other test's job.
func countRawErrorResponses(src string) int {
	n := 0
	for _, line := range strings.Split(src, "\n") {
		idx := strings.Index(line, rawErrorJSONMarker)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(rawErrorJSONMarker):]
		if !strings.HasPrefix(rest, "BadRequest") &&
			!strings.HasPrefix(rest, "Unauthorized") &&
			!strings.HasPrefix(rest, "Forbidden") &&
			!strings.HasPrefix(rest, "NotFound") &&
			!strings.HasPrefix(rest, "Conflict") &&
			!strings.HasPrefix(rest, "InternalServerError") &&
			!strings.HasPrefix(rest, "TooManyRequests") &&
			!strings.HasPrefix(rest, "ServiceUnavailable") &&
			!strings.HasPrefix(rest, "RequestEntityTooLarge") {
			continue
		}
		n++
	}
	return n
}

// TestErrorEnvelope ratchets raw error responses: each file may keep what the
// baseline records, but a new one fails, and a file that improves must lower
// its number in the same commit.
func TestErrorEnvelope(t *testing.T) {
	baseline := map[string]int{}
	raw, err := os.ReadFile(errorEnvelopeBaselineFile)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed baseline line %q", line)
		}
		n, convErr := strconv.Atoi(fields[1])
		if convErr != nil {
			t.Fatalf("bad count in %q: %v", line, convErr)
		}
		baseline[fields[0]] = n
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(files)

	problems := []string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatalf("read %s: %v", f, readErr)
		}
		got := countRawErrorResponses(string(src))
		want := baseline[f]
		switch {
		case got > want:
			problems = append(problems, f+": "+strconv.Itoa(got)+" raw error responses, baseline "+strconv.Itoa(want))
		case got < want:
			problems = append(problems, f+": "+strconv.Itoa(got)+" raw error responses, baseline still says "+strconv.Itoa(want)+" — lower it in this commit")
		}
	}
	for f := range baseline {
		if _, statErr := os.Stat(f); statErr != nil {
			problems = append(problems, f+": listed in the baseline but no longer exists — remove the line")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("error-envelope ratchet:\n  %s", strings.Join(problems, "\n  "))
	}
}
