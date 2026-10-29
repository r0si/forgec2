package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tetratelabs/wazero/api"
)

// Plugin package validation: this is the check half of the authoring loop
// (scaffold.go is the generate half). cmd/plugin-check is the CLI front end.
//
// The checks mirror what the server does on load, in the same order, with the
// same code (LoadManifest, Validate, VerifyPackageWithPolicy, the shared wasm
// runtime, the real executor for the optional run), so "check passes" and "the
// server accepts it" cannot drift apart. Every finding carries a stable code
// (manifest.invalid, wasm.bad_abi, result.bad_json, …) so agents can act on it
// without parsing prose: docs/PLUGIN_AUTHORING.md lists them all.

// Diagnostic levels, in severity order.
const (
	DiagError   = "error"
	DiagWarning = "warning"
	DiagInfo    = "info"
)

// Diagnostic is one finding. Code is stable across releases; Message is
// prose meant for humans and agents.
type Diagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CheckOptions configures one validation run.
type CheckOptions struct {
	// Dir is the plugin package directory.
	Dir string
	// Root is the tree root that anchors manifest.includes (defaults to Dir:
	// set it when the package uses a shared lib outside its own directory).
	Root string
	// Run executes the plugin once with a sample input after the static
	// checks. Only do this for packages you trust: native tiers are real
	// processes with no sandbox.
	Run bool
	// Input overrides the sample input with raw JSON.
	Input string
	// Timeout overrides the run timeout in seconds (0 = the manifest's).
	Timeout int
	// TrustedKeys are hex Ed25519 public keys for signature verification.
	TrustedKeys []string
	// RequireSigned mirrors plugins.require_signed: an unsigned package then
	// fails instead of warning.
	RequireSigned bool
}

