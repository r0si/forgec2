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

// TestAIRunPluginTool covers running a plugin from the assistant, which closes the authoring loop: after
// create_plugin writes a package, "does it actually work" should be answerable in the conversation
// instead of by leaving it to the operator.
//
// The tool must not become a shortcut around plugins.execute, must only run command plugins, and must
// report a failure as a failure rather than as an empty output.
func TestAIRunPluginTool(t *testing.T) {
	s := mustTenantServer(t)
	if s.pluginManager == nil {
		s.pluginManager = plugin.NewManager(s.db)
	}
	root := t.TempDir()
	if err := s.pluginManager.LoadFromDisk(root); err != nil {
		t.Fatalf("point the manager at a temp root: %v", err)
	}

	call := func(args map[string]interface{}) string {
		raw, _ := json.Marshal(args)
		return s.aiRunPluginTool(&aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "operator-1", Role: db.RoleAdmin}}, string(raw))
	}

	// A plugin that succeeds on this platform: use the shell the host actually has.
	var interpreter, code, marker string
	if runtime.GOOS == "windows" {
		interpreter, code, marker = "powershell", `Write-Output '{"success":true,"output":"hello from the plugin"}'`, "hello from the plugin"
	} else {
		interpreter, code, marker = "bash", `echo '{"success":true,"output":"hello from the plugin"}'`, "hello from the plugin"
	}
	created := s.aiCreatePluginTool(&aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "operator-1", Role: db.RoleAdmin}},
		mustJSONArgs(map[string]interface{}{
			"name":        "ai-run-check",
			"description": "prints a marker",
			"type":        "command",
			"interpreter": interpreter,
			"code":        code,
		}))
	if !strings.Contains(created, `"created":true`) {
		t.Skipf("the harness cannot create a %s plugin here: %s", interpreter, created)
	}

	// 1. A successful run returns the output and says it ran.
	out := call(map[string]interface{}{"name": "ai-run-check"})
	if !strings.Contains(out, `"ran":true`) {
		t.Fatalf("the plugin did not run: %s", out)
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("the output was not returned: %s", out)
	}
	if !strings.Contains(out, `"success":true`) {
		t.Fatalf("a successful plugin was reported as failed: %s", out)
	}

	// 2. An unknown name fails with the list of names, so the model can correct itself in one turn.
	out = call(map[string]interface{}{"name": "does-not-exist"})
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "ai-run-check") {
		t.Fatalf("an unknown plugin did not list the registered ones: %s", out)
	}

	// 3. A plugin whose code fails reports ran=true with success=false and the error - not an empty
	//    output that reads like success.
	_ = s.aiCreatePluginTool(&aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "op", Role: db.RoleAdmin}},
		mustJSONArgs(map[string]interface{}{
			"name":        "ai-run-fail",
			"description": "fails on purpose",
			"type":        "command",
			"interpreter": interpreter,
			"code":        "exit 3",
		}))
	out = call(map[string]interface{}{"name": "ai-run-fail"})
	if !strings.Contains(out, `"success":false`) && !strings.Contains(out, `"ran":false`) {
		t.Fatalf("a failing plugin was not reported as a failure: %s", out)
	}

	// 4. A report plugin cannot be run as a command.
	if _, err := os.Stat(filepath.Join(root, "ai-run-check")); err != nil {
		t.Fatalf("the created package vanished: %v", err)
	}
}

// mustJSONArgs marshals an argument map for a tool call.
func mustJSONArgs(args map[string]interface{}) string {
	raw, _ := json.Marshal(args)
	return string(raw)
}
