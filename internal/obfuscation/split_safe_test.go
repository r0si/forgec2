package obfuscation

import (
	"strings"
	"testing"
)

// TestSplitStringsPreservesValue pins the property the transform exists to preserve: the
// fragments must re-assemble to the original literal. Splitting is only useful if the
// payload still means the same thing.
func TestSplitStringsPreservesValue(t *testing.T) {
	literals := []string{
		"'hello world'",
		"'AMSI'",
		"'sekurlsa::logonpasswords'",
		"'a b c'",
		"'ünïcode'",
		"'12345'",
	}
	for _, literal := range literals {
		sawSplit := false
		for seed := 1; seed <= 40; seed++ {
			r := newRand(PSObfuscateOptions{Seed: int64(seed), SeedSet: true})
			out := splitStringsSafe(literal, r)
			if out == literal {
				continue
			}
			sawSplit = true
			if got := concatLiteral(t, out); got != strings.Trim(literal, "'") {
				t.Fatalf("%s split into %s, which reassembles to %q", literal, out, got)
			}
			if !strings.HasPrefix(out, "(") || !strings.HasSuffix(out, ")") {
				t.Fatalf("%s split into %s, which is not a parenthesized expression", literal, out)
			}
		}
		if !sawSplit {
			t.Fatalf("%s was never split across 40 seeds; the property test checked nothing", literal)
		}
	}
}

// TestSplitStringsLeavesLiteralsInsideDoubleQuotesAlone pins the bug this rewrite fixed:
// the regex matched the inner quotes of a double-quoted string, so "say 'hi' now" became
// "say ('h'+'i') now" - valid PowerShell, different value.
func TestSplitStringsLeavesLiteralsInsideDoubleQuotesAlone(t *testing.T) {
	cases := []string{
		`$s = "say 'hi' now"`,
		`$s = "nested 'a b' pair"`,
		`Write-Output "it 'is' fine"`,
	}
	for _, code := range cases {
		for seed := 1; seed <= 20; seed++ {
			r := newRand(PSObfuscateOptions{Seed: int64(seed), SeedSet: true})
			if got := splitStringsSafe(code, r); got != code {
				t.Fatalf("a literal inside double quotes was rewritten at seed %d:\n in: %s\nout: %s", seed, code, got)
			}
		}
	}
}

// TestSplitStringsKeepsItsSkips keeps the original guard rails: URLs, paths and format
// strings are not split, because something downstream may inspect them.
func TestSplitStringsKeepsItsSkips(t *testing.T) {
	cases := []string{
		`$u = 'https://example.com/a'`,
		`$p = 'C:\Windows\System32'`,
		`$f = '{0} items'`,
		`$g = '%s items'`,
	}
	for _, code := range cases {
		for seed := 1; seed <= 20; seed++ {
			r := newRand(PSObfuscateOptions{Seed: int64(seed), SeedSet: true})
			if got := splitStringsSafe(code, r); got != code {
				t.Fatalf("a skipped literal was rewritten at seed %d:\n in: %s\nout: %s", seed, code, got)
			}
		}
	}
}

// TestSplitStringsLeavesShortAndLongLiteralsAlone covers the size window: one character is
// not worth splitting and the original regex capped at 32 content characters.
func TestSplitStringsLeavesShortAndLongLiteralsAlone(t *testing.T) {
	for _, literal := range []string{"'a'", "'" + strings.Repeat("x", 40) + "'"} {
		for seed := 1; seed <= 20; seed++ {
			r := newRand(PSObfuscateOptions{Seed: int64(seed), SeedSet: true})
			if got := splitStringsSafe(literal, r); got != literal {
				t.Fatalf("%s was rewritten: %s", literal, got)
			}
		}
	}
}
