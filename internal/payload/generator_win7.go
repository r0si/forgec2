package payload

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// win7Pins are go1.20-compatible dependency versions for legacy builds.
// Current pins require newer Go (fail fast with "requires go >=" otherwise),
// so the Win7 branch repins the whole module set. Slim is forced on (see
// NormalizeImplantConfig), which keeps grpc/quic/gorilla out entirely.
//
// Beyond the six direct modules this also pins the modernc transitive
// closure: without it MVS lifts e.g. klauspost/compress to the modern line
// (required by the parent module) and go1.20 fails compiling it.
func win7Pins() []string {
	return []string{
		"\tgolang.org/x/sys v0.19.0",
		"\tgolang.org/x/crypto v0.22.0",
		"\tgolang.org/x/net v0.24.0",
		"\tgolang.org/x/text v0.14.0",
		"\tgithub.com/refraction-networking/utls v1.5.4",
		// NOTE: no explicit quic-go pin even though utls imports its QUIC
		// transport-parameter helper: `go mod tidy` re-adds it as an
		// indirect requirement at the era version from utls's own go.mod,
		// while an explicit pin trips the slim no-quic gate.
		"\tgithub.com/Microsoft/go-winio v0.6.1",
		"\tmodernc.org/sqlite v1.21.0",
		// Windows TUN compiles only when the Wintun runtime has been staged
		// (scripts/fetch-wintun.*) and the build carries -tags wintun; the
		// pin keeps that build deterministic under go1.20 (module go 1.17).
		"\tgolang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2",
		"\tmodernc.org/libc v1.22.3",
		"\tmodernc.org/mathutil v1.5.0",
		"\tmodernc.org/memory v1.5.0",
		"\tgithub.com/klauspost/compress v1.15.9",
		"\tgithub.com/google/uuid v1.3.0",
		"\tgithub.com/dustin/go-humanize v1.0.0",
		"\tgithub.com/mattn/go-isatty v0.0.16",
		"\tlukechampine.com/uint128 v1.2.0",
		"\tgithub.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec",
		// pkg/protocol + pkg/encoding use cbor/msgpack; pin era lines or
		// MVS lifts them past go1.20 (same mechanism as klauspost above).
		"\tgithub.com/fxamacker/cbor/v2 v2.5.0",
		"\tgithub.com/vmihailenco/msgpack/v5 v5.3.5",
		"\tgithub.com/vmihailenco/tagparser/v2 v2.0.0",
		"\tgithub.com/x448/float16 v0.8.4",
	}
}

// materializeWin7Shim vendors the go1.20-clean parent packages
// (internal/crypto, pkg/protocol, pkg/encoding) from the embedded mirror
// into a stub module and repoints the agent go.mod's parent replace at it.
// Without this, go1.20 refuses to compile those packages out of the go-1.25
// main module ("cannot compile Go 1.25 code") even though the sources
// themselves are language-compatible (verified by isolated builds).
func materializeWin7Shim(buildDir string) error {
	stubRoot := filepath.Join(buildDir, win7ShimModulePath)
	for _, pkg := range win7ShimPkgs {
		entries, err := win7shimFS.ReadDir("win7shim/" + pkg.dir)
		if err != nil {
			return fmt.Errorf("win7shim mirror missing %s: run node scripts/sync-win7shim.mjs: %w", pkg.dir, err)
		}
		dstDir := filepath.Join(stubRoot, filepath.FromSlash(pkg.importPath))
		if err := os.MkdirAll(dstDir, 0o750); err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			data, err := win7shimFS.ReadFile("win7shim/" + pkg.dir + "/" + e.Name())
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dstDir, e.Name()), data, 0o600); err != nil {
				return err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(stubRoot, "go.mod"), []byte("module github.com/forgec2/forgec2\n\ngo 1.20\n"), 0o600); err != nil {
		return err
	}
	// Repoint the parent replace at the stub. forgeC2ModuleReplace emits a
	// single `replace github.com/forgec2/forgec2 => <repodir>` line.
	gomodPath := filepath.Join(buildDir, "go.mod")
	raw, err := os.ReadFile(gomodPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	rewrote := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "replace github.com/forgec2/forgec2 =>") {
			lines[i] = "replace github.com/forgec2/forgec2 => ./" + win7ShimModulePath
			rewrote = true
		}
	}
	if !rewrote {
		return fmt.Errorf("win7shim: parent replace line not found in generated go.mod")
	}
	return os.WriteFile(gomodPath, []byte(strings.Join(lines, "\n")), 0o600)
}

