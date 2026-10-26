package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func pluginToggleCtx(p *db.Plugin, body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/plugins/"+p.Name+"/toggle", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Params = gin.Params{{Key: "id", Value: p.Name}}
	return c, w
}

// TestPluginToggleWithoutManager pins two things: a server with no plugin
// manager (any test or plugin-disabled build) must not panic on a nil manager,
// and the toggle still persists and reports the new state.
func TestPluginToggleWithoutManager(t *testing.T) {
	s := mustTenantServer(t)
	if s.pluginManager != nil {
		t.Skip("this server has a plugin manager")
	}
	p := db.Plugin{Name: "toggle-me", Version: "1.0.0", Enabled: true, Type: "command"}
	if err := s.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}

	// Explicit disable.
	c, w := pluginToggleCtx(&p, `{"enabled":false}`)
	s.handlePluginToggle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("toggle = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "false") {
		t.Fatalf("toggle body = %.120s", w.Body.String())
	}
	var reloaded db.Plugin
	if err := s.db.Where("name = ?", p.Name).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Fatal("disable was not persisted")
	}

	// No body flips the current value back on.
	c, w = pluginToggleCtx(&p, ``)
	s.handlePluginToggle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("flip = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("name = ?", p.Name).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if !reloaded.Enabled {
		t.Fatal("flip did not re-enable the plugin")
	}

	// An unknown plugin is a 404, not a silent success.
	c, w = pluginToggleCtx(&db.Plugin{Name: "missing"}, `{"enabled":true}`)
	s.handlePluginToggle(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin = %d %s", w.Code, w.Body.String())
	}
}
