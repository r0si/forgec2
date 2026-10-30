package main

import (
	"fmt"
	"os"
)

// ReadNtDllImage loads an ntdll image from disk so the pure parsers in this package can consume it.
//
// This is the disk-reading half of the direct-syscall layer, separated from the parsing half on purpose: reading a file is the only
// step that touches the environment, and keeping it apart means every byte the parsers ever see can be traced back to a file the
// operator can point at. Nothing here parses anything, and nothing here calls a Windows API - it opens a path, reads bytes, and
// refuses anything that is not a PE image, because feeding a truncated or wrong file to the SSN resolver is how a wrong syscall
// number gets issued on the target.
//
// Nothing in the agent calls this yet; the wiring decision (sleepMaskPolicyEnabled vs an independent syscallPolicy) is separate from
// the reader itself, and this file does not make it.
func ReadNtDllImage(path string) ([]byte, error) {
	if path == "" {
		return nil, &ErrNoNtDllImage{Path: path, Reason: "no path supplied"}
	}
	image, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrNoNtDllImage{Path: path, Reason: err.Error()}
	}
	if len(image) == 0 {
		return nil, &ErrNoNtDllImage{Path: path, Reason: "file is empty"}
	}
	if !isPeImage(image) {
		return nil, &ErrNoNtDllImage{Path: path, Reason: "file is not a PE image"}
	}
	return image, nil
}

// ErrNoNtDllImage reports that a file could not be used as the ntdll image.
type ErrNoNtDllImage struct {
	Path   string
	Reason string
}

func (e *ErrNoNtDllImage) Error() string {
	return fmt.Sprintf("cannot use %q as ntdll image: %s", e.Path, e.Reason)
}

// isPeImage reports whether the buffer looks like a PE32 or PE32+ image. It checks only the two signatures, because the parsers
// that consume the image perform their own deeper validation; this gate exists to refuse obvious non-images before they reach a
// resolver that would otherwise report a confusing "stub not found".
func isPeImage(image []byte) bool {
	if len(image) < dosLfanewOffset+4 {
		return false
	}
	// DOS signature: "MZ" (0x4D, 0x5A).
	if image[0] != 0x4D || image[1] != 0x5A {
		return false
	}
	lfanew := int(uint32(image[dosLfanewOffset]) |
		uint32(image[dosLfanewOffset+1])<<8 |
		uint32(image[dosLfanewOffset+2])<<16 |
		uint32(image[dosLfanewOffset+3])<<24)
	if lfanew <= 0 || lfanew+4 > len(image) {
		return false
	}
	// PE signature: "PE\0\0" (0x50, 0x45, 0x00, 0x00).
	return image[lfanew] == 0x50 &&
		image[lfanew+1] == 0x45 &&
		image[lfanew+2] == 0 &&
		image[lfanew+3] == 0
}
