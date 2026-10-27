package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestHandleFileExfilGetIsTenantScoped covers the cross-tenant case the existing tests do not:
// GET /agents/:id/files/exfil/:filename is the crown-jewel path (it serves what an implant
// exfiltrated), and it is safe only because getAgentOrFail is tenant-scoped. That property is
// one "optimization" of a shared helper away from silently disappearing - an operator in
// tenant 2 who guessed an agent id would read tenant 1's loot.
func TestHandleFileExfilGetIsTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	srv.cfg.Server.DataDir = t.TempDir()

	agent := seedImplant(t, database)
	agent.TenantID = 1
	if err := database.Model(&db.Implant{}).Where("id = ?", agent.ID).Update("tenant_id", uint(1)).Error; err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(srv.cfg.Server.DataDir, "uploads", agent.ID)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "loot.txt"), []byte("tenant-one-loot"), 0600); err != nil {
		t.Fatal(err)
	}

	fetch := func(tenant uint) *httptest.ResponseRecorder {
		w, c := newFormContext(http.MethodGet, "/", nil)
		c.Set("user", "operator")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		// The tenant the auth middleware would attach; round-161 made the server prefer it.
		c.Set(middleware.TenantIDContextKey, tenant)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}, {Key: "filename", Value: "loot.txt"}}
		srv.handleFileExfilGet(c)
		return w
	}

	// The owning tenant reads it.
	if w := fetch(1); w.Code != http.StatusOK {
		t.Fatalf("tenant 1 could not read its own exfil: %d %s", w.Code, w.Body.String())
	}

	// Another tenant must not, even with the exact agent id and filename.
	other := fetch(2)
	if other.Code == http.StatusOK {
		t.Fatalf("tenant 2 read tenant 1's exfiltrated file: %s", other.Body.String())
	}
	if other.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read answered %d, want 404 (not 403: existence must not leak)", other.Code)
	}
}
