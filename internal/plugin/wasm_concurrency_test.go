package plugin

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// TestWasmConcurrentRunsCompileOnceAndAgree exercises the compiled-module cache from several
// goroutines at once.
//
// compile() takes the cache mutex, and the executor allows concurrent plugin runs, so this is the
// path where a cache bug would show up as a data race (the race job runs this package) or as two runs
// disagreeing about the result. The module is compiled once and reused, so the reports must be
// identical byte for byte.
func TestWasmConcurrentRunsCompileOnceAndAgree(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "wasm", "hello-report")
	m, err := LoadManifest(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		t.Fatalf("load example manifest: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("example manifest is invalid: %v", err)
	}

	const runs = 8
	results := make([]string, runs)
	errs := make([]error, runs)

	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := &executor{}
			res, err := e.run(context.Background(), dir, m, map[string]interface{}{
				"params": map[string]interface{}{"label": "concurrent"},
				"config": map[string]interface{}{},
			}, 0)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = string(res.Stdout)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
	}
	for i := 1; i < runs; i++ {
		if results[i] != results[0] {
			t.Fatalf("run %d produced different output than run 0:\n%q\n%q", i, results[i], results[0])
		}
	}
	if results[0] == "" {
		t.Fatal("the example plugin produced no output")
	}
}