// buildGoModWin7 emits the legacy module file for Win7/2008R2 targets:
// go 1.20 directive plus fully repinned dependencies. Slim is forced on
// for Win7 (see NormalizeImplantConfig), so grpc/quic never appear here.
//
// The replace block is load-bearing, not cosmetic: the agent imports
// github.com/forgec2/forgec2/pkg/*, whose parent module requires MODERN
// dependency versions — without replaces, MVS would lift every pin back
// to the modern line and go1.20 would fail with "requires go >=".
func buildGoModWin7() string {
	replaceDir := forgeC2ModuleReplace()
	deps := append([]string{}, win7Pins()...)
	base := "module agent\n\ngo 1.20\n\nrequire (\n"
	base += strings.Join(deps, "\n") + "\n)\n"
	base += replaceDir
	for _, dep := range win7Pins() {
		fields := strings.Fields(dep)
		if len(fields) != 2 {
			continue
		}
		base += "replace " + fields[0] + " => " + fields[0] + " " + fields[1] + "\n"
	}
	return base
}

// appendUniqueModDep adds a require line unless the same module path is
// already present (non-slim builds already list gorilla/websocket and x/net).
func appendUniqueModDep(deps []string, line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return deps
	}
	for _, dep := range deps {
		df := strings.Fields(dep)
		if len(df) > 0 && df[0] == fields[0] {
			return deps
		}
	}
	return append(deps, line)
}

func buildGoMod(goos string, isDLL bool, slim bool, win7 bool, withWebRTC ...bool) string {
	replaceDir := forgeC2ModuleReplace()
	if win7 {
		return buildGoModWin7()
	}
	webrtc := len(withWebRTC) > 0 && withWebRTC[0]
	deps := []string{}
	deps = append(deps, "\tgolang.org/x/sys v0.46.0")
	deps = append(deps, "\tgolang.org/x/crypto v0.53.0")
	// Windows TUN support compiles only with the staged Wintun runtime and
	// -tags wintun (see wintunBuildTags); tie the version here so `go mod
	// tidy` in the temp build module cannot drift it across generations.
	deps = append(deps, "\tgolang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2")

	if !isDLL && !slim {
		deps = append(deps, "\tgolang.org/x/net v0.56.0")
		deps = append(deps, "\tgithub.com/gorilla/websocket v1.5.3")
		deps = append(deps, "\tgithub.com/quic-go/quic-go v0.54.1")
		deps = append(deps, "\tgithub.com/refraction-networking/utls v1.6.7")
	}

	if !isDLL && slim {
		// Slim keeps x/net (h2c/icmp transports) and utls (shared JA3
		// layer for HTTPS/DoT/mTLS); only the grpc/quic/wss stacks go.
		deps = append(deps, "\tgolang.org/x/net v0.56.0")
		deps = append(deps, "\tgithub.com/refraction-networking/utls v1.6.7")
	}

	if goos == "windows" {
		deps = append(deps, "\tgithub.com/Microsoft/go-winio v0.6.2")
		deps = append(deps, "\tmodernc.org/sqlite v1.52.0")
	}

	if goos == "windows" && !isDLL && !slim {
		deps = append(deps, "\tgoogle.golang.org/grpc v1.82.0")
		deps = append(deps, "\tnhooyr.io/websocket v1.8.17")
	}

	if webrtc {
		// The optional WebRTC module imports Pion and gorilla/websocket (the
		// long-lived signaling channel). Slim builds still need gorilla even
		// though transports_advanced is compiled out.
		deps = appendUniqueModDep(deps, "\tgithub.com/gorilla/websocket v1.5.3")
		deps = append(deps, "\tgithub.com/pion/webrtc/v4 v4.2.24")
		deps = appendUniqueModDep(deps, "\tgolang.org/x/net v0.56.0")
	}

	base := "module agent\n\ngo 1.25\n\nrequire (\n"
	base += strings.Join(deps, "\n") + "\n)\n"
	return base + replaceDir
}

// win7CompatConflict rejects option combinations the legacy toolchain
// cannot honor. Failing here beats shipping a subtly broken implant.
func win7CompatConflict(cfg *ImplantConfig) error {
	if !cfg.Win7Compat {
		return nil
	}
	if cfg.Obfuscate {
		return fmt.Errorf("win7compat forbids garble obfuscation (garble requires a modern Go toolchain)")
	}
	return nil
}

// slimTransportConflict rejects builds whose selected transport was compiled
// out. The legacy Slim flag and an explicit module selection that omits
// transports_advanced both drop grpc/quic/wss. This is also the module
// selection validator: unknown module names and unmet dependencies fail here,
// before any staging, instead of surfacing as broken builds.
func slimTransportConflict(cfg *ImplantConfig) error {
	plan, err := resolveAgentPlan(cfg)
	if err != nil {
		return err
	}
	if !plan.TransportsExcluded {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(cfg.BeaconTransport)) {
	case "grpc", "quic", "wss":
		return fmt.Errorf("build excludes the %q transport (slim/light profile, or agent module \"transports_advanced\" omitted): pick http/tcp/dns/icmp/ssh/mtls/h2c, re-enable the module or disable slim", cfg.BeaconTransport)
	}
	return nil
}
