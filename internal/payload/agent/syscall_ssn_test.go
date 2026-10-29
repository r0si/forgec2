package main

import (
	"encoding/binary"
	"testing"
)

// buildStub assembles an ntdll-shaped stub: optional hook jump, then the prologue and the immediate.
func buildStub(hook bool, ssn uint32) []byte {
	stub := make([]byte, 0, 32)
	if hook {
		// `E9 rel32` - the five-byte jump a user-mode hook installs.
		stub = append(stub, 0xE9, 0x00, 0x00, 0x00, 0x00)
	}
	stub = append(stub, 0x4C, 0x8B, 0xD1, 0xB8)
	immediate := make([]byte, 4)
	binary.LittleEndian.PutUint32(immediate, ssn)
	stub = append(stub, immediate...)
	// A realistic tail: `test eax, eax` and the syscall+return pair.
	stub = append(stub, 0x0F, 0x05, 0xC3)
	return stub
}

// TestExtractSSN covers the parsing this whole technique rests on.
//
// The failure that matters is not "it did not parse" but "it parsed the wrong thing": a mis-read stub yields a plausible-looking syscall
// number that calls a different function on the target, with nothing in the console to show for it. So the tests pin the happy path,
// the hooked-stub case, and every shape that must be REFUSED rather than guessed.
func TestExtractSSN(t *testing.T) {
	// 1. An unhooked stub resolves.
	image := append(make([]byte, 0x100), buildStub(false, 0x18)...)
	ssn, err := ExtractSSN(image, 0x100)
	if err != nil {
		t.Fatalf("plain stub: %v", err)
	}
	if ssn != 0x18 {
		t.Fatalf("ssn = 0x%X, want 0x18", ssn)
	}

	// 2. A stub behind a five-byte hook still resolves: the prologue sits past the jump, which is why the search window exists.
	hooked := append(make([]byte, 0x100), buildStub(true, 0x55)...)
	ssn, err = ExtractSSN(hooked, 0x100)
	if err != nil {
		t.Fatalf("hooked stub: %v", err)
	}
	if ssn != 0x55 {
		t.Fatalf("ssn = 0x%X, want 0x55", ssn)
	}

	// 3. Refusals, each of which must not produce a number.
	cases := []struct {
		name  string
		image []byte
		rva   uint32
	}{
		{"empty image", nil, 0},
		{"rva past the end", make([]byte, 16), 0x100},
		{"wrong prologue", append(make([]byte, 0x40), []byte{0x48, 0x89, 0xC8, 0xB8, 0x01, 0x00, 0x00, 0x00}...), 0x40},
		{"prologue but no mov eax", append(make([]byte, 0x40), []byte{0x4C, 0x8B, 0xD1, 0x90, 0x90, 0x90, 0x90, 0x90}...), 0x40},
		// 0xFFFF is above maxSSN: a value that means the bytes were not a stub.
		{"ssn out of range", append(make([]byte, 0x40), buildStub(false, 0xFFFF)...), 0x40},
		// Zero is not a syscall number any build uses, and accepting it would issue NtAcceptConnectPort by accident.
		{"ssn zero", append(make([]byte, 0x40), buildStub(false, 0)...), 0x40},
	}
	for _, testCase := range cases {
		if ssn, err := ExtractSSN(testCase.image, testCase.rva); err == nil {
			t.Fatalf("%s: returned 0x%X instead of refusing", testCase.name, ssn)
		}
	}
}

// TestSSNTableKeepsFailures covers the partial table: a build missing one export must say so rather than look complete.
func TestSSNTableKeepsFailures(t *testing.T) {
	image := append(make([]byte, 0x200), buildStub(false, 0x12)...)
	image = append(image, buildStub(false, 0x2A)...)

	table := NewSSNTable("ntdll-test", image, map[string]uint32{
		"NtClose":          0x200,
		"NtCreateFile":     0x200 + uint32(len(image)-0x200)/2,
		"NtNotOnThisBuild": 0xFFFF00, // past the end: must fail, not resolve to garbage
	})
	if table.Source != "ntdll-test" {
		t.Fatalf("source = %q", table.Source)
	}
	if _, ok := table.Lookup("NtNotOnThisBuild"); ok {
		t.Fatal("an unresolvable export produced a number")
	}
	if table.Failed["NtNotOnThisBuild"] == "" {
		t.Fatal("the failure was not recorded with a reason")
	}
	if table.Complete() {
		t.Fatal("a table with a failure reported itself complete")
	}
	// A name nobody asked for is a miss, not an error.
	if _, ok := table.Lookup("NtNeverRequested"); ok {
		t.Fatal("an unrequested name resolved")
	}
	// And a nil table answers safely rather than panicking on a path that may run before resolution.
	var empty *SSNTable
	if _, ok := empty.Lookup("NtClose"); ok {
		t.Fatal("a nil table resolved a name")
	}
	if empty.Complete() {
		t.Fatal("a nil table reported itself complete")
	}
}
