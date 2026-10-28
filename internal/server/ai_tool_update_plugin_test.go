package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
)

// TestAIUpdatePluginTool covers iterating on an assistant-authored plugin.
//
// Deleting and recreating a plugin to change one line loses its id, reviews and config, so the tool
// rewrites the entry file in place. Two properties matter: the new code must be what actually runs, and
// a model must never overwrite code it did not write.
func TestAIUpdatePluginTool(t *testing.T) {
	s := mustTenantServer(t)
	if s.pluginManager == nil {
		s.pluginManager = plugin.NewManager(s.db)
	}
	root := t.TempDir()
	if err := s.pluginManager.LoadFromDisk(root); err != nil {
		t.Fatalf("temp root: %v", err)
	}

	interpreter := "bash"
	if runtime.GOOS == "windows" {
		interpreter = "powershell"
	}
	aiCtx := &aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "operator-1", Role: db.RoleAdmin}}

	first := `{"success":true,"output":"version one"}`
	second := `{"success":true,"output":"version two"}`
	if interpreter == "powershell" {
		code := func(body string) string { return "Write-Output '" + body + "'" }
		created := s.aiCreatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
			"name": "ai-iterate", "description": "v1", "type": "command",
			"interpreter": interpreter, "code": code(first),
		}))
		if !strings.Contains(created, `"created":true`) {
			t.Skipf("cannot create a powershell plugin here: %s", created)
		}
		out := s.aiUpdatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
			"name": "ai-iterate", "code": code(second), "description": "v2",
		}))
		if !strings.Contains(out, `"updated":true`) {
			t.Fatalf("update failed: %s", out)
		}
	} else {
		created := s.aiCreatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
			"name": "ai-iterate", "description": "v1", "type": "command",
			"interpreter": interpreter, "code": "echo '" + first + "'",
		}))
		if !strings.Contains(created, `"created":true`) {
			t.Skipf("cannot create a bash plugin here: %s", created)
		}
		out := s.aiUpdatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
			"name": "ai-iterate", "code": "echo '" + second + "'", "description": "v2",
		}))
		if !strings.Contains(out, `"updated":true`) {
			t.Fatalf("update failed: %s", out)
		}
	}

	// The version moved without losing the rest of it, and the record follows.
	var record db.Plugin
	if err := s.db.Where("name = ?", "ai-iterate").First(&record).Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.Version != "1.0.1" {
		t.Fatalf("version = %q, want 1.0.1 (patch bump)", record.Version)
	}
	if record.Description != "v2" {
		t.Fatalf("description = %q, want v2", record.Description)
	}

	// The code on disk is the new code, and running it proves the in-memory copy was replaced too.
	body, err := os.ReadFile(filepath.Join(root, "ai-iterate", "plugin.py"))
	if err == nil && !strings.Contains(string(body), "version two") {
		t.Fatalf("the entry file still holds the old code: %q", body)
	}
	ran := s.aiRunPluginTool(aiCtx, mustJSONArgs(map[string]interface{}{"name": "ai-iterate"}))
	if !strings.Contains(ran, "version two") {
		t.Fatalf("running after the update did not use the new code: %s", ran)
	}
	if strings.Contains(ran, "version one") {
		t.Fatalf("the stale version still runs: %s", ran)
	}

	// A plugin the assistant did not author is refused: a model must not rewrite someone else's code.
	if err := s.db.Create(&db.Plugin{Name: "human-made", Author: "alice", Type: "command", Version: "2.0.0"}).Error; err != nil {
		t.Fatalf("seed human plugin: %v", err)
	}
	out := s.aiUpdatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
		"name": "human-made", "code": "echo nope",
	}))
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "not written by the assistant") {
		t.Fatalf("a human-authored plugin was accepted: %s", out)
	}
	var human db.Plugin
	if err := s.db.Where("name = ?", "human-made").First(&human).Error; err != nil {
		t.Fatalf("human record: %v", err)
	}
	if human.Version != "2.0.0" || human.Author != "alice" {
		t.Fatalf("the refused update changed the record: %+v", human)
	}

	// Unknown names, empty code and oversized code are refused.
	for _, bad := range []map[string]interface{}{
		{"name": "does-not-exist", "code": "echo x"},
		{"name": "ai-iterate", "code": ""},
		{"name": "ai-iterate", "code": strings.Repeat("a", aiPluginCodeMaxBytes+1)},
	} {
		if got := s.aiUpdatePluginTool(aiCtx, mustJSONArgs(bad)); !strings.Contains(got, `"error"`) {
			t.Fatalf("accepted %v: %s", bad, got)
		}
	}

	// A package whose manifest was deleted cannot be updated (nothing to edit against).
	if err := os.Remove(filepath.Join(root, "ai-iterate", "manifest.yaml")); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	if got := s.aiUpdatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
		"name": "ai-iterate", "code": "echo x",
	})); !strings.Contains(got, `"error"`) {
		t.Fatalf("updated a package with no manifest: %s", got)
	}

	_ = json.Marshal // keep the import if the assertions above change
}
