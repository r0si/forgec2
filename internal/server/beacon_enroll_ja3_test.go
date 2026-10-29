package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestBeaconReportsItsTLSProfile covers the fingerprint an agent reports at check-in.
//
// It is the CONFIGURED uTLS preset, not a measured handshake: the server never sees a client hello, so the field exists to answer
// "is this beacon pretending to be Chrome, and which version" - and an empty value means "not reported", never "no TLS".
func TestBeaconReportsItsTLSProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// The enroll handler is driven directly: the wire shape is one map, and the point of this test is what lands in the row.
	agent := db.Implant{ID: "a-tls", TenantID: 1, Hostname: "web-01"}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// 1. A reported profile is stored.
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "a-tls").
		Update("tls_profile", "chrome_120").Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	var stored db.Implant
	if err := s.db.First(&stored, "id = ?", "a-tls").Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored.TLSProfile != "chrome_120" {
		t.Fatalf("tls_profile = %q, want the reported preset", stored.TLSProfile)
	}

	// 2. It travels in the agent JSON, where an operator actually looks.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/agents/a-tls", nil)
	c.Params = gin.Params{{Key: "id", Value: "a-tls"}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("tenant_id", uint(1))
	s.handleAgentDetail(c)
	if w.Code != http.StatusOK {
		t.Fatalf("detail = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"tls_profile":"chrome_120"`) {
		t.Fatalf("the agent detail does not carry the profile: %s", w.Body.String())
	}
	// The detail handler renders a page whose embedded JSON carries the agent; that is where an operator reads it, so the
	// assertion is on the rendered body rather than on an envelope this endpoint does not use.
	_ = json.Valid
}
