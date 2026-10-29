package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPluginInventoryCSVExport covers ?format=csv on the plugin list.
//
// "What was installed, and what version" is a question a review asks, and the plugin set was the last list in the console with no
// export at all - the per-plugin export produces one package, not an inventory.
func TestPluginInventoryCSVExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	if err := s.db.Create(&db.Plugin{
		Name: "hello-report", Version: "1.2.0", Type: "report", Category: "reporting",
		Author: "forgec2-ai", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("seed plugin: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/plugins?format=csv", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handlePluginList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want a header and the seeded plugin", len(rows))
	}
	if got := strings.Join(rows[0], ","); got != "ID,Name,Version,Type,Category,Author,Enabled" {
		t.Fatalf("header = %q", got)
	}
	if rows[1][1] != "hello-report" || rows[1][2] != "1.2.0" || rows[1][6] != "true" {
		t.Fatalf("plugin row = %v", rows[1])
	}
	// The inventory carries its scope like every other download here.
	if scope := w.Header().Get(exportScopeHeader); scope != "all" {
		t.Fatalf("scope header = %q, want all for an unfiltered inventory", scope)
	}
}
