package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// TestReloadTokensMatchBetweenDiffAndTable pins the pairing that has already drifted
// twice: diffConfig decides WHICH changes are reported, reloadGroups decides what
// happens to each reported token. A token on the diff side only reaches the runtime
// as "unknown token (diffConfig/table drift)", and a token on the table side only can
// never fire - both mean an operator edits a value and nothing happens.
//
// The two live in different files, so the sets are compared by reading the source.
// That is deliberate: a static check is the only thing that can see both lists at
// once, and this is the same technique the repo's Node gates use.
func TestReloadTokensMatchBetweenDiffAndTable(t *testing.T) {
	diffSrc := readTestSource(t, "config_reloader.go")
	tableSrc := readTestSource(t, "config_sync.go")

	// Tokens appended by diffConfig: append(hotReloadable, "x") / append(staticOnly, "x").
	diffRe := regexp.MustCompile(`(?:hotReloadable|staticOnly)\s*=\s*append\((?:hotReloadable|staticOnly),\s*"([a-z0-9_.]+)"\)`)
	// Tokens declared in reloadGroups: {Token: "x", Mode: ...}.
	tableRe := regexp.MustCompile(`\{Token:\s*"([a-z0-9_.]+)",\s*Mode:`)

	diffTokens := map[string]bool{}
	for _, m := range diffRe.FindAllStringSubmatch(diffSrc, -1) {
		diffTokens[m[1]] = true
	}
	tableTokens := map[string]bool{}
	for _, m := range tableRe.FindAllStringSubmatch(tableSrc, -1) {
		tableTokens[m[1]] = true
	}
	if len(diffTokens) == 0 || len(tableTokens) == 0 {
		t.Fatalf("parsed %d diff tokens and %d table tokens; the patterns in this test no longer match the code",
			len(diffTokens), len(tableTokens))
	}

	var missingFromTable, missingFromDiff []string
	for tok := range diffTokens {
		if !tableTokens[tok] {
			missingFromTable = append(missingFromTable, tok)
		}
	}
	for tok := range tableTokens {
		if !diffTokens[tok] {
			missingFromDiff = append(missingFromDiff, tok)
		}
	}
	sort.Strings(missingFromTable)
	sort.Strings(missingFromDiff)
	if len(missingFromTable) > 0 {
		t.Errorf("diffConfig reports tokens the reload table cannot handle (runtime: unknown token): %v", missingFromTable)
	}
	if len(missingFromDiff) > 0 {
		t.Errorf("the reload table declares tokens no diff can ever produce: %v", missingFromDiff)
	}
}

// TestDiffConfigHasNoDuplicateTokens keeps the "N fields changed" count honest: a
// condition written twice reported the same field twice, inflating the number that
// reaches the log and the audit trail.
func TestDiffConfigHasNoDuplicateTokens(t *testing.T) {
	src := readTestSource(t, "config_reloader.go")
	re := regexp.MustCompile(`(?:hotReloadable|staticOnly)\s*=\s*append\((?:hotReloadable|staticOnly),\s*"([a-z0-9_.]+)"\)`)
	seen := map[string]int{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[1]]++
	}
	for tok, count := range seen {
		if count > 1 {
			t.Errorf("diffConfig appends %q %d times", tok, count)
		}
	}
}

func readTestSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
