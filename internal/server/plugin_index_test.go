package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/gin-gonic/gin"
)

// TestPluginIndexExportsAVerifiableSet covers GET /api/plugins/index: the installed set as one document
// an operator can diff between deployments or hand to another team.
//
// The property that makes it more than a listing is the DIGEST: it is computed with the same function
// the trust policy uses, so a receiver can recompute it. A record whose package is missing must say so
// rather than invent one.
func TestPluginIndexExportsAVerifiableSet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	if s.pluginManager == nil {
		s.pluginManager = plugin.NewManager(s.db)
	}
	root := t.TempDir()
	if err := s.pluginManager.LoadFromDisk(root); err != nil {
		t.Fatalf("temp root: %v", err)
	}

	interpreter := "bash"
	code := `echo '{"success":true,"output":"ok"}'`
	if runtime.GOOS == "windows" {
		interpreter = "powershell"
		code = `Write-Output '{"success":true,"output":"ok"}'`
	}
	aiCtx := &aiReqCtx{Principal: aiPrincipal{UserID: 1, Username: "op", Role: db.RoleAdmin}}
	if out := s.aiCreatePluginTool(aiCtx, mustJSONArgs(map[string]interface{}{
		"name": "index-one", "description": "first", "type": "command",
		"interpreter": interpreter, "code": code, "category": "recon",
	})); !strings.Contains(out, `"created":true`) {
		t.Skipf("cannot create a plugin here: %s", out)
	}
	// A record whose package is missing entirely: it must appear, marked as not loaded and unsigned,
	// with no digest.
	if err := s.db.Create(&db.Plugin{Name: "ghost", Version: "9.9.9", Type: "command", Author: "alice"}).Error; err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	call := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/plugins/index", nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		s.handlePluginIndex(c)
		return w
	}

	w := call()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Count   int                `json:"count"`
			Plugins []pluginIndexEntry `json:"plugins"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Data.Count != 2 || len(resp.Data.Plugins) != 2 {
		t.Fatalf("index = %+v, want two entries", resp.Data)
	}
	// Sorted, so two deployments can be diffed.
	if resp.Data.Plugins[0].Name != "ghost" || resp.Data.Plugins[1].Name != "index-one" {
		t.Fatalf("entries are not sorted by name: %+v", resp.Data.Plugins)
	}

	ghost := resp.Data.Plugins[0]
	if ghost.Loaded || ghost.Digest != "" || ghost.Signed {
		t.Fatalf("a missing package was described as if it existed: %+v", ghost)
	}

	real := resp.Data.Plugins[1]
	if !real.Loaded {
		t.Fatalf("the created plugin is not reported as loaded: %+v", real)
	}
	if !strings.HasPrefix(real.Digest, "sha256:") {
		t.Fatalf("digest = %q, want sha256:", real.Digest)
	}
	// The digest is recomputable: the same function the trust policy uses must agree.
	dir := s.pluginManager.PluginDir("index-one")
	manifest, err := plugin.LoadManifest(dir + "/manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	want, err := plugin.PackageDigest(dir, manifest.Includes, dir)
	if err != nil {
		t.Fatalf("recompute digest: %v", err)
	}
	if real.Digest != want {
		t.Fatalf("digest = %q, recomputed %q", real.Digest, want)
	}
	if real.Interpreter != interpreter || real.Entry == "" {
		t.Fatalf("the entry does not describe how the plugin runs: %+v", real)
	}
	if len(real.Params) == 0 {
		t.Fatalf("the command plugin's parameters are missing: %+v", real)
	}
	if real.Category != "recon" {
		t.Fatalf("category = %q, want recon", real.Category)
	}

	// Two calls produce the same document: an index that changes on its own cannot be diffed.
	first := w.Body.String()
	if second := call().Body.String(); second != first {
		t.Fatalf("the index is not deterministic:\n%s\n%s", first, second)
	}
}
