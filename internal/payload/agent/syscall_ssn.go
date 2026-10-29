package main

import (
	"encoding/binary"
	"fmt"
)

// Syscall number (SSN) resolution from ntdll's own code.
//
// The technique is well known - find the stub, read the immediate it moves into eax - and the risk is not in the idea but in the
// parsing: a stub that has been hooked, a build where the prologue differs, or a table indexed by the wrong export leaves the
// caller issuing a different syscall than it asked for, on the target, with no way to see the mistake from the console.
//
// So this file is PURE: no Windows API, no assembly, no build tag. Everything here takes a byte slice and returns numbers, which means
// it is exercised by ordinary tests on any platform, and the decision to USE it stays a separate one. Nothing in the agent calls it
// yet.

// syscallStubPrologue is the x64 ntdll prologue that precedes the SSN immediate:
//
//	4C 8B D1   mov r10, rcx
//	B8 xx xx xx xx   mov eax, <SSN>
//
// Anything else means the stub is not the shape this resolver understands, and guessing is worse than refusing.
var syscallStubPrologue = []byte{0x4C, 0x8B, 0xD1}

// ssnSearchWindow bounds how far past the stub's entry the prologue may sit.
//
// A hooked stub starts with a jump, so the real prologue can be a few bytes in; eight bytes covers the two hook shapes in common use
// (`E9 rel32` and `FF 25 rel32`) without wandering into a neighbouring function.
const ssnSearchWindow = 8

// maxSSN is the largest syscall number this resolver accepts.
//
// Windows has never allocated anything close to this, so a value above it means the bytes were not a stub - a check that turns a
// silent wrong-syscall into a refusal.
const maxSSN = 0x2000

// ErrNoStub reports that no recognisable prologue was found near the export.
type ErrNoStub struct {
	ExportRVA uint32
	Scanned   int
}

func (e *ErrNoStub) Error() string {
	return fmt.Sprintf("no syscall stub prologue near export RVA 0x%X (scanned %d bytes)", e.ExportRVA, e.Scanned)
}

// ErrSSNOutOfRange reports an immediate that cannot be a syscall number.
type ErrSSNOutOfRange struct {
	ExportRVA uint32
	Value     uint32
}

func (e *ErrSSNOutOfRange) Error() string {
	return fmt.Sprintf("export RVA 0x%X yields syscall number 0x%X, which is out of range", e.ExportRVA, e.Value)
}

// ExtractSSN reads the syscall number from the stub at exportRVA in an ntdll image.
//
// image is the whole mapped file (as read from disk, not as mapped in memory: file offsets and RVAs differ, and reading the mapped
// copy of ntdll would read the one an EDR has patched). The caller is responsible for having resolved exportRVA from the export
// directory; this function only parses bytes.
func ExtractSSN(image []byte, exportRVA uint32) (uint16, error) {
	if len(image) == 0 {
		return 0, &ErrNoStub{ExportRVA: exportRVA, Scanned: 0}
	}
	if int64(exportRVA) >= int64(len(image)) {
		return 0, &ErrNoStub{ExportRVA: exportRVA, Scanned: 0}
	}

	start := int(exportRVA)
	// The prologue may be preceded by a hook jump, so scan a short window and take the first recognisable shape.
	for offset := 0; offset <= ssnSearchWindow; offset++ {
		position := start + offset
		if position+len(syscallStubPrologue)+5 > len(image) {
			break
		}
		if !equalBytes(image[position:position+len(syscallStubPrologue)], syscallStubPrologue) {
			continue
		}
		immediate := position + len(syscallStubPrologue)
		if image[immediate] != 0xB8 { // mov eax, imm32
			continue
		}
		value := binary.LittleEndian.Uint32(image[immediate+1 : immediate+5])
		if value == 0 || value > maxSSN {
			return 0, &ErrSSNOutOfRange{ExportRVA: exportRVA, Value: value}
		}
		return uint16(value), nil
	}
	return 0, &ErrNoStub{ExportRVA: exportRVA, Scanned: ssnSearchWindow + 1}
}

// equalBytes compares two byte slices without pulling in bytes.Equal, so this file stays dependency-free.
func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// SSNTable is a resolved name -> syscall number map, with the build it came from.
//
// The build identity matters because ntdll's numbering is not stable across Windows versions: a table resolved on one build and used
// on another calls whatever function happens to sit at that number. Storing the source makes that mismatch detectable instead of
// mysterious.
type SSNTable struct {
	// Source identifies the ntdll the numbers came from (version string, or a hash of the file).
	Source string
	// Numbers maps an exported function name to its syscall number.
	Numbers map[string]uint16
	// Failed names the exports that could not be resolved, with the reason a caller can act on.
	Failed map[string]string
}

// NewSSNTable resolves the named exports from one image.
//
// Both outcomes are kept: a partial table is normal (an export may be absent on an older build), and dropping the failures would make
// a missing function look like one that was never asked for.
func NewSSNTable(source string, image []byte, exports map[string]uint32) *SSNTable {
	table := &SSNTable{
		Source:  source,
		Numbers: make(map[string]uint16, len(exports)),
		Failed:  map[string]string{},
	}
	for name, rva := range exports {
		ssn, err := ExtractSSN(image, rva)
		if err != nil {
			table.Failed[name] = err.Error()
			continue
		}
		table.Numbers[name] = ssn
	}
	return table
}

// Lookup returns the syscall number for a name, and whether the table has it.
func (t *SSNTable) Lookup(name string) (uint16, bool) {
	if t == nil {
		return 0, false
	}
	ssn, ok := t.Numbers[name]
	return ssn, ok
}

// Complete reports whether every requested name resolved.
func (t *SSNTable) Complete() bool {
	if t == nil {
		return false
	}
	return len(t.Failed) == 0
}
