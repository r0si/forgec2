package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// WASM plugin tier (untrusted code).
//
// Native plugins (python/powershell/bash/go) run as real child processes: they
// inherit the server account's reach, whatever the manifest claims. The WASM
// tier exists for code you do NOT trust to run natively:
//
//   - no WASI: the module cannot open files, sockets, environment or clocks
//   - no imports except the tiny `forgec2` host module below
//   - linear memory capped (default 16 MiB)
//   - wall-clock timeout enforced by the runtime
//   - stdout equivalent capped (log lines + result size)
//
// ABI v1 (deliberately tiny so it is implementable from C/Rust/TinyGo without
// a host SDK):
//
//	exports:
//	  memory              linear memory
//	  run(ptr, len) -> i64
//	                      executes the plugin. ptr/len address the input JSON
//	                      the host wrote. The i64 result locates the JSON the
//	                      module produced, anywhere in its own memory:
//	                      low 32 bits = pointer, high 32 bits = length.
//	imports (module "forgec2"):
//	  log(ptr, len)       append one diagnostic line (capped, length-prefixed)
//
// The result is packed into one i64 because multi-value *results* are not what
// mainstream C/Rust ABIs emit: clang lowers a two-field struct return to an
// indirect (sret) parameter, so a `(result i32 i32)` entry point could only be
// written by hand or by TinyGo. One i64 packs into any language in one line.
//
// The returned pointer/length is never trusted blindly: it is clamped to the
// module's memory bounds and to maxWasmResultBytes, and a module may return 0
// to mean "no result".
const (
	// wasmInputOffset is the floor of the host's input buffer. The input is
	// written past the module's initial linear memory (see runWasmPlugin),
	// never below this address, so even a zero-page module keeps its first
	// KiB free for scratch/globals.
	wasmInputOffset = 1024
	// maxWasmResultBytes bounds the returned result.
	maxWasmResultBytes = 2 << 20 // 2 MiB, matching the native stdout cap
	// maxWasmLogBytes bounds total log output per run.
	maxWasmLogBytes = 256 << 10 // 256 KiB
	// wasmMemoryPages is the linear-memory cap (16 MiB).
	wasmMemoryPages = 256
	// maxWasmBinaryBytes bounds the module size we are willing to compile.
	maxWasmBinaryBytes = 16 << 20 // 16 MiB
)

// wasmRunner owns a shared wazero runtime plus a compiled-module cache so a
// report that runs on every page view does not recompile the module each time.
type wasmRunner struct {
	mu      sync.Mutex
	runtime wazero.Runtime
	cache   map[string]wazero.CompiledModule
	hostMod api.Module
}

var (
	sharedWasmOnce sync.Once
	sharedWasm     *wasmRunner
)

// getWasmRunner returns the process-wide runner, initializing it on first use.
func getWasmRunner() (*wasmRunner, error) {
	var initErr error
	sharedWasmOnce.Do(func() {
		sharedWasm, initErr = newWasmRunner(context.Background())
	})
	if sharedWasm == nil && initErr != nil {
		return nil, initErr
	}
	return sharedWasm, nil
}

func newWasmRunner(ctx context.Context) (*wasmRunner, error) {
	// WithCloseOnContextDone makes wazero abort execution when the context is
	// cancelled, which is how the per-run timeout is enforced.
	rt := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfig().
			WithMemoryLimitPages(wasmMemoryPages).
			WithMemoryCapacityFromMax(true).
			WithCloseOnContextDone(true).
			WithCompilationCache(wazero.NewCompilationCache()),
	)
	r := &wasmRunner{runtime: rt, cache: map[string]wazero.CompiledModule{}}

	// The only host module a plugin may import.
	builder := r.runtime.NewHostModuleBuilder("forgec2")
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, ptr, length uint32) {
			run := activeLogSink(ctx)
			if run == nil {
				return
			}
			run.addLog(mod, ptr, length)
		}).
		Export("log")
	hostMod, err := builder.Instantiate(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate forgec2 host module: %w", err)
	}
	r.hostMod = hostMod
	return r, nil
}

// Close releases the shared runtime (tests and shutdown).
func (r *wasmRunner) Close(ctx context.Context) {
	if r == nil || r.runtime == nil {
		return
	}
	_ = r.runtime.Close(ctx)
}

