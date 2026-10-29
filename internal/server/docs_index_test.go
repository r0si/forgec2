package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocsIndexListsEveryPage keeps docs/README.md complete in both directions.
//
// A documentation directory grows by accretion: each page is written when it is needed, and the index is only written
// once. The result is that the newest pages - the ones nobody has found yet - are exactly the ones missing from it. This
// test makes that failure loud at the moment a page is added, and equally loud when the index names a file that was
// deleted or renamed.
func TestDocsIndexListsEveryPage(t *testing.T) {
	docsDir := "../../docs"
	index, err := os.ReadFile(filepath.Join(docsDir, "README.md"))
	if err != nil {
		t.Fatalf("docs/README.md is missing: %v", err)
	}
	indexText := string(index)

	entries, err := os.ReadDir(docsDir)
	if err != nil {
		t.Fatalf("read docs: %v", err)
	}

	pages := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if entry.Name() == "README.md" {
			continue
		}
		pages[entry.Name()] = true
		if !strings.Contains(indexText, "("+entry.Name()+")") {
			t.Fatalf("docs/%s exists but docs/README.md does not link to it", entry.Name())
		}
	}

	// The index is grouped by what the reader is trying to do, not alphabetically. That grouping is the reason a person can
	// find the page at all, and a flat rewrite would still pass the link checks above - so it is pinned here.
	for _, section := range []string{
		"## Using the console",
		"## Running it",
		"## Handing evidence to someone else",
		"## Extending it",
		"## The console frontend",
	} {
		if !strings.Contains(indexText, section) {
			t.Fatalf("docs/README.md lost the %q section; the index is organised by reader intent", strings.TrimPrefix(section, "## "))
		}
	}

	// The terminology page is the one place the overloaded words are disambiguated, and four of them are called "signature".
	// A review that confuses the audit chain with a bundle signature reaches the wrong conclusion about what was proven.
	terminology, err := os.ReadFile("../../docs/TERMINOLOGY.md")
	if err != nil {
		t.Fatalf("the terminology page is missing: %v", err)
	}
	for _, sense := range []string{"signature** (audit)", "signature** (bundle)", "signature** (plugin)", "signature** (failure)"} {
		if !strings.Contains(string(terminology), sense) {
			t.Fatalf("docs/TERMINOLOGY.md no longer separates %q from the other senses", sense)
		}
	}

	// The other direction: an index entry pointing at a page that no longer exists sends a reader to a 404.
	for _, line := range strings.Split(indexText, "\n") {
		start := strings.Index(line, "](")
		if start < 0 {
			continue
		}
		rest := line[start+2:]
		end := strings.Index(rest, ")")
		if end < 0 {
			continue
		}
		target := rest[:end]
		if !strings.HasSuffix(target, ".md") || strings.Contains(target, "/") {
			continue // external links and anchors are not this test's business
		}
		if target == "README.md" {
			continue
		}
		if !pages[target] {
			t.Fatalf("docs/README.md links to %q, which does not exist", target)
		}
	}

	// A directory with pages but an empty index would pass the loop above vacuously.
	if len(pages) < 10 {
		t.Fatalf("only %d documentation page(s) found; the index test is not looking at the right directory", len(pages))
	}
}
