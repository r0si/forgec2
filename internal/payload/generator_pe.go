package payload

import (
	"bytes"
	"context"
	"crypto/rand"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// finalizePE applies the full per-build PE forensic randomization pipeline:
// artifact stripping, timestamp handling, section-name randomization and
// benign-import mimicry. Every step uses fresh crypto/rand material so two
// binaries built from the same config never share PE headers. Callers must
// run patchSelfCheckHash AFTER this so the integrity hash covers final bytes.
//
// A requested transformation that cannot be applied is an error: silently
// returning an unstripped or partially modified payload would violate the
// operator's selection.
func finalizePE(outPath string, cfg ImplantConfig, goarch string) error {
	_ = goarch // kept for call-site symmetry with the architecture-specific validators
	data, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("read PE: %w", err)
	}
	// Artifact stripping is unconditional. Timestamp mode only controls the
	// COFF timestamp, not whether the Rich header / debug directory survive:
	// "keep" used to skip stripping entirely, leaving Go build artifacts in
	// place under the false promise of only preserving the timestamp.
	origTS, err := stripPEArtifactsInPlace(data)
	if err != nil {
		return fmt.Errorf("strip PE artifacts: %w", err)
	}
	switch cfg.PETimestampMode {
	case "keep":
		ApplyTimestamp(data, origTS)
	case "random":
		ts, err := GenerateTimestamp(TSRandom, "")
		if err != nil {
			return fmt.Errorf("generate random PE timestamp: %w", err)
		}
		ApplyTimestamp(data, ts)
	}
	// Optional PE section name randomization
	if cfg.PESectionMode == "random" {
		cfgSec := PESectionConfig{
			Text:  "." + randomHex(3),
			Data:  "." + randomHex(3),
			Rdata: "." + randomHex(3),
			Reloc: "." + randomHex(3),
		}
		ApplyPESectionNames(data, cfgSec)
	}
	// Optional benign import mimic with per-build subset jitter: even in
	// kernel32+user32 mode we randomly drop to kernel32-only for some builds
	// so the import table is not a stable fingerprint.
	if cfg.PEImportMode != "" && cfg.PEImportMode != "none" {
		var dlls []string
		if cfg.PEImportMode == "kernel32" {
			dlls = []string{"kernel32.dll"}
		} else {
			dlls = []string{"kernel32.dll", "user32.dll"}
			if b := make([]byte, 1); func() bool { _, err := rand.Read(b); return err == nil }() {
				if b[0]%2 == 0 {
					dlls = []string{"kernel32.dll"}
				}
			}
		}
		out, err := AddBenignImports(data, dlls)
		if err != nil {
			return fmt.Errorf("apply benign PE imports: %w", err)
		}
		data = out
	}
	// #nosec G306 -- finalized PE artifacts are executables by contract.
	if err := os.WriteFile(outPath, data, 0755); err != nil { // #nosec G306
		return fmt.Errorf("write finalized PE: %w", err)
	}
	return nil
}

