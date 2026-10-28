package main

import (
	"testing"

	"github.com/forgec2/forgec2/internal/auditchain"
)

// TestExitCode covers the decision a pipeline makes. The interesting case is the third one: rows that all check out,
// but fewer of them than the caller demanded - a truncated download must not read as "verified".
func TestExitCode(t *testing.T) {
	good := auditchain.Verdict{OK: true, Checked: 3}
	bad := auditchain.Verdict{OK: false, Checked: 3, FirstBadID: 2}

	cases := []struct {
		name    string
		verdict auditchain.Verdict
		rows    int
		minRows int
		want    int
	}{
		{"consistent and enough rows", good, 3, 1, 0},
		{"consistent but shorter than required", good, 3, 10, 1},
		{"inconsistent", bad, 3, 1, 1},
		{"inconsistent and short", bad, 3, 10, 1},
		{"exactly at the floor", good, 5, 5, 0},
	}
	for _, test := range cases {
		if got := exitCode(test.verdict, test.rows, test.minRows); got != test.want {
			t.Fatalf("%s: exit %d, want %d", test.name, got, test.want)
		}
	}
}

// TestParseAcceptsBothExportShapes keeps the two shapes the server can produce working, because a third party is told
// they may hold either.
func TestParseAcceptsBothExportShapes(t *testing.T) {
	row := `{"id":1,"user":"a","action":"login","entry_hash":"h1","prev_hash":""}`

	batched, batchMeta, err := parse([]byte(row + "\n" + `{"id":2,"user":"b","action":"login","entry_hash":"h2","prev_hash":"h1"}`))
	if err != nil {
		t.Fatalf("ndjson: %v", err)
	}
	// NDJSON keeps filtered/truncated in response headers, which a file does not have: the verifier must know it does
	// not know.
	if batchMeta.Known || batchMeta.Format != "ndjson" {
		t.Fatalf("ndjson metadata = %+v, want format ndjson and selection unknown", batchMeta)
	}
	if len(batched) != 2 || batched[0].ID != 1 || batched[1].ID != 2 {
		t.Fatalf("ndjson rows = %+v", batched)
	}

	document, docMeta, err := parse([]byte(`{"count":1,"filtered":true,"truncated":true,"format":"json","rows":[` + row + `]}`))
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	if !docMeta.Known || !docMeta.Filtered || !docMeta.Truncated || docMeta.Format != "json" {
		t.Fatalf("document metadata = %+v, want the file's own admission carried through", docMeta)
	}
	if len(document) != 1 || document[0].User != "a" {
		t.Fatalf("document rows = %+v", document)
	}

	// A single row is also valid JSON on its own: it must not be mistaken for a document with no rows.
	single, _, err := parse([]byte(row))
	if err != nil {
		t.Fatalf("single row: %v", err)
	}
	if len(single) != 1 || single[0].ID != 1 {
		t.Fatalf("single row = %+v", single)
	}

	if _, _, err := parse([]byte("not json")); err == nil {
		t.Fatal("a non-JSON file was accepted")
	}
}
