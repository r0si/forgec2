package obfuscation

import (
	"strings"
	"testing"
)

// TestScrubTriggersKeepsThePayloadParseable pins the fix for a transform that broke its own
// output. The previous implementation was a global strings.ReplaceAll, so:
//
//	Invoke-Mimikatz         -> Invoke-('mimi'+'katz')          // not a command name
//	Write-Output 'mimikatz' -> Write-Output '('mimi'+'katz')' // unterminated string
//
// Both are parse errors: the payload does not run at all, which is worse than leaving the
// token recognizable.
func TestScrubTriggersKeepsThePayloadParseable(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 21, SeedSet: true})

	// A token inside a longer command name must be left alone.
	cmd := `Invoke-Mimikatz -Command "sekurlsa::logonpasswords"`
	out := scrubTriggersSafe(cmd, r)
	if out != cmd {
		t.Fatalf("a command name was rewritten into a parse error:\n in: %s\nout: %s", cmd, out)
	}

	// A token inside a quoted string must be left alone (its value is data).
	for _, quoted := range []string{
		`Write-Output 'mimikatz'`,
		`$s = "the amsi bypass"`,
		`$t = 'it''s mimikatz'`,
	} {
		if got := scrubTriggersSafe(quoted, r); got != quoted {
			t.Fatalf("a quoted literal was rewritten:\n in: %s\nout: %s", quoted, got)
		}
	}
}

// TestScrubTriggersSplitsStandaloneTokens keeps the evasion value: a token that stands on
// its own is still broken up.
func TestScrubTriggersSplitsStandaloneTokens(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 22, SeedSet: true})
	out := scrubTriggersSafe(`Write-Output mimikatz`, r)
	if strings.Contains(out, "mimikatz") {
		t.Fatalf("a standalone token was not split: %s", out)
	}
	if !strings.Contains(out, "'") || !strings.Contains(out, "+") {
		t.Fatalf("the split does not look like a concatenation: %s", out)
	}
	// The concatenation must re-assemble to the original token.
	if concatLiteral(t, out) != "mimikatz" {
		t.Fatalf("the split changed the value: %s", out)
	}
}

// TestScrubTriggersDoesNotDoubleWrapAlreadySplitTokens guards the interaction with
// splitStrings: a token that is already a concatenation must not be wrapped again.
func TestScrubTriggersDoesNotDoubleWrapAlreadySplitTokens(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 23, SeedSet: true})
	already := `Write-Output ('mimi'+'katz')`
	out := scrubTriggersSafe(already, r)
	if strings.Count(out, "('") != 1 {
		t.Fatalf("an already-split token was wrapped again: %s", out)
	}
}

// concatLiteral extracts the string literals from a `('a'+'b')` expression and joins them.
func concatLiteral(t *testing.T, expr string) string {
	t.Helper()
	start := strings.Index(expr, "(")
	if start < 0 {
		t.Fatalf("no concatenation found in %q", expr)
	}
	body := expr[start:]
	var b strings.Builder
	i := 0
	for i < len(body) {
		if body[i] == '\'' {
			end := scanQuoted(body, i, '\'')
			frag := body[i+1 : end-1]
			b.WriteString(strings.ReplaceAll(frag, "''", "'"))
			i = end
			continue
		}
		i++
	}
	return b.String()
}
