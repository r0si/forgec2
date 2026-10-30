package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadNtDllImage covers reading an ntdll image from disk.
//
// The defect this guards against: a resolver that accepts anything fed to it turns a missing or wrong ntdll into a wrong syscall
// number on the target, with no visible symptom. ReadNtDllImage refuses empty files, missing files, and non-PE files, so a bad image
// fails at read time instead of deep in the stub scan.
func TestReadNtDllImage(t *testing.T) {
	dir := t.TempDir()

	// 1. A real PE image round-trips: read what you wrote back.
	peImage := buildNtStubImage(t, map[string]uint32{"NtClose": 0x0F})
	pePath := filepath.Join(dir, "ntdll.dll")
	if err := os.WriteFile(pePath, peImage, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	image, err := ReadNtDllImage(pePath)
	if err != nil {
		t.Fatalf("valid PE rejected: %v", err)
	}
	if len(image) != len(peImage) {
		t.Fatalf("image = %d bytes, want %d", len(image), len(peImage))
	}

	// 2. A missing path is refused with a message that names the path.
	if _, err := ReadNtDllImage(filepath.Join(dir, "does-not-exist.dll")); err == nil {
		t.Fatal("missing file was accepted")
	} else if !strings.Contains(err.Error(), "does-not-exist.dll") {
		t.Fatalf("error does not name the path: %q", err)
	}

	// 3. An empty file is refused.
	emptyPath := filepath.Join(dir, "empty.dll")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	if _, err := ReadNtDllImage(emptyPath); err == nil {
		t.Fatal("empty file was accepted")
	}

	// 4. A non-PE file is refused.
	notPePath := filepath.Join(dir, "not-pe.dll")
	if err := os.WriteFile(notPePath, []byte("this is not a PE image"), 0o644); err != nil {
		t.Fatalf("write non-PE fixture: %v", err)
	}
	if _, err := ReadNtDllImage(notPePath); err == nil {
		t.Fatal("non-PE file was accepted")
	}

	// 5. An empty path is refused without touching the filesystem.
	if _, err := ReadNtDllImage(""); err == nil {
		t.Fatal("empty path was accepted")
	}
}

// TestReadNtDllImageComposesWithParsers covers the real W1 path: read ntdll from disk, resolve exports, parse SSNs.
//
// The parsers were tested in isolation; this test pins that reading a file and feeding it to them produces the same SSN numbers the
// fixture encoded, end to end, with no Windows API and no build tag anywhere.
func TestReadNtDllImageComposesWithParsers(t *testing.T) {
	dir := t.TempDir()
	peImage := buildNtStubImage(t, map[string]uint32{
		"NtClose":                 0x0F,
		"NtAllocateVirtualMemory": 0x18,
		"NtProtectVirtualMemory":  0x50,
		"NtWriteVirtualMemory":    0x3A,
		"NtCreateThreadEx":        0xC2,
	})
	pePath := filepath.Join(dir, "ntdll.dll")
	if err := os.WriteFile(pePath, peImage, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	image, err := ReadNtDllImage(pePath)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}

	table, complete, err := ResolveRequiredSyscalls(image)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !complete {
		t.Fatalf("table incomplete: %v", table.Failed)
	}

	ssn, ok := table.Lookup("NtClose")
	if !ok || ssn != 0x0F {
		t.Fatalf("NtClose = %d (ok=%v), want 0x0F", ssn, ok)
	}
	ssn, ok = table.Lookup("NtAllocateVirtualMemory")
	if !ok || ssn != 0x18 {
		t.Fatalf("NtAllocateVirtualMemory = %d (ok=%v), want 0x18", ssn, ok)
	}
}
