package main

import (
	"encoding/binary"
	"testing"
)

// buildSyntheticPE assembles the smallest PE64 image with a usable export table.
//
// The parser's hazards are all about offsets, so the fixture is built offset by offset rather than packed into a blob: a test that
// cannot say where a field sits cannot tell a parsing bug from a fixture bug.
func buildSyntheticPE(t *testing.T, exports []syntheticExport) []byte {
	t.Helper()
	const (
		peOffset     = 0x40
		optionalSize = 0xF0 // PE32+ minimum with 16 data directories
		sectionVA    = 0x1000
		sectionRaw   = 0x200
	)
	image := make([]byte, sectionRaw+0x400)

	// DOS header.
	binary.LittleEndian.PutUint16(image[0:2], dosHeaderSignature)
	binary.LittleEndian.PutUint32(image[dosLfanewOffset:dosLfanewOffset+4], peOffset)
	// PE signature + COFF header.
	binary.LittleEndian.PutUint32(image[peOffset:peOffset+4], peSignature)
	coff := peOffset + 4
	binary.LittleEndian.PutUint16(image[coff:coff+2], 0x8664) // machine: x64
	binary.LittleEndian.PutUint16(image[coff+2:coff+4], 1)    // one section
	binary.LittleEndian.PutUint16(image[coff+16:coff+18], optionalSize)
	optional := coff + 20
	binary.LittleEndian.PutUint16(image[optional:optional+2], pe32PlusMagic)

	// Data directories: entry 0 is the export table, RVAs relative to the section's virtual address.
	directoryOffset := 112
	binary.LittleEndian.PutUint32(image[optional+directoryOffset-4:optional+directoryOffset], 16)

	// Section table (one section, ".rdata").
	sectionTable := optional + optionalSize
	binary.LittleEndian.PutUint32(image[sectionTable+8:sectionTable+12], 0x400) // virtual size
	binary.LittleEndian.PutUint32(image[sectionTable+12:sectionTable+16], sectionVA)
	binary.LittleEndian.PutUint32(image[sectionTable+16:sectionTable+20], 0x400) // raw size
	binary.LittleEndian.PutUint32(image[sectionTable+20:sectionTable+24], sectionRaw)

	// Layout inside the section: export directory, then names, ordinals, functions, then the name strings and bodies.
	const (
		dirRVA       = sectionVA
		namesRVA     = sectionVA + 0x40
		ordinalsRVA  = sectionVA + 0x80
		functionsRVA = sectionVA + 0xC0
		stringsRVA   = sectionVA + 0x100
		bodiesRVA    = sectionVA + 0x200
	)
	binary.LittleEndian.PutUint32(image[optional+directoryOffset:optional+directoryOffset+4], dirRVA)
	binary.LittleEndian.PutUint32(image[optional+directoryOffset+4:optional+directoryOffset+8], 0x40)

	dir := sectionRaw                                                         // dirRVA - sectionVA == 0
	binary.LittleEndian.PutUint32(image[dir+24:dir+28], uint32(len(exports))) // NumberOfNames
	binary.LittleEndian.PutUint32(image[dir+28:dir+32], functionsRVA)         // AddressOfFunctions
	binary.LittleEndian.PutUint32(image[dir+32:dir+36], namesRVA)             // AddressOfNames
	binary.LittleEndian.PutUint32(image[dir+36:dir+40], ordinalsRVA)          // AddressOfNameOrdinals

	stringCursor := int64(sectionRaw + (stringsRVA - sectionVA))
	bodyCursor := int64(sectionRaw + (bodiesRVA - sectionVA))
	for index, export := range exports {
		nameOffset := stringCursor
		copy(image[stringCursor:], export.name)
		stringCursor += int64(len(export.name)) + 1

		if export.forwarded != "" {
			// A forwarder's RVA points INSIDE the export directory, which is the shape the parser must refuse.
			binary.LittleEndian.PutUint32(image[sectionRaw+(namesRVA-sectionVA)+int64(index)*4:], uint32(nameOffset))
			binary.LittleEndian.PutUint32(image[sectionRaw+(namesRVA-sectionVA)+int64(index)*4+4:], 0)
			binary.LittleEndian.PutUint16(image[sectionRaw+(ordinalsRVA-sectionVA)+int64(index)*2:], uint16(index))
			binary.LittleEndian.PutUint32(image[sectionRaw+(functionsRVA-sectionVA)+int64(index)*4:], dirRVA+8)
			continue
		}
		binary.LittleEndian.PutUint32(image[sectionRaw+(namesRVA-sectionVA)+int64(index)*4:], bodiesRVA+uint32(index)*0x10)
		binary.LittleEndian.PutUint16(image[sectionRaw+(ordinalsRVA-sectionVA)+int64(index)*2:], uint16(index))
		binary.LittleEndian.PutUint32(image[sectionRaw+(functionsRVA-sectionVA)+int64(index)*4:], bodiesRVA+uint32(index)*0x10)
		bodyCursor += 0x10
	}
	_ = bodyCursor
	// The name table stores RVAs, not file offsets: writing a file offset here makes every lookup miss, which is exactly the kind
	// of unit confusion the parser exists to avoid.
	nameRVA := uint32(stringsRVA)
	for index := range exports {
		binary.LittleEndian.PutUint32(image[sectionRaw+(namesRVA-sectionVA)+int64(index)*4:], nameRVA)
		nameRVA += uint32(len(exports[index].name)) + 1
	}
	return image
}

