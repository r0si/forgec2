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

// TestPhishingAssetsAreTenantScoped pins the scope on the phishing surface, which is the one
// where an unscoped row is not just visible but RE-TARGETABLE: the template holds the landing
// page that captures credentials, and the campaign holds the SMTP settings and the target list.
// Both tables carry a creator, so ownership runs through it.
func TestPhishingAssetsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	if err := s.db.Create(&db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.PhishingTemplate{Name: "victim-tpl", Subject: "sekrit-subject", Body: "<p>sekrit-page</p>", CreatedBy: "tenant1-admin"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.PhishingCampaign{Name: "victim-camp", TargetList: "sekrit-target", SMTPHost: "sekrit.example", CreatedBy: "tenant1-admin"}).Error; err != nil {
		t.Fatal(err)
	}
	var tpl db.PhishingTemplate
	if err := s.db.Where("name = ?", "victim-tpl").First(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	var camp db.PhishingCampaign
	if err := s.db.Where("name = ?", "victim-camp").First(&camp).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(method, target string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		var r *http.Request
		if body != nil {
			r = httptest.NewRequest(method, target, bytes.NewReader(body))
		} else {
			r = httptest.NewRequest(method, target, nil)
		}
		r.Header.Set("Content-Type", "application/json")
		c.Request = r
		c.Set("user", "tenant2-admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		c.Set(middleware.TenantIDContextKey, uint(2))
		return c, w
	}

	// Lists do not expose either row.
	c, w := ctxFor(http.MethodGet, "/api/phishing/templates", nil)
	s.handleAPIPhishingTemplates(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-subject")) || bytes.Contains(w.Body.Bytes(), []byte("sekrit-page")) {
		t.Fatalf("tenant 2 listed tenant 1's template: %s", w.Body.String())
	}
	c, w = ctxFor(http.MethodGet, "/api/phishing/campaigns", nil)
	s.handleAPIPhishingCampaigns(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-target")) {
		t.Fatalf("tenant 2 listed tenant 1's campaign: %s", w.Body.String())
	}

	// Update-by-id does not reach the template.
	patch, _ := json.Marshal(map[string]string{"subject": "attacker-subject", "html": "<p>attacker</p>"})
	c, w = ctxFor(http.MethodPut, "/api/phishing/templates/"+uintString(tpl.ID), patch)
	c.Params = gin.Params{{Key: "id", Value: uintString(tpl.ID)}}
	s.handleAPIUpdatePhishingTemplate(c)
	var after db.PhishingTemplate
	if err := s.db.First(&after, tpl.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(after.Body), []byte("attacker")) {
		t.Fatalf("tenant 2 rewrote tenant 1's landing page: %q", after.Body)
	}

	// Delete-by-id does not remove it, and launching it is refused.
	c, w = ctxFor(http.MethodDelete, "/api/phishing/templates/"+uintString(tpl.ID), nil)
	c.Params = gin.Params{{Key: "id", Value: uintString(tpl.ID)}}
	s.handleAPIDeletePhishingTemplate(c)
	var tplRemaining int64
	if err := s.db.Model(&db.PhishingTemplate{}).Where("id = ?", tpl.ID).Count(&tplRemaining).Error; err != nil {
		t.Fatal(err)
	}
	if tplRemaining != 1 {
		t.Fatal("tenant 2 deleted tenant 1's template")
	}

	c, w = ctxFor(http.MethodPost, "/api/phishing/campaigns/"+uintString(camp.ID)+"/launch", nil)
	c.Params = gin.Params{{Key: "id", Value: uintString(camp.ID)}}
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 launched tenant 1's campaign: %d (%s)", w.Code, w.Body.String())
	}
}
