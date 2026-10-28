// Command verify-bundle checks an engagement handover archive without a running server.
//
// The receiving side of a handover is usually not a ForgeC2 operator, and asking them to install a console to answer
// "did these files survive the trip" would mean they never answer it. This reads the zip, re-computes every published
// digest and checks the signature:
//
//	go run ./cmd/verify-bundle handover.zip
//	go run ./cmd/verify-bundle handover.zip -key <hex ed25519 public key> -json
//
// Exit codes: 0 the archive matches its manifest, 1 a member is damaged or missing (or arrived unlisted), 2 the file
// could not be read as a bundle at all. A valid signature is reported separately from a TRUSTED one: without -key the
// key travelled inside the archive and proves internal consistency only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/forgec2/forgec2/internal/bundle"
)

func main() {
	key := flag.String("key", "", "hex Ed25519 public key you already trust; makes the signature TRUSTED rather than merely valid")
	asJSON := flag.Bool("json", false, "print the report as JSON")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: verify-bundle [-key <hex>] [-json] <handover.zip>\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-bundle: %v\n", err)
		os.Exit(2)
	}

	report, err := bundle.Verify(raw, *key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-bundle: %v\n", err)
		os.Exit(2)
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify-bundle: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(encoded))
	} else {
		printReport(report)
	}

	if !report.OK {
		os.Exit(1)
	}
}

// printReport writes the human-readable verdict: what is intact, what is not, and what the signature does and does not
// prove.
func printReport(report *bundle.Report) {
	verdict := "OK"
	if !report.OK {
		verdict = "FAILED"
	}
	fmt.Printf("%s  engagement %d (%s), generated %s\n", verdict, report.EngagementID, report.Engagement, report.GeneratedAt)
	fmt.Printf("     archive sha256: %s\n", report.ArchiveSHA256)
	fmt.Printf("     members: %d checked, %d intact, %d unlisted\n",
		report.Checked, report.Verified, len(report.Extra))
	for _, problem := range report.Mismatched {
		fmt.Printf("     DAMAGED  %s: %s\n", problem.Name, problem.Problem)
	}
	for _, name := range report.Extra {
		fmt.Printf("     UNLISTED %s: present in the archive but not in the manifest\n", name)
	}
	for _, problem := range report.Unverifiable {
		fmt.Printf("     NO DIGEST %s: %s\n", problem.Name, problem.Problem)
	}

	switch {
	case !report.Signed:
		fmt.Printf("     signature: none (%s)\n", report.SignatureNote)
	case report.Trusted:
		fmt.Printf("     signature: VALID and TRUSTED (key %s…)\n", first16(report.PublicKey))
	case report.SignatureOK:
		fmt.Printf("     signature: valid, NOT trusted - %s\n", report.TrustedNote)
	default:
		fmt.Printf("     signature: INVALID - %s\n", report.SignatureNote)
	}
}

// first16 shortens a key for display.
func first16(key string) string {
	if len(key) <= 16 {
		return key
	}
	return key[:16]
}