// runWasmPlugin executes a WASM plugin and returns its JSON result bytes as
// stdout, so the existing Result/Report parsers work unchanged.
func (e *executor) runWasmPlugin(ctx context.Context, pluginDir string, m *Manifest, input map[string]interface{}) (*execResult, error) {
	runner, err := getWasmRunner()
	if err != nil {
		return nil, err
	}
	entry := filepath.Join(pluginDir, filepath.FromSlash(m.Entry))
	info, err := os.Stat(entry)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %q: cannot stat entry: %w", m.Name, err)
	}
	if info.Size() > maxWasmBinaryBytes {
		return nil, fmt.Errorf("wasm plugin %q: module exceeds %d bytes", m.Name, maxWasmBinaryBytes)
	}
	binary, err := os.ReadFile(entry)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %q: cannot read module: %w", m.Name, err)
	}

	run := &wasmRun{}
	ctx = withLogSink(ctx, run)

	compiled, err := runner.compile(ctx, entry, binary)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %q: %w", m.Name, err)
	}
	mod, err := runner.runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %q: instantiate: %w", m.Name, err)
	}
	defer mod.Close(ctx)

	payload, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %q: marshal input: %w", m.Name, err)
	}
	// ExportedMemory, not Memory: Module.Memory returns a typed-nil interface
	// when the module defines no memory at all, which would panic on the
	// first method call. ExportedMemory returns a plain nil.
	mem := mod.ExportedMemory("memory")
	if mem == nil {
		return nil, fmt.Errorf("wasm plugin %q: module does not export linear memory", m.Name)
	}
	// Write the input past the module's initial memory. Module data (globals,
	// heap, stack) is laid out by the module's own toolchain — wasm-ld's
	// default puts the data segment right at address 1024 — so parking the
	// input at a fixed offset would overwrite it before run() executes.
	// Memory past the module's initial size is guaranteed untouched, and the
	// ABI hands the pointer to the module anyway.
	inputOffset := int(mem.Size())
	if inputOffset < wasmInputOffset {
		inputOffset = wasmInputOffset
	}
	if err := ensureWasmRegion(mem, inputOffset, len(payload)); err != nil {
		return nil, fmt.Errorf("wasm plugin %q: input does not fit in memory: %w", m.Name, err)
	}
	if !mem.Write(uint32(inputOffset), payload) {
		return nil, fmt.Errorf("wasm plugin %q: could not write input into memory", m.Name)
	}

	fn := mod.ExportedFunction("run")
	if fn == nil {
		return nil, fmt.Errorf("wasm plugin %q: module does not export run(ptr,len)->i64", m.Name)
	}
	results, err := fn.Call(ctx, uint64(inputOffset), uint64(len(payload)))
	if err != nil {
		if ctx.Err() != nil {
			return &execResult{TimedOut: true}, fmt.Errorf("wasm plugin %q timed out: %w", m.Name, ctx.Err())
		}
		return &execResult{Stderr: run.stderr()}, fmt.Errorf("wasm plugin %q failed: %w", m.Name, err)
	}
	if len(results) != 1 {
		return &execResult{Stderr: run.stderr()}, fmt.Errorf("wasm plugin %q: run must return one i64 (ptr | len<<32), got %d results", m.Name, len(results))
	}
	resultPtr := uint32(results[0])
	length := uint32(results[0] >> 32)
	if length == 0 || resultPtr == 0 {
		res := &execResult{Stderr: run.stderr()}
		if ctx.Err() != nil {
			res.TimedOut = true
		}
		return res, nil
	}
	if length > maxWasmResultBytes {
		return &execResult{Stderr: run.stderr()}, fmt.Errorf("wasm plugin %q: result of %d bytes exceeds the %d byte cap", m.Name, length, maxWasmResultBytes)
	}
	size := mem.Size()
	if resultPtr+length > size || resultPtr+length < resultPtr {
		return &execResult{Stderr: run.stderr()}, fmt.Errorf("wasm plugin %q: result region out of bounds (ptr=%d len=%d, memory %d)", m.Name, resultPtr, length, size)
	}
	result, ok := mem.Read(resultPtr, length)
	if !ok {
		return &execResult{Stderr: run.stderr()}, fmt.Errorf("wasm plugin %q: result region unreadable", m.Name)
	}

	res := &execResult{Stdout: result, Stderr: run.stderr()}
	if ctx.Err() != nil {
		res.TimedOut = true
	}
	return res, nil
}