// stripPEArtifactsInPlace removes the COFF timestamp, the Rich header and the
// debug-directory entry from data. It returns the original timestamp so a
// caller implementing PETimestampMode=keep can restore just that field.
//
// The data-directory array starts after the optional header, i.e. at
// eLfanew+0x18+0x70 (PE32+) or eLfanew+0x18+0x60 (PE32). The previous code
// used eLfanew+0x70/eLfanew+0x60, which is 0x18 bytes early: index 6 (Debug)
// landed on index 3 (Exception) instead. On amd64 that zeroed the .pdata
// exception directory and left the debug directory untouched.
func stripPEArtifactsInPlace(data []byte) (uint32, error) {
	if len(data) < 0x100 {
		return 0, fmt.Errorf("PE image too small (%d bytes)", len(data))
	}
	if data[0] != 'M' || data[1] != 'Z' {
		return 0, fmt.Errorf("not a PE image (missing MZ header)")
	}
	eLfanew := int(binary.LittleEndian.Uint32(data[0x3C:0x40]))
	if eLfanew < 0 || eLfanew+24 > len(data) {
		return 0, fmt.Errorf("not a PE image (truncated DOS/COFF header)")
	}
	if string(data[eLfanew:eLfanew+4]) != "PE\x00\x00" {
		return 0, fmt.Errorf("not a PE image (missing PE signature)")
	}
	opt := eLfanew + 24
	if opt+2 > len(data) {
		return 0, fmt.Errorf("not a PE image (truncated optional header)")
	}
	magic := binary.LittleEndian.Uint16(data[opt:])
	var dataDirBase int
	switch magic {
	case 0x10b: // PE32
		dataDirBase = opt + 0x60
	case 0x20b: // PE32+
		dataDirBase = opt + 0x70
	default:
		return 0, fmt.Errorf("unsupported PE optional-header magic 0x%04x", magic)
	}
	// We only need entries 0..6; verifying the full 16-entry array would
	// reject otherwise valid minimal images that omit trailing entries.
	if dataDirBase+7*8 > len(data) {
		return 0, fmt.Errorf("not a PE image (truncated data directories)")
	}

	// 1. COFF timestamp at PE signature + 8.
	tsOffset := eLfanew + 8
	origTS := binary.LittleEndian.Uint32(data[tsOffset : tsOffset+4])
	binary.LittleEndian.PutUint32(data[tsOffset:], 0)

	// 2. Rich header (between the DOS stub and the PE signature). Go does not
	// emit one, but MSVC-built resources/linkers can.
	if eLfanew > 0x80 {
		for i := eLfanew - 4; i >= 0x80; i-- {
			if data[i] == 'R' && data[i+1] == 'i' && data[i+2] == 'c' && data[i+3] == 'h' {
				for j := i; j < eLfanew; j++ {
					data[j] = 0
				}
				break
			}
		}
	}

	// 3. Debug directory entry (data-directory index 6).
	debugDirOffset := dataDirBase + pe.IMAGE_DIRECTORY_ENTRY_DEBUG*8
	for i := 0; i < 8; i++ {
		data[debugDirOffset+i] = 0
	}
	return origTS, nil
}

// stripPEArtifacts is the path-level wrapper used by tests and callers that do
// not need to preserve the original timestamp.
func stripPEArtifacts(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := stripPEArtifactsInPlace(data); err != nil {
		return err
	}
	// #nosec G306 -- stripped PE artifacts are executables by contract.
	return os.WriteFile(path, data, 0755) // #nosec G306
}

// windowsMachine maps the requested architecture to the PE Machine field so we
// can assert the produced binary is the architecture the operator asked for.
func windowsMachine(arch string) uint16 {
	switch arch {
	case "arm64":
		return 0xAA64
	case "386":
		return 0x014c
	default:
		return 0x8664 // amd64
	}
}

// normalizeArch canonicalizes common architecture aliases to Go's GOARCH value.
func normalizeArch(arch string) string {
	arch = strings.ToLower(strings.TrimSpace(arch))
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	case "i386", "x86", "386":
		return "386"
	case "arm":
		return "arm"
	}
	return arch
}

// resolveBuildArch returns the GOARCH to build for the given OS/arch, or an
// error if that combination is unsupported. This makes architecture selection
// honest: an unsupported request fails the build loudly instead of silently
// producing a different (wrong) architecture.
func resolveBuildArch(goos, arch string) (string, error) {
	a := normalizeArch(arch)
	for _, s := range supportedArchByOS[goos] {
		if s == a {
			return a, nil
		}
	}
	return "", fmt.Errorf("architecture %q is not supported for %s implants; supported: %s",
		arch, goos, strings.Join(supportedArchByOS[goos], ", "))
}

// peExpect describes the artifact-specific invariants validatePEWith enforces.
// Zero values mean "do not require this property"; Machine is always checked.
type peExpect struct {
	Machine             uint16
	DLL                 bool
	Signed              bool
	RequireDebugCleared bool
	// Subsystem, when non-zero, must match the optional-header subsystem.
	Subsystem uint16
}

// validatePE confirms path is a well-formed PE for the expected machine type
// (0x8664 amd64, 0x014c 386, 0xAA64 arm64). A malformed or wrong-architecture
// binary must never be delivered to an operator — doing so produces a file
// Windows rejects with "This app can't run on your PC". Validating here turns
// any such regression (bad strip, broken ldflags, wrong GOARCH) into a clear
// build failure instead of a silently corrupted download.
func validatePE(path string, machine uint16) error {
	return validatePEWith(path, peExpect{Machine: machine})
}

