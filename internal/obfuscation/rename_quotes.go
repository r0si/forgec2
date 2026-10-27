package obfuscation

import "strings"

// renameVariablesOutsideSingleQuotes is renameVariables with one correction: it does not
// touch the contents of a SINGLE-quoted PowerShell string.
//
// The plain regex matched "$name" anywhere, including inside 'single quotes' where
// PowerShell does NOT expand variables - so a script that printed 'costs $price' had its
// literal text rewritten to 'costs $AbCdEf' and behaved differently. Double-quoted strings
// are still rewritten on purpose: PowerShell expands $vars there, so the rename must follow
// them or the script breaks the other way.
//
// A full PowerShell tokenizer is not needed for this: single-quoted strings cannot span a
// newline and escape an embedded quote by doubling it, so one linear scan is exact.
func renameVariablesOutsideSingleQuotes(code string, r *jitterRand) string {
	if !strings.Contains(code, "'") {
		return renameVariables(code, r)
	}
	mapping := map[string]string{}
	var b strings.Builder
	b.Grow(len(code))

	for i := 0; i < len(code); {
		if code[i] != '\'' {
			// Plain segment: apply the variable rename to everything up to the next quote.
			next := strings.IndexByte(code[i:], '\'')
			if next < 0 {
				b.WriteString(renameSegment(code[i:], r, mapping))
				break
			}
			b.WriteString(renameSegment(code[i:i+next], r, mapping))
			i += next
			continue
		}
		// Single-quoted string: copy verbatim, honouring the '' escape.
		start := i
		i++
		for i < len(code) {
			if code[i] == '\'' {
				if i+1 < len(code) && code[i+1] == '\'' {
					i += 2
					continue
				}
				i++
				break
			}
			i++
		}
		b.WriteString(code[start:i])
	}
	return b.String()
}

// renameSegment applies the variable rename to one unquoted run of code, sharing the
// mapping so a variable keeps the same new name across the whole script.
func renameSegment(seg string, r *jitterRand, mapping map[string]string) string {
	return varRegex.ReplaceAllStringFunc(seg, func(m string) string {
		name := m[1:]
		lower := strings.ToLower(name)
		if strings.HasSuffix(m, ":") || automaticVariables[lower] {
			return m
		}
		if strings.Contains(name, ":") || strings.Contains(name, "{") {
			return m
		}
		if v, ok := mapping[lower]; ok {
			return v
		}
		v := randName(r, 6+r.intn(4))
		mapping[lower] = v
		return v
	})
}
