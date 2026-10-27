package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePackage builds a package on disk: a manifest plus whatever extra files the
// test asks for. Files are written with a leading "./" stripped and their parent
// directories created.
func writePackage(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const validManifest = `name: demo
version: 1.0.0
type: command
interpreter: sh
entry: main.sh
`

// TestRunCLIExitCodes pins the contract AGENTS.md documents ("exit code 0 means no
// error-level findings; warnings do not fail"). It is the contract every agent and
// script calling this tool depends on, and it was only ever checked by hand.
func TestRunCLIExitCodes(t *testing.T) {
	good := writePackage(t, validManifest, map[string]string{"main.sh": "echo hi\n"})
	broken := writePackage(t, validManifest, nil) // entry missing on purpose

	t.Run("warnings only exit 0", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		code := runCLI([]string{good}, &out, &errBuf)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (%s%s)", code, out.String(), errBuf.String())
		}
		if !strings.Contains(out.String(), "OK:") {
			t.Fatalf("no OK summary: %s", out.String())
		}
		// The unsigned-package warning is expected and must be the only one.
		if !strings.Contains(out.String(), "trust.unsigned") {
			t.Fatalf("the trust warning disappeared: %s", out.String())
		}
	})

	t.Run("error findings exit 1", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		code := runCLI([]string{broken}, &out, &errBuf)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (%s)", code, out.String())
		}
		if !strings.Contains(out.String(), "package.entry_missing") {
			t.Fatalf("the missing entry was not named: %s", out.String())
		}
		if !strings.Contains(out.String(), "FAIL:") {
			t.Fatalf("no FAIL summary: %s", out.String())
		}
	})

	t.Run("missing directory argument exits 2", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		if code := runCLI(nil, &out, &errBuf); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(errBuf.String(), "usage: plugin-check") {
			t.Fatalf("no usage on stderr: %s", errBuf.String())
		}
	})

	t.Run("require-signed fails an unsigned package", func(t *testing.T) {
		var out, errBuf bytes.Buffer
		code := runCLI([]string{good, "-require-signed"}, &out, &errBuf)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 for an unsigned package under -require-signed (%s)", code, out.String())
		}
	})
}

// TestRunCLIJSONIsMachineReadable pins the -json shape: the report must decode, keep
// the stable diagnostic codes, and still be produced for a failing package (an agent
// loop needs the structured findings, not just "exit 1").
func TestRunCLIJSONIsMachineReadable(t *testing.T) {
	broken := writePackage(t, validManifest, nil)

	var out, errBuf bytes.Buffer
	if code := runCLI([]string{"-json", broken}, &out, &errBuf); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	var report struct {
		Dir         string `json:"dir"`
		Name        string `json:"name"`
		Diagnostics []struct {
			Level   string `json:"level"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"diagnostics"`
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("the JSON report does not parse: %v\n%s", err, out.String())
	}
	if report.Name != "demo" {
		t.Fatalf("name = %q", report.Name)
	}
	if report.Errors < 1 {
		t.Fatalf("errors = %d, want at least 1", report.Errors)
	}
	codes := map[string]bool{}
	for _, d := range report.Diagnostics {
		codes[d.Code] = true
		if d.Message == "" {
			t.Fatalf("diagnostic %q has no message", d.Code)
		}
	}
	for _, want := range []string{"manifest.valid", "package.entry_missing", "trust.unsigned"} {
		if !codes[want] {
			t.Fatalf("diagnostic code %q is missing", want)
		}
	}
}

// TestRunCLIUnknownFlagExitsTwo keeps a typo from looking like a validation failure:
// a flag error is a usage problem, not a package problem.
func TestRunCLIUnknownFlagExitsTwo(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCLI([]string{"-no-such-flag", "."}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("a flag error wrote to stdout: %s", out.String())
	}
}
