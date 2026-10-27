// Command plugin-check validates a ForgeC2 plugin package the way the server
// loads it: manifest, entry, package digest/signature, tier-specific structure
// (the WASM ABI surface is inspected with the server's own runtime) and, with
// -run, one real execution through the server's executor followed by the
// output-contract check for the plugin's type.
//
// Every finding has a stable code (manifest.invalid, wasm.bad_abi,
// result.bad_json, …) and -json prints the whole report as JSON, so the tool
// is as usable from an agent loop as from a terminal. The codes are documented
// in docs/PLUGIN_AUTHORING.md.
//
//	go run ./cmd/plugin-check ./my-plugin            # static checks
//	go run ./cmd/plugin-check ./my-plugin -run       # + execute it once
//	go run ./cmd/plugin-check ./my-plugin -json      # machine-readable report
//
// Exit code 0 means no error-level findings; warnings do not fail the check.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/forgec2/forgec2/internal/plugin"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI is the whole tool behind an io.Writer wall so the exit-code and output
// contract can be tested without spawning a process (main_test.go).
func runCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	run := fs.Bool("run", false, "execute the plugin once with a sample input after the static checks (only for packages you trust)")
	input := fs.String("input", "", "raw JSON input for -run (default: the manifest's own defaults)")
	timeout := fs.Int("timeout", 0, "run timeout in seconds (default: the manifest's)")
	root := fs.String("root", "", "package tree root anchoring manifest.includes (default: the package directory)")
	keys := fs.String("key", "", "comma-separated hex Ed25519 public keys for signature verification")
	requireSigned := fs.Bool("require-signed", false, "fail unsigned packages (mirrors plugins.require_signed)")
	asJSON := fs.Bool("json", false, "print the machine-readable report instead of text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	positional := fs.Args()
	if len(positional) == 0 || strings.TrimSpace(positional[0]) == "" {
		usage(fs, stderr)
		return 2
	}
	dir := strings.TrimSpace(positional[0])
	// Go's flag package stops at the first positional argument; accept the
	// natural `plugin-check ./dir -run` reading order by parsing the rest.
	if rest := positional[1:]; len(rest) > 0 {
		_ = fs.Parse(rest)
	}

	opts := plugin.CheckOptions{
		Dir:           dir,
		Root:          strings.TrimSpace(*root),
		Run:           *run,
		Input:         *input,
		Timeout:       *timeout,
		RequireSigned: *requireSigned,
	}
	if *keys != "" {
		opts.TrustedKeys = strings.Split(*keys, ",")
	}

	report := plugin.CheckPlugin(opts)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(stderr, "plugin-check: "+err.Error())
			return 1
		}
	} else {
		printText(stdout, report)
	}
	if !report.OK() {
		return 1
	}
	return 0
}

func usage(fs *flag.FlagSet, stderr io.Writer) {
	fmt.Fprintln(stderr, "usage: plugin-check [flags] <plugin-directory>")
	fmt.Fprintln(stderr, "flags:")
	fs.SetOutput(stderr)
	fs.PrintDefaults()
}

func printText(stdout io.Writer, report *plugin.CheckReport) {
	if report.Name == "" {
		fmt.Fprintln(stdout, report.Dir)
	} else {
		fmt.Fprintf(stdout, "%s: name=%s version=%s type=%s interpreter=%s entry=%s\n",
			report.Dir, report.Name, report.Version, report.Type, report.Interpreter, report.Entry)
	}
	for _, d := range report.Diagnostics {
		fmt.Fprintf(stdout, "  [%-7s] %-26s %s\n", d.Level, d.Code, d.Message)
	}
	if report.Ran && (report.Stdout != "" || report.Stderr != "") {
		fmt.Fprintln(stdout, "  --- run output ---")
		if report.Stdout != "" {
			fmt.Fprintln(stdout, "  stdout: "+firstLines(report.Stdout, 8))
		}
		if report.Stderr != "" {
			fmt.Fprintln(stdout, "  stderr: "+firstLines(report.Stderr, 8))
		}
	}
	summary := fmt.Sprintf("%d error(s), %d warning(s)", report.Errors, report.Warnings)
	if report.OK() {
		fmt.Fprintln(stdout, "OK: "+summary)
		return
	}
	fmt.Fprintln(stdout, "FAIL: "+summary)
}

// firstLines keeps the terminal honest for plugins that print a wall of text.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		return strings.Join(lines[:n], "\n         ") + fmt.Sprintf("\n         … (%d more lines)", len(lines)-n)
	}
	return strings.Join(lines, "\n         ")
}
