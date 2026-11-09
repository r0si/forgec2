package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestBeaconReportsItsTLSProfile covers the fingerprint an agent reports at check-in.
//
// It is the CONFIGURED uTLS preset, not a measured handshake: the server never sees a client hello, so the field exists to answer
// "is this beacon pretending to be Chrome, and which version" - and an empty value means "not reported", never "no TLS".
//
// The check-in goes through processAgentRegistration on purpose. Seeding the row directly (what this test used to do) cannot see
// the info allowlist, and `tls_profile` was missing from it: the agent reported the field on every beacon and the server dropped it
// before enrollment, so the column stayed empty in every real deployment while the direct-seed test stayed green.
func TestBeaconReportsItsTLSProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// 1. A reported profile survives the allowlist and lands in the row.
	req := beaconRequest{
		UUID: "a-tls",
		Info: map[string]string{"hostname": "web-01", "tls_profile": "random"},
	}
	agent, _ := s.processAgentRegistration(req, "203.0.113.9", time.Now())
	if agent.ID == "" {
		t.Fatal("the agent was not enrolled")
	}
	var stored db.Implant
	if err := s.db.First(&stored, "id = ?", "a-tls").Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored.TLSProfile != "random" {
		t.Fatalf("tls_profile = %q, want the reported preset", stored.TLSProfile)
	}

	// 2. A later beacon that omits it must not erase it: the field is a build
	// fact, and a partial check-in is not evidence of a rebuild.
	req.Info = map[string]string{"hostname": "web-01"}
	s.processAgentRegistration(req, "203.0.113.9", time.Now())
	if err := s.db.First(&stored, "id = ?", "a-tls").Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.TLSProfile != "random" {
		t.Fatalf("tls_profile after an empty report = %q, want random", stored.TLSProfile)
	}

	// 3. It travels in the agent JSON, where an operator actually looks.
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
	if !strings.Contains(w.Body.String(), `"tls_profile":"random"`) {
		t.Fatalf("the agent detail does not carry the profile: %s", w.Body.String())
	}
}

// TestSanitizeInfoKeepsTLSProfile is the narrow half of the test above: the
// allowlist must not quietly drop a field the agent reports, and it must still
// drop fields nobody declared.
func TestSanitizeInfoKeepsTLSProfile(t *testing.T) {
	out := sanitizeInfo(map[string]string{
		"hostname":    "web-01",
		"tls_profile": "firefox",
		"not_a_field": "x",
	})
	if out["tls_profile"] != "firefox" {
		t.Fatalf("tls_profile = %q, want firefox", out["tls_profile"])
	}
	if _, ok := out["not_a_field"]; ok {
		t.Fatal("sanitizeInfo accepted an undeclared key")
	}
}
