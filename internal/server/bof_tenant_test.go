package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBOFFilesAreTenantScoped pins ownership on uploaded BOF blobs. bof_files has no tenant
// column, so ownership runs through the uploader - and every per-id handler used findOrFail,
// which ignores the context entirely. A BOF is an executable the implant loads, so an unscoped
// download/run is worse than a read: it hands another tenant's tooling to be executed.
func TestBOFFilesAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	if err := s.db.Create(&db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.BOFFile{Name: "victim.o", Description: "sekrit-bof", Data: []byte("sekrit-bof-bytes"), CreatedBy: "tenant1-admin"}).Error; err != nil {
		t.Fatal(err)
	}
	var victim db.BOFFile
	if err := s.db.Where("name = ?", "victim.o").First(&victim).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(method, target string, tenant uint) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, nil)
		c.Params = gin.Params{{Key: "id", Value: uintString(victim.ID)}}
		c.Set("user", "tenant2-admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		c.Set(middleware.TenantIDContextKey, tenant)
		return c, w
	}

	// The list does not expose it.
	c, w := ctxFor(http.MethodGet, "/api/bof", 2)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/bof", nil)
	s.handleBOFList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}
	if containsAny(w.Body.String(), "victim.o") {
		t.Fatalf("tenant 2 listed tenant 1's BOF: %s", w.Body.String())
	}

	// The download does not serve it.
	c, w = ctxFor(http.MethodGet, "/api/bof/"+uintString(victim.ID), 2)
	s.handleBOFDownload(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 downloaded tenant 1's BOF: %d", w.Code)
	}
	if containsAny(w.Body.String(), "sekrit-bof-bytes") {
		t.Fatal("the BOF bytes leaked")
	}

	// The delete does not remove it.
	c, w = ctxFor(http.MethodDelete, "/api/bof/"+uintString(victim.ID), 2)
	s.handleBOFDelete(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 deleted tenant 1's BOF: %d", w.Code)
	}
	var remaining int64
	if err := s.db.Model(&db.BOFFile{}).Where("id = ?", victim.ID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("the BOF was deleted")
	}

	// And the owning tenant still gets it, so the scope is not a blanket denial.
	c, w = ctxFor(http.MethodGet, "/api/bof/"+uintString(victim.ID), 1)
	c.Set("user", "tenant1-admin")
	s.handleBOFDownload(c)
	if w.Code != http.StatusOK {
		t.Fatalf("tenant 1 could not download its own BOF: %d (%s)", w.Code, w.Body.String())
	}
}