func (r *wasmRunner) compile(ctx context.Context, path string, binary []byte) (wazero.CompiledModule, error) {
	// Look up under the lock, but COMPILE outside it: compilation takes milliseconds to seconds, and
	// holding the mutex across it serialized every concurrent wasm run. Worse, a goroutine waiting on
	// sync.Mutex.Lock cannot be cancelled, so a run whose context expired while queueing still waited
	// for the compile ahead of it before failing.
	r.mu.Lock()
	if c, ok := r.cache[path]; ok {
		r.mu.Unlock()
		return c, nil
	}
	r.mu.Unlock()

	c, err := r.runtime.CompileModule(ctx, binary)
	if err != nil {
		return nil, fmt.Errorf("invalid wasm module: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Another goroutine may have compiled the same module while we did: keep the first one, so the
	// cache stays 1:1 with the path.
	if existing, ok := r.cache[path]; ok {
		return existing, nil
	}
	if len(r.cache) >= 64 {
		// Bounded cache: drop everything rather than grow without limit.
		r.cache = map[string]wazero.CompiledModule{}
	}
	r.cache[path] = c
	return c, nil
}

// ensureWasmRegion grows memory so [offset, offset+length) is addressable.
func ensureWasmRegion(mem api.Memory, offset, length int) error {
	if length < 0 || offset < 0 {
		return fmt.Errorf("invalid region")
	}
	need := offset + length
	pageSize := 65536
	have := int(mem.Size())
	if need <= have {
		return nil
	}
	missing := (need - have + pageSize - 1) / pageSize
	if _, ok := mem.Grow(uint32(missing)); !ok {
		return fmt.Errorf("needs %d bytes, only %d available (cap %d pages)", need, have, wasmMemoryPages)
	}
	return nil
}

// ── per-run log sink ────────────────────────────────────────────────────────
// wazero host functions receive a context, so the sink travels with the run
// instead of being shared mutable state.

type logSinkKey struct{}

type wasmLogSink struct {
	mu       sync.Mutex
	lines    []byte
	dropped  int
	maxBytes int
}

type wasmRun struct {
	sink *wasmLogSink
}

func (r *wasmRun) addLog(mod api.Module, ptr, length uint32) {
	if length > 64*1024 {
		r.sink.dropped += int(length)
		return
	}
	mem := mod.ExportedMemory("memory")
	if mem == nil {
		r.sink.dropped += int(length)
		return
	}
	data, ok := mem.Read(ptr, length)
	if !ok {
		r.sink.dropped += int(length)
		return
	}
	r.sink.mu.Lock()
	defer r.sink.mu.Unlock()
	if len(r.sink.lines)+len(data) > r.sink.maxBytes {
		r.sink.dropped += len(data)
		return
	}
	r.sink.lines = append(r.sink.lines, data...)
	r.sink.lines = append(r.sink.lines, '\n')
}

func (r *wasmRun) stderr() []byte {
	if r == nil || r.sink == nil {
		return nil
	}
	r.sink.mu.Lock()
	defer r.sink.mu.Unlock()
	out := append([]byte(nil), r.sink.lines...)
	if r.sink.dropped > 0 {
		out = append(out, fmt.Sprintf("\n[forgec2: %d log bytes dropped]\n", r.sink.dropped)...)
	}
	return out
}

func withLogSink(ctx context.Context, run *wasmRun) context.Context {
	run.sink = &wasmLogSink{maxBytes: maxWasmLogBytes}
	return context.WithValue(ctx, logSinkKey{}, run)
}

func activeLogSink(ctx context.Context) *wasmRun {
	run, _ := ctx.Value(logSinkKey{}).(*wasmRun)
	return run
}

// isWasmInterpreter reports whether the manifest selects the WASM tier.
func isWasmInterpreter(interpreter string) bool {
	return strings.EqualFold(strings.TrimSpace(interpreter), "wasm")
}
