package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPluginLoadFailures covers the endpoint that names packages which are on disk but rejected.
//
// A package that cannot be registered is silently absent: it never appears in the plugin list, cannot be run, and the only
// trace was a startup warning. An operator who installed a plugin and sees nothing assumes the console is stale.
func TestPluginLoadFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// A plugin directory with one valid package and one whose manifest cannot parse.
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	bad := filepath.Join(dir, "bad")
	for _, path := range []string{good, bad} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(good, "manifest.yaml"), []byte(`name: good
version: 1.0.0
type: command
interpreter: sh
entry: main.sh
description: fine
`), 0o644); err != nil {
		t.Fatalf("write good manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(good, "main.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatalf("write good entry: %v", err)
	}
	// Tab-indented YAML is invalid, which is the cheapest way to make a package unregistrable.
	if err := os.WriteFile(filepath.Join(bad, "manifest.yaml"), []byte("name: bad\n\tversion: 1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write bad manifest: %v", err)
	}

	manager := plugin.NewManager(s.db)
	if err := manager.LoadFromDisk(dir); err != nil {
		t.Fatalf("load plugins: %v", err)
	}
	s.pluginManager = manager

	call := func() map[string]interface{} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/plugins/failures", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handlePluginLoadFailures(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Data
	}

	data := call()
	if data["plugins_enabled"] != true {
		t.Fatalf("plugins_enabled = %v", data["plugins_enabled"])
	}
	failures, _ := data["failures"].([]interface{})
	if len(failures) == 0 {
		t.Fatalf("the unregistrable package was not reported: %v", data)
	}
	// The good package must NOT be reported: this list is the rejected set, not the installed set.
	for _, raw := range failures {
		entry, _ := raw.(map[string]interface{})
		if entry["name"] == "good" {
			t.Fatalf("a package that loaded was reported as a failure: %v", entry)
		}
		if entry["reason"] == "" || entry["reason"] == nil {
			t.Fatalf("a failure was reported without a reason: %v", entry)
		}
	}
	if data["count"] != float64(len(failures)) {
		t.Fatalf("count = %v, failures = %d", data["count"], len(failures))
	}

	// Without a manager the answer says so rather than claiming zero failures.
	s.pluginManager = nil
	data = call()
	if data["plugins_enabled"] != false {
		t.Fatalf("a server with no plugin manager claimed plugins_enabled = %v", data["plugins_enabled"])
	}
}
