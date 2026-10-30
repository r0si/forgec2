package payload

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CheckGoToolchain reports whether a usable Go command is discoverable. It is
// intentionally separate from ValidateImplantBuild so unit tests can validate
// request semantics on hosts without a Go installation.
func CheckGoToolchain() error {
	if getGoCmd() == "" {
		return fmt.Errorf("go executable not found in PATH. Install Go from https://go.dev/dl/ or set the GO_BINARY environment variable")
	}
	return nil
}

// ValidateImplantBuild performs the cheap, request-level checks that must run
// before a build is queued. Keeping these checks here (instead of only in the
// individual generator functions) prevents a build from minting a one-shot
// registration secret, staging a full agent tree, and running module resolution
// only to fail on an unsupported architecture or an option conflict.
//
// format is the requested artifact format ("exe", "dll", "elf", "macho", ...).
func ValidateImplantBuild(goos, format string, cfg *ImplantConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil implant config")
	}
	// Fail fast if the Go toolchain itself is unavailable: better to return a
	// 400 immediately than to queue a build that will fail after minutes in the
	// queue.
	if err := CheckGoToolchain(); err != nil {
		return err
	}
	switch goos {
	case "windows", "linux", "darwin":
	default:
		return fmt.Errorf("unsupported target OS %q", goos)
	}
	if _, err := resolveBuildArch(goos, cfg.Architecture); err != nil {
		return err
	}
	if cfg.Win7Compat && goos != "windows" {
		return fmt.Errorf("win7compat is only supported for Windows targets")
	}
	if err := win7CompatConflict(cfg); err != nil {
		return err
	}
	if err := slimTransportConflict(cfg); err != nil {
		return err
	}
	// A self-integrity hash is patched into the final file by locating a raw
	// placeholder. UPX compresses that placeholder, so the patch can no longer
	// be applied after compression. Reject the combination up front instead of
	// failing after an expensive compile.
	if cfg.SelfCheck && cfg.UPX && (format == "exe" || format == "elf") {
		return fmt.Errorf("self_check and upx cannot be combined: the self-check placeholder is compressed by UPX and cannot be patched; disable one of the options")
	}
	// Fail fast instead of silently publishing an uncompressed artifact or
	// ignoring a checkbox: the generator only applies UPX to Windows EXE and
	// Linux ELF, and an absent upx.exe must not make the request look successful.
	if cfg.UPX {
		if format != "exe" && format != "elf" {
			return fmt.Errorf("upx compression is only supported for Windows EXE and Linux ELF builds (requested %s)", format)
		}
		if _, err := exec.LookPath("upx"); err != nil {
			return fmt.Errorf("upx compression requested but the upx executable was not found in PATH; install UPX or disable compression")
		}
	}
	if cfg.Obfuscate {
		if format == "dll" {
			return fmt.Errorf("garble obfuscation is not supported with -buildmode=c-shared; disable obfuscation for DLL builds")
		}
		if getGarbleCmd() == "" {
			return fmt.Errorf("obfuscation requested but garble is not installed: install it with `go install mvdan.cc/garble@latest`")
		}
	}
	// Self-check resolves os.Executable() at runtime. In a DLL that is the host
	// process (rundll32.exe, the loader, ...), not the DLL, so the digest can
	// never match — and os.Exit(1) would kill the host instead of failing the
	// implant. Reject the combination rather than shipping a DLL that cannot
	// work.
	if cfg.SelfCheck && format == "dll" {
		return fmt.Errorf("self_check is not supported for DLL builds: os.Executable() resolves to the host process, not the DLL")
	}
	if cfg.CodeSigningEnabled() {
		switch strings.ToLower(strings.TrimSpace(cfg.SignMode)) {
		case "self", "pfx", "pem":
		default:
			return fmt.Errorf("unsupported sign_mode %q (use self, pfx or pem)", cfg.SignMode)
		}
		if goos != "windows" {
			return fmt.Errorf("authenticode signing is only supported for Windows targets")
		}
		if format != "exe" && format != "dll" {
			return fmt.Errorf("authenticode signing is only supported for Windows EXE and DLL builds (requested %s)", format)
		}
		if err := validateSigningMaterial(*cfg); err != nil {
			return err
		}
	}
	return nil
}

