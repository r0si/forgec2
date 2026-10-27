package obfuscation

import (
	"regexp"
	"strings"
	"testing"
)

// TestVarRenameLeavesSingleQuotedLiteralsAlone pins a semantic bug: the rename transform
// matched "$name" anywhere, including inside PowerShell SINGLE-quoted strings, where the
// variable is not expanded. A script that printed 'costs $price' came out as
// 'costs $AbCdEf' - valid PowerShell that does something different.
func TestVarRenameLeavesSingleQuotedLiteralsAlone(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 11, SeedSet: true})
	code := `$price = 5
Write-Output 'costs $price'
Write-Output "costs $price"`

	out := renameVariablesOutsideSingleQuotes(code, r)

	// The literal survives byte for byte.
	if !strings.Contains(out, "'costs $price'") {
		t.Fatalf("a single-quoted literal was rewritten:\n%s", out)
	}
	// The variable in real code was renamed, consistently.
	if strings.Contains(out, "$price") {
		// "$price" may only survive inside the single-quoted literal.
		if strings.Count(out, "$price") != 1 {
			t.Fatalf("variable rename was incomplete:\n%s", out)
		}
	}
	// The double-quoted string must follow the rename, because PowerShell expands there.
	if strings.Contains(out, `"costs $price"`) {
		t.Fatalf("a double-quoted interpolation was not renamed:\n%s", out)
	}
}

// TestVarRenameHandlesEscapedQuotes keeps the linear scanner honest about ” escapes:
// a literal written as 'it”s $x' still ends at the right place, so the code AFTER it is
// renamed again.
func TestVarRenameHandlesEscapedQuotes(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 12, SeedSet: true})
	code := `$a = 1; Write-Output 'it''s $a'; Write-Output $a`

	out := renameVariablesOutsideSingleQuotes(code, r)
	if !strings.Contains(out, "'it''s $a'") {
		t.Fatalf("the escaped-quote literal was rewritten:\n%s", out)
	}
	if strings.Count(out, "$a") != 1 {
		t.Fatalf("the trailing variable was not renamed:\n%s", out)
	}
}

// TestVarRenamedConsistentlyAcrossSegments pins that one variable keeps one new name even
// though the scanner processes the script in pieces.
func TestVarRenamedConsistentlyAcrossSegments(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 13, SeedSet: true})
	code := `$count = 1; $x = 'sep'; Write-Output $count; Write-Output $count`

	out := renameVariablesOutsideSingleQuotes(code, r)
	// Extract the two uses of the renamed variable: they must be identical.
	uses := regexp.MustCompile(`Write-Output\s+(\$?[A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(out, -1)
	if len(uses) != 2 {
		t.Fatalf("expected two Write-Output statements, got %d:\n%s", len(uses), out)
	}
	if uses[0][1] != uses[1][1] {
		t.Fatalf("the same variable got two names (%q vs %q):\n%s", uses[0][1], uses[1][1], out)
	}
	if strings.Contains(out, "$count") {
		t.Fatalf("the variable was not renamed:\n%s", out)
	}
}

// TestAutomaticVariablesAreNeverRenamed keeps the semantics-critical guard: $true, $_, $PSVersionTable
// and friends are language values, not user variables.
func TestAutomaticVariablesAreNeverRenamed(t *testing.T) {
	r := newRand(PSObfuscateOptions{Seed: 14, SeedSet: true})
	code := `$x = $true; if ($x) { Write-Output $PSVersionTable } ; Write-Output $env:PATH`

	out := renameVariablesOutsideSingleQuotes(code, r)
	for _, keep := range []string{"$true", "$PSVersionTable", "$env:PATH"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("%s was renamed:\n%s", keep, out)
		}
	}
	if strings.Contains(out, "$x") {
		t.Fatalf("a user variable was left unrenamed:\n%s", out)
	}
}
