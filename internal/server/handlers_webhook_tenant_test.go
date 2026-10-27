package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func seedWebhook(t *testing.T, s *Server, name string, tenantID uint, eventType string) db.WebhookConfig {
	t.Helper()
	ensureExportCrypto(t, s)
	wh := db.WebhookConfig{
		Name:      name,
		URL:       "https://203.0.113.9/" + name,
		EventType: eventType,
		Method:    "POST",
		Headers:   `{"Authorization":"Bearer test-token"}`,
		Enabled:   true,
		TenantID:  tenantID,
	}
	if err := s.db.Create(&wh).Error; err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	return wh
}

func webhookNames(list []db.WebhookConfig) []string {
	out := make([]string, 0, len(list))
	for _, w := range list {
		out = append(out, w.Name)
	}
	return out
}

// TestWebhooksForEventScoped (P1-3) proves event delivery is contained:
// a webhook never receives another tenant's event data.
func TestWebhooksForEventScoped(t *testing.T) {
	s := mustTenantServer(t)
	evt := string(EventImplantCheckin)
	seedWebhook(t, s, "wh-own", 1, evt)
	seedWebhook(t, s, "wh-foreign", 2, evt)
	seedWebhook(t, s, "wh-legacy", 0, string(EventSchedule))
	disabled := db.WebhookConfig{Name: "wh-off", URL: "https://203.0.113.9/off", EventType: evt, TenantID: 1}
	if err := s.db.Create(&disabled).Error; err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
	// NOTE: gorm omits false bools on Create when the column has
	// default:true, so disable via Update (mirrors the UI toggle path).
	if err := s.db.Model(&db.WebhookConfig{}).Where("id = ?", disabled.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}

	got := webhookNames(s.webhooksForEvent(Event{Type: EventImplantCheckin, AgentID: "a", TenantID: 1}))
	if len(got) != 1 || got[0] != "wh-own" {
		t.Fatalf("tenant-1 event fired %v, want [wh-own]", got)
	}
	got = webhookNames(s.webhooksForEvent(Event{Type: EventImplantCheckin, AgentID: "b", TenantID: 2}))
	if len(got) != 1 || got[0] != "wh-foreign" {
		t.Fatalf("tenant-2 event fired %v, want [wh-foreign]", got)
	}
	got = webhookNames(s.webhooksForEvent(Event{Type: EventSchedule}))
	if len(got) != 1 || got[0] != "wh-legacy" {
		t.Fatalf("legacy event fired %v, want [wh-legacy]", got)
	}
}

func webhookCtx(s *Server, t *testing.T, method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, "wh-viewer", 1)
	if body != "" {
		c.Request, _ = http.NewRequest(method, "/", bytes.NewReader([]byte(body)))
		c.Request.Header.Set("Content-Type", "application/json")
	} else {
		c.Request, _ = http.NewRequest(method, "/", nil)
	}
	return c, w
}

// TestWebhookCRUDScoped proves webhook rows are invisible across tenants
// and creations inherit the operator's tenant.
func TestWebhookCRUDScoped(t *testing.T) {
	s := mustTenantServer(t)
	seedWebhook(t, s, "wh-list-foreign", 2, string(EventImplantCheckin))

	c, w := webhookCtx(s, t, http.MethodGet, "")
	s.handleListWebhooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "wh-list-foreign") {
		t.Fatalf("foreign webhook leaked into list")
	}

	body := `{"name":"wh-stamped","url":"https://203.0.113.9/new","event_type":"` + string(EventImplantCheckin) + `","tenant_id":2}`
	c, w = webhookCtx(s, t, http.MethodPost, body)
	s.handleCreateWebhook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var stored db.WebhookConfig
	if err := s.db.Where("name = ?", "wh-stamped").First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.TenantID != 1 {
		t.Fatalf("stored tenant=%d, want 1 (body spoof ignored)", stored.TenantID)
	}

	c, w = webhookCtx(s, t, http.MethodDelete, "")
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(stored.ID + 100000)}}
	s.handleDeleteWebhook(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete-missing status=%d, want 404", w.Code)
	}

	var foreign db.WebhookConfig
	if err := s.db.Where("name = ?", "wh-list-foreign").First(&foreign).Error; err != nil {
		t.Fatalf("reload foreign: %v", err)
	}
	c, w = webhookCtx(s, t, http.MethodDelete, "")
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(foreign.ID)}}
	s.handleDeleteWebhook(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete-foreign status=%d, want 404", w.Code)
	}
	var kept db.WebhookConfig
	if err := s.db.First(&kept, foreign.ID).Error; err != nil {
		t.Fatalf("foreign webhook deleted cross-tenant: %v", err)
	}
}

// TestWebhookHeadersEncrypted proves Authorization-bearing headers are
// encrypted at rest but transparent to same-tenant readers.
func TestWebhookHeadersEncrypted(t *testing.T) {
	s := mustTenantServer(t)
	ensureExportCrypto(t, s)
	wh := seedWebhook(t, s, "wh-enc", 1, string(EventImplantCheckin))

	var raw string
	if err := s.db.Model(&db.WebhookConfig{}).Select("headers").Where("id = ?", wh.ID).First(&raw).Error; err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if strings.Contains(raw, "Bearer test-token") {
		t.Fatalf("headers stored in plaintext: %q", raw)
	}

	c, w := webhookCtx(s, t, http.MethodGet, "")
	s.handleListWebhooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Bearer test-token") {
		t.Fatalf("decrypted headers missing from list for owner tenant: %s", w.Body.String())
	}
}
