package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
)

// TestAIDeletePluginTool closes the assistant's plugin lifecycle: create, run, update, and finally
// remove. Without it the assistant's experiments accumulate in the console forever, and the only way to
// clean up is for the operator to do it by hand.
func TestAIDeletePluginTool(t *testing.T) {
	s := mustTenantServer(t)
	if s.pluginManager == nil {
		s.pluginManager = plugin.NewManager(s.db)
	}
	root := t.TempDir()
	if err := s.pluginManager.LoadFromDisk(root); err != nil {
		t.Fatalf("temp root: %v", err)
	}

	interpreter := "bash"
	code := `echo '{"success":true,"output":"temporary"}'`
	if runtime.GOOS == "windows" {
		interpreter = "powershell"
		code = `Write-Output '{"success":true,"output":"temporary"}'`
	}
	aiCtx := &aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "operator-1", Role: db.RoleAdmin}}

	created := s.aiCreatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
		"name": "ai-temp", "description": "temporary", "type": "command",
		"interpreter": interpreter, "code": code,
	}))
	if !strings.Contains(created, `"created":true`) {
		t.Skipf("cannot create a %s plugin here: %s", interpreter, created)
	}
	dir := filepath.Join(root, "ai-temp")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the package is missing before the delete: %v", err)
	}

	// Deleting removes the plugin from memory, its record and its directory.
	out := s.aiDeletePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{"name": "ai-temp"}))
	if !strings.Contains(out, `"deleted":true`) {
		t.Fatalf("delete failed: %s", out)
	}
	if _, err := s.pluginManager.Get("ai-temp"); err == nil {
		t.Fatal("the plugin is still registered")
	}
	var record db.Plugin
	if err := s.db.Where("name = ?", "ai-temp").First(&record).Error; err == nil {
		t.Fatalf("the plugin record survived: %+v", record)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the package directory survived")
	}

	// A human-authored plugin is refused and left completely untouched.
	if err := s.db.Create(&db.Plugin{Name: "human-two", Author: "alice", Type: "command", Version: "1.2.3"}).Error; err != nil {
		t.Fatalf("seed human plugin: %v", err)
	}
	out = s.aiDeletePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{"name": "human-two"}))
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "not written by the assistant") {
		t.Fatalf("a human-authored plugin was deleted: %s", out)
	}
	var human db.Plugin
	if err := s.db.Where("name = ?", "human-two").First(&human).Error; err != nil {
		t.Fatalf("the refused delete removed the record: %v", err)
	}
	if human.Version != "1.2.3" {
		t.Fatalf("the refused delete changed the record: %+v", human)
	}

	// An unknown name reports the registered set so the model can correct itself in one turn.
	out = s.aiDeletePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{"name": "never-existed"}))
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "human-two") {
		t.Fatalf("an unknown name did not list the registered plugins: %s", out)
	}

	// A missing name is a bad request, not an empty-name delete.
	if got := s.aiDeletePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{})); !strings.Contains(got, `"error"`) {
		t.Fatalf("an empty name was accepted: %s", got)
	}
}