type syntheticExport struct {
	name      string
	forwarded string
}

// TestFindExportRVA covers the export walk, including every shape that must be refused.
func TestFindExportRVA(t *testing.T) {
	image := buildSyntheticPE(t, []syntheticExport{
		{name: "NtClose"},
		{name: "NtCreateFile"},
		{name: "ForwardedThing", forwarded: "other.NtThing"},
	})

	// 1. A name resolves to its body RVA inside the section.
	rva, err := FindExportRVA(image, "NtClose")
	if err != nil {
		t.Fatalf("NtClose: %v", err)
	}
	if rva < 0x1200 || rva > 0x1400 {
		t.Fatalf("NtClose RVA = 0x%X, want inside the section's body area", rva)
	}
	second, err := FindExportRVA(image, "NtCreateFile")
	if err != nil {
		t.Fatalf("NtCreateFile: %v", err)
	}
	if second == rva {
		t.Fatal("two exports resolved to the same RVA, so the name table is not being followed")
	}

	// 2. A forwarder is refused, not returned as an address.
	if _, err := FindExportRVA(image, "ForwardedThing"); err == nil {
		t.Fatal("a forwarded export was returned as a body address")
	}

	// 3. A missing name is refused with the count, so a caller can tell "wrong build" from "wrong table".
	if _, err := FindExportRVA(image, "NtNotHere"); err == nil {
		t.Fatal("a missing export resolved")
	}

	// 4. Non-PE inputs are refused rather than parsed as something else.
	for _, testCase := range []struct {
		name  string
		image []byte
	}{
		{"empty", nil},
		{"too short", []byte("MZ")},
		{"no MZ", make([]byte, 512)},
		{"lfanew out of range", func() []byte {
			buffer := make([]byte, 512)
			binary.LittleEndian.PutUint16(buffer[0:2], dosHeaderSignature)
			binary.LittleEndian.PutUint32(buffer[dosLfanewOffset:dosLfanewOffset+4], 0xFFFFFF)
			return buffer
		}()},
		{"truncated after the signature", func() []byte {
			buffer := make([]byte, 0x44)
			binary.LittleEndian.PutUint16(buffer[0:2], dosHeaderSignature)
			binary.LittleEndian.PutUint32(buffer[dosLfanewOffset:dosLfanewOffset+4], 0x40)
			binary.LittleEndian.PutUint32(buffer[0x40:0x44], peSignature)
			return buffer
		}()},
	} {
		if _, err := FindExportRVA(testCase.image, "NtClose"); err == nil {
			t.Fatalf("%s: parsed an image that is not a PE", testCase.name)
		}
	}
}

// TestExportNameAtReadsTheString covers the small helper the forwarder path relies on.
func TestExportNameAtReadsTheString(t *testing.T) {
	buffer := []byte("NTDLL.RtlAllocateHeap\x00trailing")
	if got := exportNameAt(buffer, 0); got != "NTDLL.RtlAllocateHeap" {
		t.Fatalf("name = %q", got)
	}
	// Out of range and empty strings return "", never a panic: this runs on attacker-influenced bytes in the real flow.
	if got := exportNameAt(buffer, 1<<20); got != "" {
		t.Fatalf("out-of-range name = %q", got)
	}
	if got := exportNameAt([]byte{0x00}, 0); got != "" {
		t.Fatalf("empty name = %q", got)
	}
}
