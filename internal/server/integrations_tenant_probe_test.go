package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func integrationCtx(method, body string, uid uint, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/integrations", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uid)
	c.Params = params
	return c, w
}

// TestIntegrationsAreTenantScoped covers the integrations view of the webhook
// table: creation must stamp the caller's tenant (otherwise the delivery
// filter, which matches on tenant_id, never fires the integration), the list
// must hide other tenants' rows, and update/toggle/delete must refuse a
// foreign row.
func TestIntegrationsAreTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	seedAIUser(t, s, "admin1", 1)
	seedAIUser(t, s, "admin2", 2)

	foreign := db.WebhookConfig{Name: "foreign", URL: "https://203.0.113.9/hook", EventType: "alert", Method: "POST", Enabled: true, TenantID: 2}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	// Create as a tenant-1 operator: the row must carry tenant 1.
	c, w := integrationCtx(http.MethodPost, `{"type":"slack","name":"mine","url":"https://203.0.113.9/mine","event_type":"alert"}`, 1, nil)
	s.handleIntegrationsCreate(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created db.WebhookConfig
	if err := s.db.Where("name = ?", "mine").First(&created).Error; err != nil {
		t.Fatalf("created webhook missing: %v", err)
	}
	if created.TenantID != 1 {
		t.Fatalf("created webhook tenant = %d, want the caller's tenant 1", created.TenantID)
	}

	// The list must not show the tenant-2 webhook.
	c, w = integrationCtx(http.MethodGet, "", 1, nil)
	s.handleIntegrationsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "foreign") {
		t.Fatalf("list leaked another tenant's integration: %.300s", w.Body.String())
	}

	// Toggle and delete must answer 404 for the foreign row and leave it alone.
	c, w = integrationCtx(http.MethodPost, "", 1, gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}})
	s.handleIntegrationsToggle(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign toggle = %d %s", w.Code, w.Body.String())
	}
	c, w = integrationCtx(http.MethodDelete, "", 1, gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}})
	s.handleIntegrationsDelete(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d %s", w.Code, w.Body.String())
	}
	var still db.WebhookConfig
	if err := s.db.Where("id = ?", foreign.ID).First(&still).Error; err != nil {
		t.Fatal("foreign integration was deleted")
	}
	if !still.Enabled {
		t.Fatal("foreign integration was toggled")
	}

	// Updating a foreign row is refused and does not change its URL.
	c, w = integrationCtx(http.MethodPut, `{"name":"hijacked","url":"https://attacker.test/hook"}`, 1, gin.Params{{Key: "id", Value: jsonNumber(foreign.ID)}})
	s.handleIntegrationsUpdate(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign update = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("id = ?", foreign.ID).First(&still).Error; err != nil {
		t.Fatal(err)
	}
	if still.URL != "https://203.0.113.9/hook" || still.Name != "foreign" {
		t.Fatalf("foreign integration was rewritten: url=%q name=%q", still.URL, still.Name)
	}
}
