package plugin

import (
	"context"
	"strings"
	"testing"
)

// buildImportedFuncModule builds the smallest module that IMPORTS one host function and
// exports the ABI surface. The import section (id 2) sits between type and function, as
// the binary format requires, and the imported function takes index 0 - so the defined
// "run" is index 1.
func buildImportedFuncModule(t *testing.T, moduleName, funcName string) []byte {
	t.Helper()
	// A wrong-signature import is refused for the signature rather than for the missing
	// module, which would make the negative cases pass for the wrong reason. Use the
	// declared log signature (i32,i32)->() for the one host function that exists.
	if moduleName == "forgec2" && funcName == "log" {
		return buildLogImportModule()
	}
	mod := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}

	// type: (i32,i32)->i64
	typ := []byte{0x01, 0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7E}
	// import: one import of kind func, type 0
	imp := []byte{0x01}
	imp = append(imp, wasmName(moduleName)...)
	imp = append(imp, wasmName(funcName)...)
	imp = append(imp, 0x00, 0x00)
	// function: one defined function of type 0 (its index is 1)
	fun := []byte{0x01, 0x00}
	mem := []byte{0x01, 0x00, 0x01}
	// exports: memory(0), run(1)
	exp := []byte{0x02}
	exp = append(exp, wasmName("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmName("run")...)
	exp = append(exp, 0x00, 0x01)
	// code: one body returning i64 0
	body := []byte{0x00, 0x42, 0x00, 0x0B}
	code := append([]byte{0x01}, wasmULEB(uint32(len(body)))...)
	code = append(code, body...)

	for _, s := range []wasmSection{
		{1, typ}, {2, imp}, {3, fun}, {5, mem}, {7, exp}, {10, code},
	} {
		mod = append(mod, s.id)
		mod = append(mod, wasmULEB(uint32(len(s.buf)))...)
		mod = append(mod, s.buf...)
	}
	return mod
}

// TestWasmSandboxRejectsEveryHostImport is the threat model from docs/PLUGIN_TIERS.md in
// executable form: "no WASI, no filesystem/network/env" is only true if a module that
// imports such a host function cannot be instantiated at all. The runner instantiates
// exactly one host module, forgec2, so anything else must fail before run() is reached.
func TestWasmSandboxRejectsEveryHostImport(t *testing.T) {
	cases := []struct{ module, function string }{
		{"wasi_snapshot_preview1", "fd_write"},
		{"wasi_snapshot_preview1", "environ_get"},
		{"wasi_unstable", "fd_read"},
		{"env", "getenv"},
		{"forgec2", "exec"},         // the one host module, but not a function it exposes
		{"forgec2", "http_request"}, // neither is this
	}
	for _, tc := range cases {
		t.Run(tc.module+"."+tc.function, func(t *testing.T) {
			dir, m := writeWasmPlugin(t, buildImportedFuncModule(t, tc.module, tc.function))
			_, err := (&executor{}).run(context.Background(), dir, m, map[string]interface{}{}, 0)
			if err == nil {
				t.Fatalf("a module importing %s.%s was executed", tc.module, tc.function)
			}
			// The failure must name the missing host function, so an operator can see
			// why their plugin was refused instead of guessing.
			if !strings.Contains(err.Error(), tc.module) {
				t.Fatalf("error does not name the host module: %v", err)
			}
		})
	}
}

// TestWasmSandboxOnlyExposesLog proves the positive half: the one host function the
// runner does provide is log(), and it is provided on forgec2.
func TestWasmSandboxOnlyExposesLog(t *testing.T) {
	dir, m := writeWasmPlugin(t, buildImportedFuncModule(t, "forgec2", "log"))
	if _, err := (&executor{}).run(context.Background(), dir, m, map[string]interface{}{}, 0); err != nil {
		t.Fatalf("a module importing forgec2.log was refused: %v", err)
	}
}

// buildLogImportModule imports forgec2.log with the signature the runner declares,
// (i32,i32)->(), so the positive case exercises the sandbox allowing what it should.
func buildLogImportModule() []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
	// type 0: (i32,i32)->i64 for run; type 1: (i32,i32)->() for log
	typ := []byte{0x02, 0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7E, 0x60, 0x02, 0x7F, 0x7F, 0x00}
	imp := []byte{0x01}
	imp = append(imp, wasmName("forgec2")...)
	imp = append(imp, wasmName("log")...)
	imp = append(imp, 0x00, 0x01)
	fun := []byte{0x01, 0x00}
	mem := []byte{0x01, 0x00, 0x01}
	exp := []byte{0x02}
	exp = append(exp, wasmName("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmName("run")...)
	exp = append(exp, 0x00, 0x01)
	body := []byte{0x00, 0x42, 0x00, 0x0B}
	code := append([]byte{0x01}, wasmULEB(uint32(len(body)))...)
	code = append(code, body...)
	for _, s := range []wasmSection{{1, typ}, {2, imp}, {3, fun}, {5, mem}, {7, exp}, {10, code}} {
		mod = append(mod, s.id)
		mod = append(mod, wasmULEB(uint32(len(s.buf)))...)
		mod = append(mod, s.buf...)
	}
	return mod
}
