package server

import (
	"regexp"
	"strings"
)

// Normalising an error message so two hosts reporting one problem produce one signature.
//
// Digits, hex runs and paths are collapsed before grouping: "open /tmp/a/file.txt: denied" and "open /tmp/b/file.txt:
// denied" are the same failure, and a report that lists them separately helps nobody.
//
// These patterns are raw strings, so they contain SINGLE backslashes: `\s` is the whitespace class and `\\` is a literal
// backslash. Writing them through a shell that eats backslashes turns `\s` into "backslash, then s", which silently makes
// the class "not backslash and not the letter s" - it matches the paths in a test and mangles real messages. The unit test
// below exists to keep that from happening again.

var (
	// Windows drive paths, POSIX paths.
	failurePathPattern = regexp.MustCompile(`[a-z]:\\[^\s:]*|/(?:[^\s/:]+/)+[^\s/:]*`)
	// Hex identifiers: 0x…, and bare runs long enough not to be a word.
	failureHexPattern = regexp.MustCompile(`\b0x[0-9a-f]+\b|\b[0-9a-f]{8,}\b`)
	// Any remaining digits: pids, ports, error numbers.
	failureDigitPattern = regexp.MustCompile(`\d+`)
)

// The limits: a signature is a lead, not an inventory.
const (
	failureSignatureLimit    = 5
	failureSignatureMaxChars = 120
)

// normaliseFailureText collapses the parts of an error that differ between hosts but not between problems.
func normaliseFailureText(text string) string {
	lowered := strings.ToLower(strings.TrimSpace(text))
	lowered = failurePathPattern.ReplaceAllString(lowered, "<path>")
	lowered = failureHexPattern.ReplaceAllString(lowered, "<hex>")
	lowered = failureDigitPattern.ReplaceAllString(lowered, "<n>")
	// Whitespace runs are an artefact of removing the identifiers.
	lowered = strings.Join(strings.Fields(lowered), " ")
	return truncateString(lowered, failureSignatureMaxChars)
}