// validatePEWith additionally checks the invariants that distinguish a usable
// EXE/DLL from a merely parseable header: DLL flag, subsystem, entry point,
// section bounds, checksum (for signed images), certificate-table placement
// and the stripped debug directory.
func validatePEWith(path string, exp peExpect) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("not a valid PE: %w", err)
	}
	defer f.Close()

	if f.Machine != exp.Machine {
		return fmt.Errorf("unexpected PE machine 0x%04x, wanted 0x%04x", f.Machine, exp.Machine)
	}
	if f.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 {
		return fmt.Errorf("PE is missing IMAGE_FILE_EXECUTABLE_IMAGE")
	}
	isDLL := f.Characteristics&pe.IMAGE_FILE_DLL != 0
	if isDLL != exp.DLL {
		return fmt.Errorf("PE DLL flag = %v, wanted %v", isDLL, exp.DLL)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fileSize := uint64(info.Size())

	var (
		magic           uint16
		entry           uint32
		sizeOfImage     uint32
		sizeOfHeaders   uint32
		subsystem       uint16
		checksum        uint32
		sectionAlign    uint32
		fileAlign       uint32
		dataDirectories []pe.DataDirectory
	)
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		magic = oh.Magic
		entry = oh.AddressOfEntryPoint
		sizeOfImage = oh.SizeOfImage
		sizeOfHeaders = oh.SizeOfHeaders
		subsystem = oh.Subsystem
		checksum = oh.CheckSum
		sectionAlign = oh.SectionAlignment
		fileAlign = oh.FileAlignment
		dataDirectories = oh.DataDirectory[:]
	case *pe.OptionalHeader64:
		magic = oh.Magic
		entry = oh.AddressOfEntryPoint
		sizeOfImage = oh.SizeOfImage
		sizeOfHeaders = oh.SizeOfHeaders
		subsystem = oh.Subsystem
		checksum = oh.CheckSum
		sectionAlign = oh.SectionAlignment
		fileAlign = oh.FileAlignment
		dataDirectories = oh.DataDirectory[:]
	default:
		return fmt.Errorf("unsupported PE optional header type %T", f.OptionalHeader)
	}

	switch exp.Machine {
	case 0x8664, 0xAA64:
		if magic != 0x20b {
			return fmt.Errorf("PE32+ machine 0x%04x has optional-header magic 0x%04x", exp.Machine, magic)
		}
	case 0x014c:
		if magic != 0x10b {
			return fmt.Errorf("PE32 machine 0x%04x has optional-header magic 0x%04x", exp.Machine, magic)
		}
	}
	if exp.Subsystem != 0 && subsystem != exp.Subsystem {
		return fmt.Errorf("PE subsystem = %d, wanted %d", subsystem, exp.Subsystem)
	}
	if len(f.Sections) == 0 {
		return fmt.Errorf("PE has no sections")
	}
	if sizeOfHeaders == 0 || uint64(sizeOfHeaders) > fileSize {
		return fmt.Errorf("PE SizeOfHeaders %d is outside the file (%d bytes)", sizeOfHeaders, fileSize)
	}
	if sizeOfImage == 0 {
		return fmt.Errorf("PE SizeOfImage is zero")
	}
	if sectionAlign == 0 || fileAlign == 0 {
		return fmt.Errorf("PE section/file alignment is zero")
	}
	if sizeOfImage%sectionAlign != 0 {
		return fmt.Errorf("PE SizeOfImage 0x%x is not section-aligned (0x%x)", sizeOfImage, sectionAlign)
	}
	if sizeOfHeaders%fileAlign != 0 {
		return fmt.Errorf("PE SizeOfHeaders 0x%x is not file-aligned (0x%x)", sizeOfHeaders, fileAlign)
	}

	type sectionRange struct {
		name string
		va   uint64
		end  uint64
	}
	var sectionRanges []sectionRange
	entryInSection := false
	imageEnd := uint64(sizeOfHeaders)
	for _, s := range f.Sections {
		rawStart := uint64(s.Offset)
		rawSize := uint64(s.Size)
		if rawSize > 0 {
			if rawStart > fileSize || rawSize > fileSize-rawStart {
				return fmt.Errorf("section %q raw data [0x%x,0x%x) is outside the file (%d bytes)",
					s.Name, rawStart, rawStart+rawSize, fileSize)
			}
			if rawStart+rawSize > imageEnd {
				imageEnd = rawStart + rawSize
			}
		}
		vsize := uint64(s.VirtualSize)
		if vsize < rawSize {
			vsize = rawSize
		}
		if uint64(s.VirtualAddress)+vsize > uint64(sizeOfImage) {
			return fmt.Errorf("section %q virtual range [0x%x,0x%x) exceeds SizeOfImage 0x%x",
				s.Name, s.VirtualAddress, uint64(s.VirtualAddress)+vsize, sizeOfImage)
		}
		// The loader maps whole section-alignment units. Two sections may not
		// occupy the same page even when their raw VirtualSize fields don't
		// overlap, or the second section's data silently replaces the first.
		mapped := (vsize + uint64(sectionAlign) - 1) / uint64(sectionAlign) * uint64(sectionAlign)
		sectionRanges = append(sectionRanges, sectionRange{
			name: s.Name,
			va:   uint64(s.VirtualAddress),
			end:  uint64(s.VirtualAddress) + mapped,
		})
		if entry != 0 && uint64(entry) >= uint64(s.VirtualAddress) &&
			uint64(entry) < uint64(s.VirtualAddress)+vsize {
			entryInSection = true
		}
	}
	sort.Slice(sectionRanges, func(i, j int) bool { return sectionRanges[i].va < sectionRanges[j].va })
	for i := 1; i < len(sectionRanges); i++ {
		prev, cur := sectionRanges[i-1], sectionRanges[i]
		if cur.va < prev.end {
			return fmt.Errorf("PE sections %q [0x%x,0x%x) and %q [0x%x,0x%x) overlap in virtual memory",
				prev.name, prev.va, prev.end, cur.name, cur.va, cur.end)
		}
	}
	if entry == 0 {
		if !exp.DLL {
			return fmt.Errorf("EXE has no entry point")
		}
	} else if !entryInSection {
		return fmt.Errorf("PE entry point 0x%x is outside all sections", entry)
	}
	if imageEnd > fileSize {
		return fmt.Errorf("PE image ends at 0x%x, past the file (%d bytes)", imageEnd, fileSize)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := validatePEImports(data); err != nil {
		return fmt.Errorf("PE import table: %w", err)
	}

	if len(dataDirectories) <= pe.IMAGE_DIRECTORY_ENTRY_SECURITY {
		return fmt.Errorf("PE has no security-directory slot")
	}
	security := dataDirectories[pe.IMAGE_DIRECTORY_ENTRY_SECURITY]
	if exp.Signed {
		if security.VirtualAddress == 0 || security.Size < 8 {
			return fmt.Errorf("signed PE has no certificate table (offset=%d size=%d)",
				security.VirtualAddress, security.Size)
		}
		if security.VirtualAddress%8 != 0 {
			return fmt.Errorf("certificate table offset 0x%x is not 8-byte aligned", security.VirtualAddress)
		}
		secEnd := uint64(security.VirtualAddress) + uint64(security.Size)
		if secEnd > fileSize {
			return fmt.Errorf("certificate table [0x%x,0x%x) is outside the file (%d bytes)",
				security.VirtualAddress, secEnd, fileSize)
		}
		if err := validateCertificateTable(data, security); err != nil {
			return err
		}
		checksumOff, _, err := peSecurityFields(data)
		if err != nil {
			return err
		}
		if got := computePEChecksum(data, checksumOff); got != checksum {
			return fmt.Errorf("signed PE checksum = 0x%08x, computed 0x%08x", checksum, got)
		}
		if err := VerifyPESignature(data); err != nil {
			return fmt.Errorf("signed PE failed signature verification: %w", err)
		}
	} else if security.VirtualAddress != 0 || security.Size != 0 {
		return fmt.Errorf("unsigned PE has a certificate table (offset=%d size=%d)",
			security.VirtualAddress, security.Size)
	}

	if exp.RequireDebugCleared && len(dataDirectories) > pe.IMAGE_DIRECTORY_ENTRY_DEBUG {
		debug := dataDirectories[pe.IMAGE_DIRECTORY_ENTRY_DEBUG]
		if debug.VirtualAddress != 0 || debug.Size != 0 {
			return fmt.Errorf("debug directory was not cleared (offset=%d size=%d)",
				debug.VirtualAddress, debug.Size)
		}
	}
	return nil
}

