package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
)

// TestAICreatePluginTool covers the assistant's plugin-authoring tool.
//
// The tool writes code to disk and registers it, so the properties that matter are the boundaries, not
// the happy path: the name must be a safe directory, the entry must stay inside the package, oversized
// code is refused, a rejected manifest leaves nothing behind, and the plugin is NEVER executed by the
// tool itself.
func TestAICreatePluginTool(t *testing.T) {
	s := mustTenantServer(t)
	if s.pluginManager == nil {
		s.pluginManager = plugin.NewManager(s.db)
	}
	root := t.TempDir()
	// LoadFromDisk sets the manager's root; call it before the first package is written so
	// PluginDir and Register agree on where packages live.
	if err := s.pluginManager.LoadFromDisk(root); err != nil {
		t.Fatalf("point the manager at a temp root: %v", err)
	}

	call := func(args map[string]interface{}) string {
		raw, _ := json.Marshal(args)
		return s.aiCreatePluginTool(&aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "operator-1", Role: db.RoleAdmin}}, string(raw))
	}

	// 1. A valid python plugin is written, validated and registered.
	out := call(map[string]interface{}{
		"name":        "ai-hello",
		"description": "Says hello to the operator",
		"type":        "command",
		"interpreter": "python",
		"code":        "import json,sys\nprint(json.dumps({'ok': True}))\n",
	})
	if !strings.Contains(out, `"created":true`) {
		t.Fatalf("the plugin was not created: %s", out)
	}
	dir := filepath.Join(root, "ai-hello")
	if _, err := os.Stat(filepath.Join(dir, "manifest.yaml")); err != nil {
		t.Fatalf("the manifest was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.py")); err != nil {
		t.Fatalf("the entry file was not written: %v", err)
	}
	// Registered in memory and in the DB, and it says it was not executed.
	if _, err := s.pluginManager.Get("ai-hello"); err != nil {
		t.Fatalf("the plugin was not registered: %v", err)
	}
	var record db.Plugin
	if err := s.db.Where("name = ?", "ai-hello").First(&record).Error; err != nil {
		t.Fatalf("no plugin record: %v", err)
	}
	if !record.Enabled || record.Type != "command" {
		t.Fatalf("record = %+v", record)
	}
	if !strings.Contains(out, "NOT executed") {
		t.Fatalf("the result does not say the plugin was not run: %s", out)
	}

	// 1b. A capitalised name is normalised rather than rejected: the model often title-cases, and the
	// directory has to be lowercase for the loader.
	if got := call(map[string]interface{}{
		"name": "Ai-MixedCase", "description": "x", "interpreter": "python", "code": "print(1)",
	}); !strings.Contains(got, `"name":"ai-mixedcase"`) {
		t.Fatalf("a capitalised name was not normalised: %s", got)
	}

	// 2. Unsafe names, entries and oversized code are refused before anything is written.
	for _, bad := range []map[string]interface{}{
		{"name": "../escape", "description": "x", "interpreter": "python", "code": "print(1)"},
		{"name": "ok-name", "description": "x", "interpreter": "nope", "code": "print(1)"},
		{"name": "ok-name", "description": "x", "interpreter": "python", "code": ""},
		{"name": "ok-name", "description": "x", "interpreter": "python", "code": strings.Repeat("a", aiPluginCodeMaxBytes+1)},
		{"name": "ok-name", "description": "x", "interpreter": "python", "code": "print(1)", "entry": "../evil.py"},
	} {
		if got := call(bad); !strings.Contains(got, `"error"`) {
			t.Fatalf("accepted an unsafe request %v: %s", bad, got)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Fatal("a rejected name still created a directory")
	}

	// 3. A manifest the plugin package rejects leaves nothing behind.
	out = call(map[string]interface{}{
		"name":        "ai-badtype",
		"description": "x",
		"type":        "not-a-type",
		"interpreter": "python",
		"code":        "print(1)",
	})
	if !strings.Contains(out, `"error"`) {
		t.Fatalf("an invalid type was accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "ai-badtype")); err == nil {
		t.Fatal("a rejected manifest left a package directory behind")
	}

	// 4. A duplicate name is refused by Register, and the second attempt does not corrupt the first.
	out = call(map[string]interface{}{
		"name":        "ai-hello",
		"description": "again",
		"interpreter": "python",
		"code":        "print(2)",
	})
	if !strings.Contains(out, `"error"`) {
		t.Fatalf("a duplicate registration was accepted: %s", out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "plugin.py"))
	if err != nil || !strings.Contains(string(body), "ok") {
		t.Fatalf("the original package was damaged: %q (%v)", body, err)
	}

	// 5. The written package is what the plugin loader would accept: reload it from disk.
	// A fresh manager over the same root must find the package.
	fresh := plugin.NewManager(s.db)
	if err := fresh.LoadFromDisk(root); err != nil {
		t.Fatalf("the written package does not load: %v", err)
	}
	if _, err := fresh.Get("ai-hello"); err != nil {
		t.Fatalf("the package on disk is not loadable: %v", err)
	}
}

// TestAICreatePluginEntryMapCoversValidInterpreters keeps the AI mapping in step with what the plugin
// package accepts: a missing interpreter is a model call that always fails, and one it does not accept
// is a package that can never register.
func TestAICreatePluginEntryMapCoversValidInterpreters(t *testing.T) {
	for _, interpreter := range plugin.ValidInterpreters {
		entry, ok := aiPluginEntryByInterpreter[interpreter]
		if !ok {
			t.Errorf("interpreter %q is valid for the package but has no entry file mapping", interpreter)
			continue
		}
		if !strings.Contains(entry, ".") {
			t.Errorf("interpreter %q maps to %q, which has no extension", interpreter, entry)
		}
	}
}
