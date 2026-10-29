package main

import "fmt"

// ResolveNtSyscalls composes the two pure halves: export resolution and stub parsing.
//
// Keeping the composition separate from the callers has two benefits. It is the piece the two parsers were written for, so it gets its
// own test rather than being debugged later inside a Windows-only path; and the eventual wiring becomes one call, which is the only
// place a policy switch has to be consulted.
//
// It still touches nothing: no file read, no process, no Windows API. The image is a parameter, so this is exercised by ordinary tests.
func ResolveNtSyscalls(image []byte, functionNames []string) (*SSNTable, error) {
	if len(image) == 0 {
		return nil, fmt.Errorf("an empty image cannot be parsed")
	}
	if len(functionNames) == 0 {
		return nil, fmt.Errorf("no function names requested")
	}

	// The two parsers speak different units: export resolution produces an RVA, stub parsing reads at a FILE OFFSET. Translating
	// here, once, is what keeps a unit mistake from looking like "no stub found" on the target.
	offsets := make(map[string]uint32, len(functionNames))
	failures := make(map[string]string)
	for _, name := range functionNames {
		rva, err := FindExportRVA(image, name)
		if err != nil {
			// Kept, not discarded: "this ntdll does not export it" and "this ntdll does not exist" are different answers, and a
			// table that silently dropped them would make a missing function look like one nobody asked for.
			failures[name] = err.Error()
			continue
		}
		offset, offsetErr := FileOffsetForRVA(image, rva)
		if offsetErr != nil {
			failures[name] = offsetErr.Error()
			continue
		}
		offsets[name] = uint32(offset)
	}

	table := NewSSNTable("", image, offsets)
	for name, reason := range failures {
		table.Failed[name] = reason
	}
	return table, nil
}

// RequiredSyscalls is the set the agent would need if it ever issues indirect syscalls.
//
// Named here rather than in the wiring so the list can be reviewed for what it does NOT include: every entry is a memory or thread
// primitive, and nothing that reads or writes operator data appears, because a syscall layer is not a reason to widen what a task can
// do.
var RequiredSyscalls = []string{
	"NtAllocateVirtualMemory",
	"NtProtectVirtualMemory",
	"NtWriteVirtualMemory",
	"NtCreateThreadEx",
	"NtClose",
}

// ResolveRequiredSyscalls resolves the set above, returning the table and whether every name was found.
func ResolveRequiredSyscalls(image []byte) (*SSNTable, bool, error) {
	table, err := ResolveNtSyscalls(image, RequiredSyscalls)
	if err != nil {
		return nil, false, err
	}
	return table, table.Complete(), nil
}
