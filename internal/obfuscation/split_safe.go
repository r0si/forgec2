package obfuscation

import "strings"

// splitStringsSafe is splitStrings with two corrections.
//
//  1. It never rewrites a single-quoted run that sits INSIDE a double-quoted string.
//     The regex matched the inner quotes anyway, so "say 'hi' now" became "say ('h'+'i') now"
//     - still valid PowerShell, but a different string value. That is a payload that does
//     something else.
//  2. It keeps the value exactly: the fragments are derived from the literal's bytes, and the
//     concatenation is what the test asserts on.
//
// The skips from the original are kept: URLs, paths (backslashes) and format strings stay
// intact, because splitting them can break a consumer that inspects the literal.
func splitStringsSafe(code string, r *jitterRand) string {
	if !strings.Contains(code, "'") {
		return code
	}
	var b strings.Builder
	b.Grow(len(code))
	i := 0
	for i < len(code) {
		switch code[i] {
		case '"':
			// A double-quoted string may contain single quotes as literal text: copy it whole.
			end := scanQuoted(code, i, '"')
			b.WriteString(code[i:end])
			i = end
		case '\'':
			end := scanQuoted(code, i, '\'')
			literal := code[i:end]
			b.WriteString(maybeSplitLiteral(literal, r))
			i = end
		default:
			next := strings.IndexAny(code[i:], "'\"")
			if next < 0 {
				b.WriteString(code[i:])
				i = len(code)
			} else {
				b.WriteString(code[i : i+next])
				i += next
			}
		}
	}
	return b.String()
}

// maybeSplitLiteral applies the original rules to one complete single-quoted literal.
func maybeSplitLiteral(literal string, r *jitterRand) string {
	if len(literal) < 4 || len(literal) > 34 { // two content chars plus both quotes
		return literal
	}
	inner := literal[1 : len(literal)-1]
	if strings.Contains(inner, "\\") || strings.Contains(inner, "://") ||
		strings.Contains(inner, "{0}") || strings.Contains(inner, "%s") || !r.coin(60) {
		return literal
	}
	parts := make([]string, 0, len(inner))
	for _, ch := range inner {
		s := string(ch)
		if ch == '\'' {
			s = "''"
		}
		parts = append(parts, "'"+s+"'")
	}
	return "(" + strings.Join(parts, "+") + ")"
}
