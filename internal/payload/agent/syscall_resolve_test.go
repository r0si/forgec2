package main

import (
	"encoding/binary"
	"testing"
)

// buildNtStubImage assembles a PE64 fixture whose export table maps each name to a stub with the given syscall number.
//
// The two parsers are tested separately; this fixture exists to test them COMPOSED, which is the shape the agent would call and the
// shape where an off-by-one between "export RVA" and "stub address" would show up.
func buildNtStubImage(t *testing.T, functions map[string]uint32) []byte {
	t.Helper()
	const (
		peOffset     = 0x40
		optionalSize = 0xF0
		sectionVA    = 0x1000
		sectionRaw   = 0x200
		dirRVA       = sectionVA
		namesRVA     = sectionVA + 0x60
		ordinalsRVA  = sectionVA + 0x140
		functionsRVA = sectionVA + 0x1C0
		stringsRVA   = sectionVA + 0x240
		stubsRVA     = sectionVA + 0x400
	)
	image := make([]byte, sectionRaw+0x800)
	binary.LittleEndian.PutUint16(image[0:2], dosHeaderSignature)
	binary.LittleEndian.PutUint32(image[dosLfanewOffset:dosLfanewOffset+4], peOffset)
	binary.LittleEndian.PutUint32(image[peOffset:peOffset+4], peSignature)
	coff := peOffset + 4
	binary.LittleEndian.PutUint16(image[coff:coff+2], 0x8664)
	binary.LittleEndian.PutUint16(image[coff+2:coff+4], 1)
	binary.LittleEndian.PutUint16(image[coff+16:coff+18], optionalSize)
	optional := coff + 20
	binary.LittleEndian.PutUint16(image[optional:optional+2], pe32PlusMagic)
	binary.LittleEndian.PutUint32(image[optional+108:optional+112], 16) // NumberOfRvaAndSizes
	binary.LittleEndian.PutUint32(image[optional+112:optional+116], dirRVA)
	binary.LittleEndian.PutUint32(image[optional+116:optional+120], 0x40)

	sectionTable := optional + optionalSize
	binary.LittleEndian.PutUint32(image[sectionTable+8:sectionTable+12], 0x800)
	binary.LittleEndian.PutUint32(image[sectionTable+12:sectionTable+16], sectionVA)
	binary.LittleEndian.PutUint32(image[sectionTable+16:sectionTable+20], 0x800)
	binary.LittleEndian.PutUint32(image[sectionTable+20:sectionTable+24], sectionRaw)

	toOffset := func(rva uint32) int { return sectionRaw + int(rva-sectionVA) }

	// Sorted names, as a real export table stores them.
	names := make([]string, 0, len(functions))
	for name := range functions {
		names = append(names, name)
	}
	sortStrings(names)

	dir := toOffset(dirRVA)
	binary.LittleEndian.PutUint32(image[dir+24:dir+28], uint32(len(names)))
	binary.LittleEndian.PutUint32(image[dir+28:dir+32], functionsRVA)
	binary.LittleEndian.PutUint32(image[dir+32:dir+36], namesRVA)
	binary.LittleEndian.PutUint32(image[dir+36:dir+40], ordinalsRVA)

	nameRVA := uint32(stringsRVA)
	stubRVA := uint32(stubsRVA)
	for index, name := range names {
		copy(image[toOffset(nameRVA):], name)
		binary.LittleEndian.PutUint32(image[toOffset(namesRVA)+index*4:], nameRVA)
		nameRVA += uint32(len(name)) + 1

		binary.LittleEndian.PutUint16(image[toOffset(ordinalsRVA)+index*2:], uint16(index))
		binary.LittleEndian.PutUint32(image[toOffset(functionsRVA)+index*4:], stubRVA)

		stub := image[toOffset(stubRVA):]
		copy(stub, buildStub(false, functions[name]))
		stubRVA += 0x20
	}
	return image
}

// sortStrings is a tiny insertion sort, so this test file stays dependency-free in the same way the parsers are.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// TestResolveNtSyscallsComposed covers the composition the agent would call.
func TestResolveNtSyscallsComposed(t *testing.T) {
	image := buildNtStubImage(t, map[string]uint32{
		"NtAllocateVirtualMemory": 0x18,
		"NtProtectVirtualMemory":  0x50,
		"NtWriteVirtualMemory":    0x3A,
		"NtCreateThreadEx":        0xC2,
		"NtClose":                 0x0F,
	})

	table, complete, err := ResolveRequiredSyscalls(image)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !complete {
		t.Fatalf("the table is incomplete: %v", table.Failed)
	}
	// Each name must map to ITS number: a shared-stub bug would make every lookup return the same value and look complete.
	want := map[string]uint16{
		"NtAllocateVirtualMemory": 0x18,
		"NtProtectVirtualMemory":  0x50,
		"NtWriteVirtualMemory":    0x3A,
		"NtCreateThreadEx":        0xC2,
		"NtClose":                 0x0F,
	}
	for name, expected := range want {
		ssn, ok := table.Lookup(name)
		if !ok {
			t.Fatalf("%s did not resolve", name)
		}
		if ssn != expected {
			t.Fatalf("%s = 0x%X, want 0x%X", name, ssn, expected)
		}
	}

	// A partial image reports what is missing instead of claiming completeness.
	partial := buildNtStubImage(t, map[string]uint32{"NtClose": 0x0F})
	partialTable, partialComplete, err := ResolveRequiredSyscalls(partial)
	if err != nil {
		t.Fatalf("partial resolve: %v", err)
	}
	if partialComplete {
		t.Fatal("a table missing four of five names reported itself complete")
	}
	if _, ok := partialTable.Lookup("NtAllocateVirtualMemory"); ok {
		t.Fatal("a name absent from the image resolved")
	}
	if len(partialTable.Failed) != 4 {
		t.Fatalf("failed = %d, want 4: %v", len(partialTable.Failed), partialTable.Failed)
	}
	// The failure reason must be actionable: it names the export, so an operator can tell a build difference from a parser bug.
	for name, reason := range partialTable.Failed {
		if reason == "" {
			t.Fatalf("%s failed without a reason", name)
		}
	}

	// Empty input is refused rather than producing an empty-but-complete table.
	if _, err := ResolveNtSyscalls(nil, []string{"NtClose"}); err == nil {
		t.Fatal("an empty image was accepted")
	}
	if _, err := ResolveNtSyscalls(image, nil); err == nil {
		t.Fatal("an empty request was accepted")
	}
}