// validateCertificateTable walks the WIN_CERTIFICATE entries registered in the
// security directory. Bounds and alignment are checked before every read so a
// crafted or truncated table cannot panic the validator.
func validateCertificateTable(data []byte, security pe.DataDirectory) error {
	start := uint64(security.VirtualAddress)
	end := start + uint64(security.Size)
	if end > uint64(len(data)) {
		return fmt.Errorf("certificate table [0x%x,0x%x) is outside the file (%d bytes)",
			start, end, len(data))
	}
	for off := start; off < end; {
		if off+8 > end {
			return fmt.Errorf("certificate table has a truncated WIN_CERTIFICATE header at 0x%x", off)
		}
		length := uint64(binary.LittleEndian.Uint32(data[off : off+4]))
		revision := binary.LittleEndian.Uint16(data[off+4 : off+6])
		certType := binary.LittleEndian.Uint16(data[off+6 : off+8])
		if length < 8 || length > end-off {
			return fmt.Errorf("WIN_CERTIFICATE at 0x%x has invalid length %d", off, length)
		}
		if length%8 != 0 {
			return fmt.Errorf("WIN_CERTIFICATE at 0x%x is not 8-byte aligned (length %d)", off, length)
		}
		if revision != winCertRevisionV2 {
			return fmt.Errorf("WIN_CERTIFICATE at 0x%x has revision 0x%04x, wanted 0x%04x",
				off, revision, winCertRevisionV2)
		}
		if certType != winCertTypePKCSSignedData {
			return fmt.Errorf("WIN_CERTIFICATE at 0x%x has type 0x%04x, wanted 0x%04x",
				off, certType, winCertTypePKCSSignedData)
		}
		off += length
	}
	return nil
}

