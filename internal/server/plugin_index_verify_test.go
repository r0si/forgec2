package server

import (
	"bytes"
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

// TestPluginIndexVerify covers POST /api/plugins/index/verify: comparing an installation against an
// index document.
//
// The distinction the endpoint has to get right is between "same" and "checkable": an entry without a
// digest cannot prove anything, so it is reported as unverifiable rather than counted as a match - a
// false "in sync" is worse than an honest "I cannot tell".
func TestPluginIndexVerify(t *testing.T) {
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
		"name": "verify-me", "description": "x", "type": "command",
		"interpreter": interpreter, "code": code,
	})); !strings.Contains(out, `"created":true`) {
		t.Skipf("cannot create a plugin here: %s", out)
	}

	// The installed truth, read from the export endpoint's own builder.
	var installed []pluginIndexEntry
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/plugins/index", nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		installed = s.buildPluginIndex(c)
	}
	if len(installed) != 1 || installed[0].Digest == "" {
		t.Fatalf("installed set = %+v", installed)
	}
	real := installed[0]

	verify := func(entries []pluginIndexEntry) (int, map[string]interface{}) {
		body, _ := json.Marshal(map[string]interface{}{"plugins": entries})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/plugins/index/verify", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		s.handlePluginIndexVerify(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. The exact index: in sync.
	code200, data := verify([]pluginIndexEntry{{Name: real.Name, Version: real.Version, Digest: real.Digest}})
	if code200 != http.StatusOK {
		t.Fatalf("status = %d", code200)
	}
	if data["in_sync"] != true {
		t.Fatalf("an identical index was not reported in sync: %v", data)
	}

	// 2. A different digest is drift, and both sides are reported.
	_, data = verify([]pluginIndexEntry{{Name: real.Name, Version: real.Version, Digest: "sha256:deadbeef"}})
	if data["in_sync"] != false {
		t.Fatalf("a digest mismatch was reported in sync: %v", data)
	}
	if changed, _ := data["changed"].([]interface{}); len(changed) != 1 {
		t.Fatalf("changed = %v, want one entry", data["changed"])
	}

	// 3. An entry with no digest cannot be verified - it is not a match.
	_, data = verify([]pluginIndexEntry{{Name: real.Name, Version: real.Version}})
	if data["in_sync"] != false {
		t.Fatalf("an unverifiable entry was reported in sync: %v", data)
	}
	if unverifiable, _ := data["unverifiable"].([]interface{}); len(unverifiable) != 1 {
		t.Fatalf("unverifiable = %v, want one entry", data["unverifiable"])
	}

	// 4. A name the installation does not have is missing; a name it has but the index omits is extra.
	_, data = verify([]pluginIndexEntry{
		{Name: real.Name, Version: real.Version, Digest: real.Digest},
		{Name: "not-here", Digest: "sha256:abc"},
	})
	if missing, _ := data["missing"].([]interface{}); len(missing) != 1 {
		t.Fatalf("missing = %v, want one entry", data["missing"])
	}

	// Empty and malformed documents are rejected rather than silently "in sync".
	if got, _ := verify(nil); got != http.StatusBadRequest {
		t.Fatalf("an empty index = %d, want 400", got)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/plugins/index/verify", strings.NewReader("{not json"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handlePluginIndexVerify(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed index = %d, want 400", w.Code)
	}
}
