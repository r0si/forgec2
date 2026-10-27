package plugin

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The numbers in docs/PLUGIN_TIERS.md and config.example.yaml are a promise to plugin
// authors: how many runs may proceed at once, how much CPU and disk one run may use, and
// how long it may take. They were accurate when this test was written - but nothing held
// them to the constants, and a drift is invisible: the docs keep reading as authoritative
// while the server enforces something else.
func TestTierNumbersMatchTheCode(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "PLUGIN_TIERS.md"))
	if err != nil {
		t.Fatalf("read the tiers doc: %v", err)
	}
	example, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}

	type check struct {
		name  string
		src   []byte
		re    *regexp.Regexp
		want  int64
		where string
	}
	checks := []check{
		{
			name: "max_concurrent in the doc example", src: doc,
			re: regexp.MustCompile(`max_concurrent:\s*(\d+)`), want: int64(defaultMaxConcurrentPlugins),
			where: "docs/PLUGIN_TIERS.md",
		},
		{
			name: "max_concurrent in the shipped example", src: example,
			re: regexp.MustCompile(`(?m)^\s*max_concurrent:\s*(\d+)`), want: int64(defaultMaxConcurrentPlugins),
			where: "config.example.yaml",
		},
		{
			name: "max_cpu_seconds in the doc example", src: doc,
			re: regexp.MustCompile(`max_cpu_seconds:\s*(\d+)`), want: int64(defaultPluginCPUQuota / 1e9),
			where: "docs/PLUGIN_TIERS.md",
		},
		{
			name: "max_cpu_seconds in the shipped example", src: example,
			re: regexp.MustCompile(`(?m)^\s*max_cpu_seconds:\s*(\d+)`), want: int64(defaultPluginCPUQuota / 1e9),
			where: "config.example.yaml",
		},
		{
			name: "max_write_mb in the doc example", src: doc,
			re: regexp.MustCompile(`max_write_mb:\s*(\d+)`), want: defaultPluginWriteQuota >> 20,
			where: "docs/PLUGIN_TIERS.md",
		},
		{
			name: "max_write_mb in the shipped example", src: example,
			re: regexp.MustCompile(`(?m)^\s*max_write_mb:\s*(\d+)`), want: defaultPluginWriteQuota >> 20,
			where: "config.example.yaml",
		},
		{
			name: "timeout ceiling in the doc", src: doc,
			re: regexp.MustCompile(`(\d+)s server timeout ceiling`), want: int64(maxPluginTimeoutSecs),
			where: "docs/PLUGIN_TIERS.md",
		},
		{
			name: "default CPU budget in the doc", src: doc,
			re: regexp.MustCompile(`CPU-time budget \(default (\d+)s`), want: int64(defaultPluginCPUQuota / 1e9),
			where: "docs/PLUGIN_TIERS.md",
		},
		{
			name: "wasm memory cap in the doc", src: doc,
			re: regexp.MustCompile(`(\d+) MiB linear-memory`), want: wasmMemoryPages * 64 / 1024,
			where: "docs/PLUGIN_TIERS.md",
		},
	}

	for _, c := range checks {
		m := c.re.FindSubmatch(c.src)
		if m == nil {
			t.Errorf("%s: %s no longer matches the documented shape, so this check enforces nothing", c.where, c.name)
			continue
		}
		got, err := strconv.ParseInt(string(m[1]), 10, 64)
		if err != nil {
			t.Errorf("%s: %s is not a number: %q", c.where, c.name, m[1])
			continue
		}
		if got != c.want {
			t.Errorf("%s: %s says %d, the code enforces %d", c.where, c.name, got, c.want)
		}
	}
}
