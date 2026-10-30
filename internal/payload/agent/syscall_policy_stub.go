//go:build !windows || arm64

package main

// initSyscallPolicy is a no-op off windows (and on arm64 windows where the
// syscall stubs are not compiled): there is no ntdll to read and no indirect
// syscall path to wire.
func initSyscallPolicy() error {
	return nil
}
