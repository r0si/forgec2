package payload

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A symbol declared only in a windows-tagged file is invisible to a build that
// never runs for windows, which is exactly how the agent broke off-Windows for
// three days. This gate compiles the implant for every target the product
// advertises, from whatever host runs the test.

var (
	agentTargetOnce sync.Once
	agentTargetList []string
)

// supportedAgentTargets flattens supportedArchByOS — the map the generate API
// already treats as "the architectures this product actually builds" — so a
// new shipped target is covered the moment it is added to that map, with no
// second list to keep in sync.
func supportedAgentTargets() []string {
	agentTargetOnce.Do(func() {
		for goos, arches := range supportedArchByOS {
			for _, arch := range arches {
				agentTargetList = append(agentTargetList, goos+"/"+arch)
			}
		}
	})
	return agentTargetList
}

// buildEnv returns the host environment with the named keys replaced by
// values, leaving every other entry (including GOPATH and GOMODCACHE) intact.
//
// exec.Cmd documents duplicate-entry behaviour as operating-system-specific,
// so the old values are removed rather than shadowed.
func buildEnv(values map[string]string) []string {
	out := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if _, clash := values[name]; found && clash {
			continue
		}
		out = append(out, entry)
	}
	for name, value := range values {
		out = append(out, name+"="+value)
	}
	return out
}

// TestAgentCompilesForEverySupportedTarget cross-compiles the implant for each
// GOOS/GOARCH in the shipped matrix.
//
// The generate pipeline builds the agent at request time, so a windows-only
// symbol referenced from cross-platform code breaks every non-Windows payload.
// `go build ./...` on a Windows host never sees it, because Go compiles only
// the host's GOOS. agent_main.go once assigned useIndirectSyscall (declared in
// edr_strategies_windows.go) that way — and a runtime.GOOS guard does not
// help, since the compiler still type-checks that branch for linux and darwin.
// The defect shipped for three days, failing CI's go/lint/smoke/docker/agent-e2e
// jobs on every push while every local check stayed green: each agent build
// test in this package is gated behind an env var, so nothing compiled the
// agent off-host by default.
//
// Builds run in parallel. A warm module cache costs a few seconds per target;
// a cold one costs a couple of minutes, which is the price of the class of
// bug it catches. Set FORGEC2_SKIP_AGENT_MATRIX=1 when iterating on a machine
// where that cost is not wanted.
func TestAgentCompilesForEverySupportedTarget(t *testing.T) {
	if os.Getenv("FORGEC2_SKIP_AGENT_MATRIX") != "" {
		t.Skip("FORGEC2_SKIP_AGENT_MATRIX is set")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	agentDir, err := filepath.Abs(filepath.Join("..", "payload", "agent"))
	if err != nil {
		t.Fatalf("resolve agent package path: %v", err)
	}
	targets := supportedAgentTargets()
	if len(targets) == 0 {
		t.Fatal("supportedArchByOS produced no build targets")
	}
	outRoot := t.TempDir()

	for _, target := range targets {
		goos, goarch, ok := strings.Cut(target, "/")
		if !ok || goos == "" || goarch == "" {
			t.Fatalf("bad target %q (want goos/goarch)", target)
		}
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			out := filepath.Join(outRoot, "agent-"+goos+"-"+goarch)
			if goos == "windows" {
				out += ".exe"
			}
			cmd := exec.Command(goBin, "build", "-o", out, agentDir)
			cmd.Env = buildEnv(map[string]string{
				"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0",
				// A host-set GOFLAGS could inject a build tag that hides a
				// constraint error, which is the thing this gate exists to find.
				"GOFLAGS": "", "GORACE": "",
			})
			combined, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go build %s failed: %v\n%s", target, err, trimOutput(string(combined), 4000))
			}
			// A zero-length binary means the compile succeeded but produced
			// nothing usable, which the generate pipeline would hand to an
			// operator as a working payload.
			if fi, statErr := os.Stat(out); statErr != nil || fi.Size() == 0 {
				t.Fatalf("build %s produced no binary (stat err=%v)", target, statErr)
			}
		})
	}
}

func trimOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
