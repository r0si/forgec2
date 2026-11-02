package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestCustomRolePermissionsAreReadOnlyAndRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := setupAPITestDB(t)
	if err := database.AutoMigrate(&db.CustomRole{}); err != nil {
		t.Fatal(err)
	}
	role := db.CustomRole{Name: "demo-observer", Permissions: `["agents.read","tasks.read"]`}
	if err := database.Create(&role).Error; err != nil {
		t.Fatal(err)
	}

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/agents", nil)
		c.Set("user_role", role.Name)
		setCustomRolePermissions(c, database, role.Name)
		return c, w
	}

	c, w := newContext()
	RequirePermission(db.PermAgentsRead)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("observer read status=%d", w.Code)
	}
	c, w = newContext()
	RequirePermission(db.PermAgentsWrite)(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("observer write status=%d, want 403", w.Code)
	}
	c, w = newContext()
	RequireAllPermissions(db.PermAgentsRead, db.PermTasksRead)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("observer combined read status=%d", w.Code)
	}

	if err := database.Model(&role).Update("permissions", `[]`).Error; err != nil {
		t.Fatal(err)
	}
	c, w = newContext()
	RequirePermission(db.PermAgentsRead)(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("updated role kept stale grant: status=%d", w.Code)
	}

	c, w = newContext()
	c.Set("auth_via_api_key", true)
	c.Set("api_key_scopes", map[string]bool{db.PermAgentsWrite: true})
	RequirePermission(db.PermAgentsWrite)(c)
	if w.Code == http.StatusOK {
		t.Fatal("scoped key exceeded the observer role")
	}
}

func TestUnknownCustomRoleGrantsNothing(t *testing.T) {
	database := setupAPITestDB(t)
	if err := database.AutoMigrate(&db.CustomRole{}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", "missing-role")
	setCustomRolePermissions(c, database, "missing-role")
	RequirePermission(db.PermAgentsRead)(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing role status=%d, want 403", w.Code)
	}
}

func TestAuthRequiredLoadsCustomRoleForAPIKey(t *testing.T) {
	database := setupAPITestDB(t)
	if err := database.AutoMigrate(&db.CustomRole{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&db.CustomRole{Name: "demo-observer", Permissions: `["agents.read"]`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&db.User{}).Where("username = ?", "apiuser").Update("role", "demo-observer").Error; err != nil {
		t.Fatal(err)
	}
	const key = "observer-api-key-test"
	if err := database.Create(&db.APIKey{KeyHash: sha256Of(key), Prefix: "observer", UserID: 1, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	c.Request.Header.Set("X-API-Key", key)
	AuthRequired(database)(c)
	if c.IsAborted() {
		t.Fatalf("observer API key authentication failed: %d", w.Code)
	}
	RequirePermission(db.PermAgentsRead)(c)
	if w.Code != http.StatusOK {
		t.Fatalf("observer API key read status=%d", w.Code)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	c.Request.Header.Set("X-API-Key", key)
	AuthRequired(database)(c)
	RequirePermission(db.PermAgentsWrite)(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("observer API key write status=%d, want 403", w.Code)
	}
}