// maybeCompressUPX runs an optional UPX pass over a freshly built implant.
// It only acts when cfg.UPX is set and format is exe or elf (UPX Mach-O and
// c-shared DLL support is unreliable, so those are skipped silently).
// A missing upx binary is a skip, not an error — unlike garble, UPX is pure
// size optimization with no security promise attached. A failed UPX run
// restores the pre-compression bytes and fails loudly rather than shipping
// a possibly corrupt binary. The original permission bits are preserved on
// every restore path so a failed UPX pass cannot turn an ELF into a
// non-executable file.
func maybeCompressUPX(outPath string, cfg ImplantConfig, format string) error {
	if !cfg.UPX {
		return nil
	}
	if format != "exe" && format != "elf" {
		fmt.Printf("upx: skipping unsupported format %q (exe/elf only)\n", format)
		return nil
	}
	upxPath, err := exec.LookPath("upx")
	if err != nil {
		fmt.Printf("upx: binary not found in PATH, skipping compression (install UPX to enable)\n")
		return nil
	}
	orig, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("upx: read for backup: %w", err)
	}
	origMode := os.FileMode(0755)
	if info, statErr := os.Stat(outPath); statErr == nil {
		origMode = info.Mode().Perm()
	}
	restore := func() {
		_ = os.WriteFile(outPath, orig, origMode)
		_ = os.Chmod(outPath, origMode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, upxPath, "--lzma", "-q", outPath)
	cmd.Env = goModuleEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		restore()
		return fmt.Errorf("upx compression failed (original restored): %w\n%s", err, stderr.String())
	}
	compressed, err := os.ReadFile(outPath)
	if err != nil || len(compressed) == 0 || len(compressed) >= len(orig) {
		restore()
		if err == nil && len(compressed) >= len(orig) {
			fmt.Printf("upx: no size gain (%d -> %d bytes), keeping original\n", len(orig), len(compressed))
			return nil
		}
		return fmt.Errorf("upx: compressed output invalid, original restored")
	}
	fmt.Printf("upx: compressed %d -> %d bytes\n", len(orig), len(compressed))
	return nil
}
