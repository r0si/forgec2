// Command verify-plugin checks a plugin package you RECEIVED against the key you trust.
//
// plugin-check answers "is this package valid and does it run"; it takes a directory, and it treats signatures as
// optional because the bundled plugins ship unsigned. The receiving case is different in three ways, and this command is
// only those three: the package usually arrives as an **archive**, the caller already knows which **key** they expect,
// and the whole point is a yes/no a pipeline can act on.
//
//	go run ./cmd/verify-plugin package.zip -key <hex>
//	go run ./cmd/verify-plugin ./some/dir  -key <hex> -quiet
//
// Exit codes: 0 the package is valid and signed by the supplied key, 1 it is invalid, unsigned or signed by another key,
// 2 the archive could not be read at all.
package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/forgec2/forgec2/internal/plugin"
)

func main() {
	key := flag.String("key", "", "hex Ed25519 public key you trust; required, because a package that could be signed by anyone is not verified")
	quiet := flag.Bool("quiet", false, "print nothing; rely on the exit code")
	asJSON := flag.Bool("json", false, "print the machine-readable report")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: verify-plugin -key <hex> [-quiet] [-json] <package.zip|directory>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 || strings.TrimSpace(*key) == "" {
		// The key is not optional: without it "verified" would only mean "the package agrees with itself".
		flag.Usage()
		os.Exit(2)
	}

	dir := flag.Arg(0)
	cleanup := func() {}
	info, err := os.Stat(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-plugin: %v\n", err)
		os.Exit(2)
	}
	if !info.IsDir() {
		extracted, cleanupFunc, err := extractPackage(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify-plugin: %v\n", err)
			os.Exit(2)
		}
		dir, cleanup = extracted, cleanupFunc
	}
	defer cleanup()

	report := plugin.CheckPlugin(plugin.CheckOptions{
		Dir:           dir,
		RequireSigned: true,
		TrustedKeys:   []string{strings.TrimSpace(*key)},
	})

	switch {
	case *quiet:
	case *asJSON:
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify-plugin: %v\n", err)
			os.Exit(2)
		}
		fmt.Println(string(encoded))
	default:
		printText(report)
	}

	if !report.OK() {
		os.Exit(1)
	}
}

// extractPackage unpacks an archive into a temporary directory and returns it with its cleanup.
//
// Entries are resolved inside the destination and refused when they would escape it: a package is attacker-supplied
// input, and an "unzip then validate" that honoured `../` would let the archive choose where its files land.
func extractPackage(path string) (string, func(), error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", nil, fmt.Errorf("not a zip archive: %w", err)
	}
	defer archive.Close()

	dir, err := os.MkdirTemp("", "verify-plugin-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	for _, file := range archive.File {
		target, err := safeJoin(dir, file.Name)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				cleanup()
				return "", nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", nil, err
		}
		source, err := file.Open()
		if err != nil {
			cleanup()
			return "", nil, err
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = source.Close()
			cleanup()
			return "", nil, err
		}
		_, copyErr := io.Copy(destination, source)
		_ = destination.Close()
		_ = source.Close()
		if copyErr != nil {
			cleanup()
			return "", nil, copyErr
		}
	}

	// A zip of a single top-level directory is the common shape: use it, so the manifest is found where the packager
	// put it.
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(dir, entries[0].Name()), cleanup, nil
	}
	return dir, cleanup, nil
}

// safeJoin resolves a zip entry inside root, refusing anything that would escape it.
func safeJoin(root, name string) (string, error) {
	cleaned := filepath.Clean(strings.TrimPrefix(filepath.FromSlash(name), string(filepath.Separator)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the archive contains an entry that escapes the package directory: %q", name)
	}
	return filepath.Join(root, cleaned), nil
}

// printText writes the verdict and every error-level diagnostic, because a bare "FAILED" leaves the receiver guessing
// which check they are being told about.
func printText(report *plugin.CheckReport) {
	verdict := "OK"
	if !report.OK() {
		verdict = "FAILED"
	}
	fmt.Printf("%s  plugin %q %s\n", verdict, report.Name, report.Version)
	fmt.Printf("     %d error(s), %d warning(s)\n", report.Errors, report.Warnings)
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Level == plugin.DiagError {
			fmt.Printf("     %s: %s\n", diagnostic.Code, diagnostic.Message)
		}
	}
}
