package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
)

// TestDataDirResolutionFamily pins the four places that resolve a directory from
// server.data_dir, because they used to disagree: payloads and uploads wrote to a RELATIVE
// path when the setting was empty (so the files landed next to the process working
// directory), screenshots already defaulted to "data", and the build output directory
// resolved to <cwd>/agents. One rule now: a write target never depends on the caller's cwd.
func TestDataDirResolutionFamily(t *testing.T) {
	// Case 1: no data_dir -> the writers refuse rather than guess.
	bare := &Server{cfg: &config.Config{}}
	if _, err := bare.hostedPayloadsDir(); err == nil {
		t.Fatal("hostedPayloadsDir accepted an empty data_dir")
	}
	if _, err := bare.uploadsRootDir(); err == nil {
		t.Fatal("uploadsRootDir accepted an empty data_dir")
	}

	// Case 2: no data_dir -> the build output directory falls back to the conventional
	// "data" tree instead of the current working directory.
	got := bare.extractAgentsDir()
	if !filepath.IsAbs(got) {
		t.Fatalf("agents dir is not absolute: %q", got)
	}
	if !strings.HasSuffix(filepath.ToSlash(got), "/data/agents") {
		t.Fatalf("agents dir = %q, want it under .../data/agents", got)
	}

	// Case 3: a configured data_dir is honoured by all three.
	dir := t.TempDir()
	configured := &Server{cfg: &config.Config{}}
	configured.cfg.Server.DataDir = dir

	p, err := configured.hostedPayloadsDir()
	if err != nil || p != filepath.Join(dir, "payloads") {
		t.Fatalf("hostedPayloadsDir = %q, %v", p, err)
	}
	u, err := configured.uploadsRootDir()
	if err != nil || u != filepath.Join(dir, "uploads") {
		t.Fatalf("uploadsRootDir = %q, %v", u, err)
	}
	if a := configured.extractAgentsDir(); a != filepath.Join(dir, "agents") {
		t.Fatalf("agents dir = %q, want %q", a, filepath.Join(dir, "agents"))
	}
}
