package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSafeJoinRefusesEscapes is the security property this command depends on: a package is attacker-supplied input, so
// an entry that resolves outside the extraction directory must be refused rather than written.
func TestSafeJoinRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	allowed := []string{"manifest.yaml", "main.py", "sub/dir/file.txt"}
	for _, name := range allowed {
		if _, err := safeJoin(root, name); err != nil {
			t.Fatalf("%q was refused but is inside the package: %v", name, err)
		}
	}
	for _, name := range []string{"../escape.txt", "a/../../escape.txt", "..", "./../x"} {
		if _, err := safeJoin(root, name); err == nil {
			t.Fatalf("%q was accepted and would escape the package directory", name)
		}
	}
}

// TestExtractPackageUnwrapsASingleRoot covers the shape packagers actually produce - one top-level directory - so the
// manifest is found where they put it instead of one level too high.
func TestExtractPackageUnwrapsASingleRoot(t *testing.T) {
	archivePath := writeZip(t, map[string]string{
		"myplugin/manifest.yaml": "name: demo\n",
		"myplugin/main.py":       "print(1)\n",
	})

	dir, cleanup, err := extractPackage(archivePath)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer cleanup()

	if filepath.Base(dir) != "myplugin" {
		t.Fatalf("extracted root = %q, want the single top-level directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.yaml")); err != nil {
		t.Fatalf("the manifest is not where the package put it: %v", err)
	}
}

// TestExtractPackageKeepsFlatLayouts is the other common shape: files at the archive root, which must not be unwrapped
// into a subdirectory that does not exist.
func TestExtractPackageKeepsFlatLayouts(t *testing.T) {
	archivePath := writeZip(t, map[string]string{
		"manifest.yaml": "name: demo\n",
		"main.py":       "print(1)\n",
	})

	dir, cleanup, err := extractPackage(archivePath)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dir, "manifest.yaml")); err != nil {
		t.Fatalf("a flat archive was unwrapped incorrectly: %v", err)
	}
}

// TestExtractPackageRefusesANonZip keeps the error path honest: a tarball or a text file says so instead of producing an
// empty directory that then fails validation for the wrong reason.
func TestExtractPackageRefusesANonZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.tar.gz")
	if err := os.WriteFile(path, []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := extractPackage(path); err == nil || !strings.Contains(err.Error(), "not a zip") {
		t.Fatalf("a non-zip was accepted: %v", err)
	}
}

// writeZip builds an archive on disk for the extractor.
func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		out, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := out.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	path := filepath.Join(t.TempDir(), "package.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path
}