// validateELF confirms path is a structurally valid little-endian ELF for the
// requested Go architecture. Linux builds previously only checked that the
// output file existed; a corrupt or wrong-architecture artifact must fail the
// build before it is offered for download.
func validateELF(path, goarch string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 0x34 {
		return fmt.Errorf("truncated ELF header")
	}
	if string(data[:4]) != "\x7fELF" {
		return fmt.Errorf("not an ELF file (missing ELF magic)")
	}
	class := data[4]
	if class != 1 && class != 2 {
		return fmt.Errorf("invalid ELF class %d", class)
	}
	if data[5] != 1 {
		return fmt.Errorf("unsupported ELF byte order %d (only little-endian is produced)", data[5])
	}
	if data[6] != 1 {
		return fmt.Errorf("unsupported ELF version %d", data[6])
	}

	var wantClass byte
	switch normalizeArch(goarch) {
	case "arm":
		wantClass = 1
	case "amd64", "arm64":
		wantClass = 2
	default:
		return fmt.Errorf("no ELF validator for architecture %q", goarch)
	}
	if class != wantClass {
		return fmt.Errorf("unexpected ELF class %d for %s (wanted %d)", class, goarch, wantClass)
	}

	eType := binary.LittleEndian.Uint16(data[16:18])
	if eType != 2 && eType != 3 { // ET_EXEC or ET_DYN
		return fmt.Errorf("unexpected ELF type %d (wanted executable or PIE)", eType)
	}
	machine := binary.LittleEndian.Uint16(data[18:20])
	wantMachine := uint16(0)
	switch normalizeArch(goarch) {
	case "amd64":
		wantMachine = 62 // EM_X86_64
	case "arm64":
		wantMachine = 183 // EM_AARCH64
	case "arm":
		wantMachine = 40 // EM_ARM
	}
	if machine != wantMachine {
		return fmt.Errorf("unexpected ELF machine %d for %s (wanted %d)", machine, goarch, wantMachine)
	}

	headerSize := 52
	if class == 2 {
		headerSize = 64
	}
	if len(data) < headerSize {
		return fmt.Errorf("truncated ELF header (need %d bytes, have %d)", headerSize, len(data))
	}
	return nil
}

// validateMachO performs the same output check for macOS builds. Go emits a
// thin little-endian Mach-O for both amd64 and arm64; fat binaries are not
// produced by the generator.
func validateMachO(path, goarch string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 32 {
		return fmt.Errorf("truncated Mach-O header")
	}
	magic := binary.LittleEndian.Uint32(data[:4])
	if magic != 0xfeedface && magic != 0xfeedfacf {
		// A fat binary starts with 0xcafebabe in big-endian form. The
		// generator does not create one, but accept it as a valid artifact
		// rather than rejecting an otherwise usable cross-build.
		if binary.BigEndian.Uint32(data[:4]) != 0xcafebabe {
			return fmt.Errorf("not a Mach-O file (magic 0x%08x)", magic)
		}
		return nil
	}
	cputype := binary.LittleEndian.Uint32(data[4:8])
	want := uint32(0)
	switch normalizeArch(goarch) {
	case "amd64":
		want = 0x01000007 // CPU_TYPE_X86_64
	case "arm64":
		want = 0x0100000c // CPU_TYPE_ARM64
	default:
		return fmt.Errorf("no Mach-O validator for architecture %q", goarch)
	}
	if cputype != want {
		return fmt.Errorf("unexpected Mach-O CPU type 0x%08x for %s (wanted 0x%08x)", cputype, goarch, want)
	}
	return nil
}

// prepareBuildOutput returns the final and temporary output paths for an
// atomic build. The toolchain writes to tempPath; after post-processing and
// validation, commitBuildOutput renames it into place. This prevents a
// partially-written binary from being served if a build is interrupted.
func prepareBuildOutput(outputDir, outName string) (finalPath, tempPath string, err error) {
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return "", "", err
	}
	finalPath = filepath.Join(outputDir, outName)
	if !filepath.IsAbs(finalPath) {
		if abs, absErr := filepath.Abs(finalPath); absErr == nil {
			finalPath = abs
		}
	}
	ext := filepath.Ext(finalPath)
	base := strings.TrimSuffix(finalPath, ext)
	tempPath = base + ".tmp-" + randomHex(4) + ext
	_ = os.Remove(tempPath)
	return finalPath, tempPath, nil
}

// commitBuildOutput atomically publishes a successful build. The existing
// file is removed first because Windows os.Rename does not replace an existing
// destination.
func commitBuildOutput(tempPath, finalPath string, mode os.FileMode) error {
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return err
	}
	if mode != 0 {
		if err := os.Chmod(finalPath, mode); err != nil && runtime.GOOS != "windows" {
			_ = os.Remove(finalPath)
			return err
		}
	}
	return nil
}

// uniqueBuildName adds a short random suffix to the built-in default names so
// two builds with an empty operator-supplied filename can never write to the
// same path. Explicit filenames are already prefixed by the server; this is the
// direct-generator fallback used by tests and embedders.
func uniqueBuildName(prefix, ext string) string {
	return fmt.Sprintf("%s_%s%s", prefix, randomHex(4), ext)
}
