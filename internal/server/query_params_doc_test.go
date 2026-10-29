package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestQueryParametersAreDocumented is the ratchet for parameters the spec never mentions.
//
// `TestDocumentedAPIRoutesAreInTheSpec` proves a PATH is documented; it cannot see a parameter added to an operation that already
// exists, which is exactly how `?format=csv` shipped undocumented twice in this batch. A client generator cannot offer a filter
// it has never heard of, and a reader of the spec cannot use one either.
//
// The list is shrink-only, like the envelope ratchets: a NEW undocumented parameter fails, and a baselined one that has since
// been documented must have its line deleted - so the count can only go down.
func TestQueryParametersAreDocumented(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("the API document is missing: %v", err)
	}
	documented := map[string]bool{}
	for _, match := range regexp.MustCompile(`- name: ([a-zA-Z0-9_]+)`).FindAllStringSubmatch(string(spec), -1) {
		documented[match[1]] = true
	}
	if len(documented) < 40 {
		t.Fatalf("only %d documented parameters found; this guard is not reading the spec", len(documented))
	}

	inCode := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	queryPattern := regexp.MustCompile(`c\.(?:Default)?Query\("([a-zA-Z0-9_]+)"`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, readErr := os.ReadFile(entry.Name())
		if readErr != nil {
			continue
		}
		for _, match := range queryPattern.FindAllStringSubmatch(string(raw), -1) {
			inCode[match[1]] = true
		}
	}
	if len(inCode) < 20 {
		t.Fatalf("only %d query parameters found in the handlers; this guard is not reading the sources", len(inCode))
	}

	raw, err := os.ReadFile("query_params_baseline.txt")
	if err != nil {
		t.Fatalf("the baseline is missing: %v", err)
	}
	baseline := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		baseline[line] = true
	}

	// 1. A parameter that is neither documented nor baselined is a new silent one.
	var added []string
	for name := range inCode {
		if documented[name] || baseline[name] {
			continue
		}
		added = append(added, name)
	}
	if len(added) > 0 {
		sort.Strings(added)
		t.Fatalf("these query parameters are read by a handler and documented nowhere: %s\n"+
			"document them in api/openapi.yaml, or add them to query_params_baseline.txt with the reason they are not part of the API",
			strings.Join(added, ", "))
	}

	// 2. A baselined parameter that is now documented (or no longer read) must have its line removed, so the ratchet only tightens.
	var stale []string
	for name := range baseline {
		if documented[name] || !inCode[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("these baseline entries are now documented or no longer read; delete their lines: %s", strings.Join(stale, ", "))
	}
}
