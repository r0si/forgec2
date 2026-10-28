// Command verify-audit checks an exported audit log without a running server.
//
// The export endpoint has always said a third party can verify it offline; until now that required the console. This
// reads the file the export produced - newline-delimited JSON, or the single-document `format=json` shape - and walks
// the chain:
//
//	go run ./cmd/verify-audit audit.ndjson
//	go run ./cmd/verify-audit audit.json -json
//
// Exit codes: 0 every digest and link checks out, 1 a row was edited or a link was broken, 2 the file could not be read.
//
// What it proves is partial-edit detection: the hash is unkeyed and prev_hash is not an input, so a rewrite that
// recomputes every row verifies. That is a property of the design, not of this tool, and the report says it.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/forgec2/forgec2/internal/auditchain"
)

func main() {
	asJSON := flag.Bool("json", false, "print the verdict as JSON")
	quiet := flag.Bool("quiet", false, "print nothing; rely on the exit code")
	// A truncated download and a log with nothing in it both look like "no problems found" to a naive verifier, and
	// both are exactly what an attacker with write access would prefer. A floor makes that failure loud.
	minRows := flag.Int("min-rows", 1, "fail when the file holds fewer rows than this; guards against verifying a truncated export")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: verify-audit [-json] [-quiet] [-min-rows N] <audit.ndjson|audit.json>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	raw, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-audit: %v\n", err)
		os.Exit(2)
	}
	rows, meta, err := parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-audit: %v\n", err)
		os.Exit(2)
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "verify-audit: no audit rows in %s\n", flag.Arg(0))
		os.Exit(2)
	}

	verdict := auditchain.Verify(rows)
	verdict.SelectionKnown = meta.Known
	verdict.Filtered = meta.Filtered
	if *quiet {
		// nothing: the exit code is the answer
	} else if *asJSON {
		encoded, err := json.MarshalIndent(verdict, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify-audit: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(encoded))
	} else {
		printVerdict(verdict, meta)
	}

	os.Exit(exitCode(verdict, len(rows), *minRows))
}

// exitCode turns a verdict into the process result.
//
// Exit 1 means the chain does not check out OR the file holds fewer rows than the caller requires: a caller that asked
// for at least a thousand rows got a truncated download, and telling them "OK" because the rows that did arrive are
// consistent would be the wrong answer twice over. Exit 2 is reserved for "this file is not an audit export".
func exitCode(verdict auditchain.Verdict, rows, minRows int) int {
	if !verdict.OK || rows < minRows {
		return 1
	}
	return 0
}

// exportMeta is what the file itself says about the export.
//
// Only the single-document format carries it: the newline-delimited export puts `filtered`/`truncated` in RESPONSE
// HEADERS, and a file loses headers. A verifier that cannot tell "the whole chain" from "a selection" must say so
// rather than imply the stronger claim, which is why Known exists.
type exportMeta struct {
	Known     bool
	Filtered  bool
	Truncated bool
	Format    string
}

// parse accepts both export shapes: newline-delimited rows (the default) and one document with a `rows` array.
func parse(raw []byte) ([]auditchain.Row, exportMeta, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, exportMeta{}, fmt.Errorf("the file is empty")
	}
	if trimmed[0] == '{' {
		// Could still be one NDJSON row: try the document shape first, then fall back.
		var document struct {
			Rows      []auditchain.Row `json:"rows"`
			Filtered  bool             `json:"filtered"`
			Truncated bool             `json:"truncated"`
			Format    string           `json:"format"`
		}
		if err := json.Unmarshal(trimmed, &document); err == nil && document.Rows != nil {
			return document.Rows, exportMeta{
				Known: true, Filtered: document.Filtered, Truncated: document.Truncated, Format: document.Format,
			}, nil
		}
		var single auditchain.Row
		if err := json.Unmarshal(trimmed, &single); err == nil && single.ID != 0 {
			return []auditchain.Row{single}, exportMeta{Format: "ndjson"}, nil
		}
	}

	rows := make([]auditchain.Row, 0, 128)
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	// A row carries the details of one action; the default 64 KiB line limit is generous but not unlimited.
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var row auditchain.Row
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, exportMeta{}, fmt.Errorf("a line is not an audit row: %w", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, exportMeta{}, err
	}
	return rows, exportMeta{Format: "ndjson"}, nil
}

// printVerdict writes the human-readable answer, including what the tool cannot prove.
func printVerdict(verdict auditchain.Verdict, meta exportMeta) {
	verdictText := "OK"
	if !verdict.OK {
		verdictText = "FAILED"
	}
	fmt.Printf("%s  %d row(s) checked, %d problem(s), %d id gap(s)\n",
		verdictText, verdict.Checked, len(verdict.Problems), verdict.Discontinuities)
	for _, problem := range verdict.Problems {
		fmt.Printf("     row %d: %s\n", problem.ID, problem.Problem)
	}
	if meta.Known {
		if meta.Filtered {
			fmt.Printf("     note: this export says it is a FILTERED SELECTION, not a contiguous chain\\n")
		}
		if meta.Truncated {
			fmt.Printf("     note: this export says it was TRUNCATED; rows are missing from the end\\n")
		}
	} else if verdict.Discontinuities > 0 {
		fmt.Printf("     note: this file carries no filter metadata (the export keeps it in headers), so a gap could be\\n" +
			"           a selection or a purge - verify the unfiltered export before drawing a conclusion\\n")
	}
	if verdict.Discontinuities > 0 {
		fmt.Printf("     note: an id gap is what a purge leaves behind (it writes an audit_purge marker row)\n")
	}
	fmt.Printf("     note: the hash is unkeyed and prev_hash is not an input, so this detects partial edits\n" +
		"           (a changed row, a removed row, a rewritten link), not a rewrite of every row\n")
}
