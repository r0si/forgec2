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
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: verify-audit [-json] <audit.ndjson|audit.json>\n")
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
	rows, err := parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-audit: %v\n", err)
		os.Exit(2)
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "verify-audit: no audit rows in %s\n", flag.Arg(0))
		os.Exit(2)
	}

	verdict := auditchain.Verify(rows)
	if *asJSON {
		encoded, err := json.MarshalIndent(verdict, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify-audit: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(encoded))
	} else {
		printVerdict(verdict)
	}
	if !verdict.OK {
		os.Exit(1)
	}
}

// parse accepts both export shapes: newline-delimited rows (the default) and one document with a `rows` array.
func parse(raw []byte) ([]auditchain.Row, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("the file is empty")
	}
	if trimmed[0] == '{' {
		// Could still be one NDJSON row: try the document shape first, then fall back.
		var document struct {
			Rows []auditchain.Row `json:"rows"`
		}
		if err := json.Unmarshal(trimmed, &document); err == nil && document.Rows != nil {
			return document.Rows, nil
		}
		var single auditchain.Row
		if err := json.Unmarshal(trimmed, &single); err == nil && single.ID != 0 {
			return []auditchain.Row{single}, nil
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
			return nil, fmt.Errorf("a line is not an audit row: %w", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// printVerdict writes the human-readable answer, including what the tool cannot prove.
func printVerdict(verdict auditchain.Verdict) {
	verdictText := "OK"
	if !verdict.OK {
		verdictText = "FAILED"
	}
	fmt.Printf("%s  %d row(s) checked, %d problem(s), %d id gap(s)\n",
		verdictText, verdict.Checked, len(verdict.Problems), verdict.Discontinuities)
	for _, problem := range verdict.Problems {
		fmt.Printf("     row %d: %s\n", problem.ID, problem.Problem)
	}
	if verdict.Discontinuities > 0 {
		fmt.Printf("     note: an id gap is what a purge leaves behind (it writes an audit_purge marker row)\n")
	}
	fmt.Printf("     note: the hash is unkeyed and prev_hash is not an input, so this detects partial edits\n" +
		"           (a changed row, a removed row, a rewritten link), not a rewrite of every row\n")
}
