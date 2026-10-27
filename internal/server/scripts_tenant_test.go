package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestScriptsAreTenantScoped pins ownership on the script CRUD surface. The scripts table has no
// tenant column, so ownership runs through `created_by`; before this, the list returned every
// tenant's code and the save/delete looked rows up by id alone. Overwriting another tenant's
// script is worse than reading it: scripts run on events, so the attacker's code executes with
// the victim's identity the next time their automation fires.
func TestScriptsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// A tenant-1 admin exists so created_by maps to tenant 1, plus the victim script.
	if err := s.db.Create(&db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Script{Name: "victim-script", Code: "return 'sekrit-code'", Enabled: true, CreatedBy: "tenant1-admin"}).Error; err != nil {
		t.Fatal(err)
	}
	var victim db.Script
	if err := s.db.Where("name = ?", "victim-script").First(&victim).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(method, target string, body []byte, tenant uint) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "tenant2-admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		c.Set(middleware.TenantIDContextKey, tenant)
		return c, w
	}

	// 1. The list does not expose the other tenant's code.
	c, w := ctxFor(http.MethodGet, "/api/scripting/scripts", nil, 2)
	s.handleAPIGetScripts(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-code")) {
		t.Fatalf("tenant 2 listed tenant 1's script code: %s", w.Body.String())
	}

	// 2. Editing by id cannot overwrite it.
	patch, _ := json.Marshal(map[string]string{
		"id":   uintString(victim.ID),
		"name": "victim-script",
		"code": "return 'attacker-code'",
	})
	c, w = ctxFor(http.MethodPost, "/api/scripting/scripts", patch, 2)
	s.handleAPISaveScript(c)
	var after db.Script
	if err := s.db.First(&after, victim.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(after.Code), []byte("attacker-code")) {
		t.Fatalf("tenant 2 overwrote tenant 1's script: %q", after.Code)
	}

	// 3. And cannot delete it.
	c, w = ctxFor(http.MethodDelete, "/api/scripting/scripts/"+uintString(victim.ID), nil, 2)
	c.Params = gin.Params{{Key: "id", Value: uintString(victim.ID)}}
	s.handleAPIDeleteScript(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 deleted tenant 1's script: %d (%s)", w.Code, w.Body.String())
	}
	var remaining int64
	if err := s.db.Model(&db.Script{}).Where("id = ?", victim.ID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("the victim script was deleted")
	}

	// 4. Tenant 1's own admin still sees and can delete it, so the scope is not a blanket deny.
	if err := s.db.Create(&db.User{Username: "t1second", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	c, w = ctxFor(http.MethodGet, "/api/scripting/scripts", nil, 1)
	c.Set("user", "t1second")
	s.handleAPIGetScripts(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("victim-script")) {
		t.Fatalf("tenant 1 cannot see its own script: %s", w.Body.String())
	}
	c, w = ctxFor(http.MethodDelete, "/api/scripting/scripts/"+uintString(victim.ID), nil, 1)
	c.Set("user", "t1second")
	c.Params = gin.Params{{Key: "id", Value: uintString(victim.ID)}}
	s.handleAPIDeleteScript(c)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant 1 could not delete its own script: %d (%s)", w.Code, w.Body.String())
	}
}