// CheckReport is the machine-readable outcome (cmd/plugin-check -json).
type CheckReport struct {
	Dir         string       `json:"dir"`
	Name        string       `json:"name,omitempty"`
	Version     string       `json:"version,omitempty"`
	Type        string       `json:"type,omitempty"`
	Interpreter string       `json:"interpreter,omitempty"`
	Entry       string       `json:"entry,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Ran         bool         `json:"ran"`
	// Stdout/Stderr are the run's output, truncated for humans.
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Errors   int    `json:"errors"`
	Warnings int    `json:"warnings"`
	// SignatureKind says which of this product's four "signatures" a signing finding is about: the KEYED Ed25519 signature over
	// the package digest. Carried here because `verify-plugin -json` and the server's plugin failure report both describe
	// signing results, and "the signature did not verify" without saying which kind is how the unkeyed audit chain gets
	// mistaken for an authenticated artefact.
	SignatureKind string `json:"signature_kind"`
}

// SignatureKind is the description every plugin verdict carries.
const SignatureKind = "ed25519 signature over the plugin package digest: keyed, checked against plugins.trusted_keys"

// OK reports whether the package passed every error-level check.
func (r *CheckReport) OK() bool { return r.Errors == 0 }

func (r *CheckReport) add(level, code, format string, args ...interface{}) {
	switch level {
	case DiagError:
		r.Errors++
	case DiagWarning:
		r.Warnings++
	}
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Level: level, Code: code, Message: fmt.Sprintf(format, args...)})
}

// CheckPlugin validates a plugin package the way the server loads it and
// optionally runs it once. It never modifies the package.
func CheckPlugin(opts CheckOptions) *CheckReport {
	opts.Dir = strings.TrimSpace(opts.Dir)
	rep := &CheckReport{Dir: opts.Dir, SignatureKind: SignatureKind}
	if opts.Dir == "" {
		rep.add(DiagError, "package.no_dir", "no package directory given")
		return rep
	}
	info, err := os.Stat(opts.Dir)
	if err != nil || !info.IsDir() {
		rep.add(DiagError, "package.missing_dir", "cannot read directory %s", opts.Dir)
		return rep
	}
	if opts.Root == "" {
		opts.Root = opts.Dir
	}

	manifestPath := filepath.Join(opts.Dir, "manifest.yaml")
	if _, err := os.Stat(manifestPath); err != nil {
		rep.add(DiagError, "package.manifest_missing",
			"%s has no manifest.yaml; every plugin package needs one (generate a valid one with: go run ./cmd/plugin-init -name <name> -type command -interpreter python -dir %s)",
			opts.Dir, opts.Dir)
		return rep
	}
	m, err := LoadManifest(manifestPath)
	if err != nil {
		rep.add(DiagError, "manifest.unreadable", "%v", err)
		return rep
	}
	if err := m.Validate(); err != nil {
		rep.add(DiagError, "manifest.invalid", "%v", err)
		return rep
	}
	rep.Name, rep.Version = m.Name, m.Version
	rep.Type, rep.Interpreter, rep.Entry = m.Type, m.Interpreter, m.Entry
	rep.add(DiagInfo, "manifest.valid", "manifest parses and validates: name=%s version=%s type=%s interpreter=%s entry=%s",
		m.Name, m.Version, m.Type, m.Interpreter, m.Entry)
	if m.Type == "hook" && len(m.Events) == 0 {
		rep.add(DiagWarning, "manifest.no_events",
			"a hook plugin with an empty `events` list never fires; subscribe to at least one event")
	}

	entryPath := filepath.Join(opts.Dir, filepath.FromSlash(m.Entry))
	if isWasmInterpreter(m.Interpreter) {
		checkWasmPackage(rep, entryPath, m)
	} else {
		checkNativeEntry(rep, entryPath, m)
	}
	checkPackageTrust(rep, opts, m)

	if !opts.Run {
		rep.add(DiagInfo, "run.skipped", "static checks only; run `go run ./cmd/plugin-check %s -run` to execute the plugin with a sample input", opts.Dir)
		return rep
	}
	runCheck(rep, opts, m)
	return rep
}

// checkNativeEntry verifies the entry file exists and the interpreter is
// reachable here (the server needs it reachable on its own host).
func checkNativeEntry(rep *CheckReport, entryPath string, m *Manifest) {
	args := interpreterArgs(m.Interpreter, m.Entry)
	if _, err := os.Stat(entryPath); err != nil {
		rep.add(DiagError, "package.entry_missing",
			"entry %s does not exist; the server would run: %s", m.Entry, strings.Join(args, " "))
		return
	}
	rep.add(DiagInfo, "package.entry_ok", "entry %s found; the server runs: %s", m.Entry, strings.Join(args, " "))
	if probeErr := probeInterpreter(m.Interpreter); probeErr != nil {
		if _, lookErr := exec.LookPath(args[0]); lookErr != nil {
			rep.add(DiagWarning, "tier.interpreter_missing",
				"%s is not on PATH here; install it to run this plugin locally (the server needs it on the host that runs the plugin)", args[0])
		} else {
			rep.add(DiagError, "tier.interpreter_broken",
				"%v; the manifest names interpreter %q, which must run on the server host (on Windows the WindowsApps python.exe alias resolves but exits 9009 — install Python or use python3)", probeErr, m.Interpreter)
		}
	}
}

// probeInterpreter runs the cheapest command that proves an interpreter
// executes and exits 0. LookPath alone is not enough: on Windows the
// WindowsApps "python.exe" alias resolves while exiting 9009, and a manifest
// naming that alias produces a plugin that can never run.
func probeInterpreter(interpreter string) error {
	args := interpreterProbeArgs(interpreter)
	if len(args) == 0 {
		_, err := exec.LookPath(interpreter)
		return err
	}
	if _, err := exec.LookPath(args[0]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		if tail := truncateForReport(string(out), 200); tail != "" {
			return fmt.Errorf("%s does not run (%v): %s", args[0], err, tail)
		}
		return fmt.Errorf("%s does not run (%v)", args[0], err)
	}
	return nil
}

// interpreterProbeArgs returns the probe command for an interpreter, or nil
// when a probe would not prove anything (LookPath is then the only check).
func interpreterProbeArgs(interpreter string) []string {
	switch {
	case strings.HasPrefix(interpreter, "python"):
		return []string{interpreter, "-c", "pass"}
	case interpreter == "py":
		return []string{"py", "-3", "-c", "pass"}
	case interpreter == "bash" || interpreter == "sh":
		return []string{interpreter, "-c", "true"}
	case interpreter == "powershell" || interpreter == "pwsh":
		return []string{interpreter, "-NoProfile", "-NonInteractive", "-Command", "exit 0"}
	case interpreter == "go":
		return []string{"go", "version"}
	}
	return nil
}

// checkWasmPackage inspects the module structurally with the same runtime and
// limits the server uses, so ABI mistakes are caught before install.
func checkWasmPackage(rep *CheckReport, entryPath string, m *Manifest) {
	info, err := os.Stat(entryPath)
	if err != nil {
		rep.add(DiagError, "package.entry_missing",
			"entry %s does not exist; build it first (e.g. `sh %s/build.sh`), then re-run the check",
			m.Entry, filepath.ToSlash(filepath.Dir(filepath.FromSlash(entryPath))))
		return
	}
	if info.Size() > maxWasmBinaryBytes {
		rep.add(DiagError, "wasm.too_large", "module is %d bytes; the host refuses anything over %d", info.Size(), maxWasmBinaryBytes)
		return
	}
	binary, err := os.ReadFile(entryPath)
	if err != nil {
		rep.add(DiagError, "wasm.unreadable", "cannot read %s: %v", m.Entry, err)
		return
	}
	runner, err := getWasmRunner()
	if err != nil {
		rep.add(DiagError, "wasm.runtime", "cannot start the wasm runtime: %v", err)
		return
	}
	compiled, err := runner.compile(context.Background(), entryPath, binary)
	if err != nil {
		rep.add(DiagError, "wasm.invalid_module", "%v", err)
		return
	}

	for _, imp := range compiled.ImportedFunctions() {
		mod, name, _ := imp.Import()
		if mod == "forgec2" && name == "log" {
			if got := wasmSignature(imp); got != "(i32,i32) -> ()" {
				rep.add(DiagError, "wasm.bad_log_abi",
					"forgec2.log must be (i32,i32) -> (); the module imports %s", got)
			}
			continue
		}
		rep.add(DiagError, "wasm.unexpected_import",
			"the module imports %s.%s; ABI v1 allows only forgec2.log (no WASI: no files, sockets, environment or clock)", mod, name)
	}
	if len(compiled.ImportedMemories()) > 0 {
		rep.add(DiagError, "wasm.unexpected_import",
			"the module imports memory; ABI v1 requires the module to define and export its own memory")
	}

	runDef, ok := compiled.ExportedFunctions()["run"]
	if !ok {
		rep.add(DiagError, "wasm.missing_run", "the module does not export run; ABI v1 needs `run(ptr, len) -> i64`")
	} else if got := wasmSignature(runDef); got != "(i32,i32) -> (i64)" {
		rep.add(DiagError, "wasm.bad_abi",
			"run must be (i32,i32) -> (i64) — pack the reply pointer and length as ptr | len<<32; the module exports %s", got)
	}
	if _, ok := compiled.ExportedMemories()["memory"]; !ok {
		rep.add(DiagError, "wasm.missing_memory", "the module does not export `memory`; the host writes the input into linear memory and reads the reply back")
	}
	for _, mem := range compiled.ExportedMemories() {
		if mem.Min() > wasmMemoryPages {
			rep.add(DiagError, "wasm.memory_too_large",
				"the module requires at least %d pages but the host caps linear memory at %d", mem.Min(), wasmMemoryPages)
		}
	}
	rep.add(DiagInfo, "wasm.exports_ok", "module exports the ABI v1 surface (run(ptr,len)->i64, memory)")
}

// wasmSignature renders a function definition as "(i32,i32) -> (i64)".
func wasmSignature(def api.FunctionDefinition) string {
	names := func(types []api.ValueType) string {
		out := make([]string, 0, len(types))
		for _, t := range types {
			out = append(out, api.ValueTypeName(t))
		}
		return strings.Join(out, ",")
	}
	return fmt.Sprintf("(%s) -> (%s)", names(def.ParamTypes()), names(def.ResultTypes()))
}

// checkPackageTrust runs the server's package verification (digest, signature)
// against the configured policy.
func checkPackageTrust(rep *CheckReport, opts CheckOptions, m *Manifest) {
	policy := VerificationPolicy(nil, opts.RequireSigned)
	keysConfigured := len(opts.TrustedKeys) > 0
	if keysConfigured {
		keys, err := ParseTrustedPluginKeys(opts.TrustedKeys)
		if err != nil {
			rep.add(DiagError, "trust.bad_key", "%v", err)
			return
		}
		policy = VerificationPolicy(keys, opts.RequireSigned)
	}
	// A signed package checked without keys is not a package problem: the
	// checker simply cannot verify the signature. Report a warning instead of
	// asking the server's verifier, which treats "signed but no trusted keys"
	// as an error because the server cannot afford to guess.
	if strings.TrimSpace(m.Signature) != "" && !keysConfigured {
		rep.add(DiagWarning, "trust.unverified",
			"the package carries a signature but no -key was given; pass the signer's hex public key to verify it (the server needs it in plugins.trusted_keys)")
		return
	}
	status, err := VerifyPackageWithPolicy(opts.Dir, opts.Root, m, policy)
	switch {
	case err != nil:
		rep.add(DiagError, "trust.failed", "%v", err)
	case status.Verified:
		rep.add(DiagInfo, "trust.verified", "package digest matches and the signature verifies against a trusted key")
	case status.Unsigned:
		rep.add(DiagWarning, "trust.unsigned",
			"package has no digest: the server accepts it only while plugins.require_signed is off; stamp it with `PLUGIN_SIGNING_KEY=<seed> go run ./cmd/sign-plugin %s` (generate a key with -gen)", opts.Dir)
	case status.Signed:
		rep.add(DiagWarning, "trust.unverified", "signature present but not verified (%s); pass -key <hex pubkey> to verify it", status.Detail)
	default:
		rep.add(DiagWarning, "trust.unverified", "%s", status.Detail)
	}
}

// runCheck executes the package once through the real executor with a sample
// input and validates the output contract for its type.
func runCheck(rep *CheckReport, opts CheckOptions, m *Manifest) {
	input, err := checkRunInput(opts, m)
	if err != nil {
		rep.add(DiagError, "run.bad_input", "%v", err)
		return
	}
	timeout := m.DefaultTimeout()
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	e := &executor{
		cpuQuota:   defaultPluginCPUQuota,
		writeQuota: defaultPluginWriteQuota,
	}
	res, runErr := e.run(context.Background(), opts.Dir, m, input, timeout)
	rep.Ran = true
	if res != nil {
		rep.Stdout = truncateForReport(string(res.Stdout), 4000)
		rep.Stderr = truncateForReport(string(res.Stderr), 4000)
	}
	if runErr != nil {
		rep.add(DiagError, "run.failed", "%v", runErr)
		if rep.Stderr != "" {
			rep.add(DiagInfo, "run.output", "stderr: %s", truncateForReport(rep.Stderr, 1000))
		}
		return
	}
	rep.add(DiagInfo, "run.completed", "the plugin ran and exited 0 within %ds", timeout)
	if rep.Stderr != "" {
		rep.add(DiagInfo, "run.output", "stderr: %s", truncateForReport(rep.Stderr, 1000))
	}
	validateCheckOutput(rep, m, res)
}

// validateCheckOutput applies the output contract of the plugin type.
func validateCheckOutput(rep *CheckReport, m *Manifest, res *execResult) {
	if res == nil {
		return
	}
	stdout := bytes.TrimSpace(res.Stdout)
	if len(stdout) == 0 {
		if m.Type == "hook" {
			rep.add(DiagInfo, "result.empty_ok", "no output: hooks may print nothing")
			return
		}
		rep.add(DiagWarning, "result.empty_output",
			"the plugin printed nothing; the server needs one JSON object on stdout (a WASM module that returns 0 means the same)")
		return
	}
	switch m.Type {
	case "hook":
		if _, err := parseResult(stdout); err != nil {
			rep.add(DiagError, "result.bad_json", "%v; first bytes: %s", err, truncateForReport(string(stdout), 200))
			return
		}
		rep.add(DiagInfo, "result.ok", "hook output parses as a Result object")
	case "report":
		report, err := parseReport(stdout)
		if err != nil {
			rep.add(DiagError, "result.bad_json",
				"%v (a Report is {\"title\", \"format\", \"content\"} and content is base64 of the bytes); first bytes: %s",
				err, truncateForReport(string(stdout), 200))
			return
		}
		if strings.TrimSpace(report.Title) == "" {
			rep.add(DiagWarning, "result.empty_title", "the report has no title")
		}
		if len(report.Content) == 0 {
			rep.add(DiagWarning, "result.empty_report", "the report content is empty")
			return
		}
		rep.add(DiagInfo, "result.ok", "report parses: format=%s content=%d bytes", report.Format, len(report.Content))
	default:
		r, err := parseResult(stdout)
		if err != nil {
			rep.add(DiagError, "result.bad_json",
				"%v (a Result is {\"success\", \"output\", \"data\"}); first bytes: %s",
				err, truncateForReport(string(stdout), 200))
			return
		}
		if !r.Success {
			if r.Error != "" {
				rep.add(DiagWarning, "result.unsuccessful", "the plugin reported success=false: %s", truncateForReport(r.Error, 200))
			} else {
				rep.add(DiagWarning, "result.unsuccessful", "the plugin reported success=false without an error message")
			}
			return
		}
		rep.add(DiagInfo, "result.ok", "command output parses as a successful Result")
	}
}

// checkRunInput builds the sample payload the server would send: manifest
// parameter defaults (typed where the manifest declares a type), the
// manifest's default config, and a synthetic event for hooks.
func checkRunInput(opts CheckOptions, m *Manifest) (map[string]interface{}, error) {
	if raw := strings.TrimSpace(opts.Input); raw != "" {
		var in map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			return nil, fmt.Errorf("-input is not a JSON object: %w", err)
		}
		return in, nil
	}
	config := m.Config
	if config == nil {
		config = map[string]interface{}{}
	}
	params := sampleCheckParams(m)
	switch m.Type {
	case "hook":
		eventType := "plugin.check"
		if len(m.Events) > 0 {
			eventType = m.Events[0]
		}
		return map[string]interface{}{
			"event": map[string]interface{}{
				"type":      eventType,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
				"payload":   map[string]interface{}{"source": "plugin-check"},
			},
			"config": config,
		}, nil
	case "report":
		return map[string]interface{}{"params": params, "config": config}, nil
	default:
		return map[string]interface{}{"agent_id": "", "params": params, "config": config}, nil
	}
}

// sampleCheckParams fills every manifest parameter with its declared default,
// typed by the declared parameter type, so the sample run exercises the same
// path an operator's form submission would.
func sampleCheckParams(m *Manifest) map[string]interface{} {
	out := map[string]interface{}{}
	for _, p := range m.Params {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		switch p.Type {
		case "int":
			if v, err := strconv.Atoi(strings.TrimSpace(p.Default)); err == nil {
				out[p.Name] = v
				continue
			}
		case "number":
			if v, err := strconv.ParseFloat(strings.TrimSpace(p.Default), 64); err == nil {
				out[p.Name] = v
				continue
			}
		case "bool":
			if v, err := strconv.ParseBool(strings.TrimSpace(p.Default)); err == nil {
				out[p.Name] = v
				continue
			}
		}
		if p.Default != "" {
			out[p.Name] = p.Default
		}
	}
	return out
}

// truncateForReport trims a diagnostic payload to n bytes.
func truncateForReport(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "… (truncated)"
}
