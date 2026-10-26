package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestSanitizeRequestHost pins the validator's accept/reject matrix. The Host
// header ends up inside generated PowerShell/curl commands, so anything beyond
// hostname[:port] or a bracketed IPv6 literal must be refused.
func TestSanitizeRequestHost(t *testing.T) {
	accept := []string{
		"c2.example.test",
		"c2.example.test:8443",
		"10.0.0.5:8000",
		"127.0.0.1",
		"[::1]:8000",
		"[fe80::1]",
		"localhost:8000",
	}
	for _, in := range accept {
		if got, ok := sanitizeRequestHost(in); !ok || got == "" {
			t.Fatalf("sanitizeRequestHost(%q) rejected a valid host", in)
		}
	}

	reject := []string{
		"",
		"c2.example'; Start-Process calc; '",
		"evil.test\"; calc; \"",
		"evil.test/../x",
		"evil.test x",
		"evil.test;calc",
		"evil.test$env:TEMP",
		"evil.test`calc`",
		"evil.test:0",
		"evil.test:99999",
		"evil.test:-1",
		"c2.example.test:80:90",
		"[::1",
		"-evil.test",
		"evil.test-",
	}
	for _, in := range reject {
		if got, ok := sanitizeRequestHost(in); ok {
			t.Fatalf("sanitizeRequestHost(%q) accepted %q", in, got)
		}
	}
}

// TestOneLinerRejectsSpoofedHost proves the handler refuses to build a
// download URL (and therefore the generated commands) from an injected Host.
func TestOneLinerRejectsSpoofedHost(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	if err := s.db.Create(&db.Listener{Name: "ol", Scheme: "http", Host: "0.0.0.0", Port: 8080, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	post := func(host string) (*httptest.ResponseRecorder, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := "c2_url=http%3A%2F%2F127.0.0.1%3A8080&listener_id=1&payload_type=ps1"
		c.Request = httptest.NewRequest(http.MethodPost, "/generate/oneliner", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.Request.Host = host
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set("tenant_id", uint(1))
		s.handleGenerateOneLiner(c)
		return w, w.Body.String()
	}

	// A spoofed Host is refused before any payload is generated.
	bad := "evil.example'; Start-Process calc; '"
	w, _ := post(bad)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("injected host = %d, want 400", w.Code)
	}

	// A valid Host is accepted and the URL in the generated commands uses it.
	w, body := post("c2.example.test:8443")
	if w.Code != http.StatusOK {
		t.Fatalf("valid host = %d %s", w.Code, body)
	}
	if !strings.Contains(body, "c2.example.test:8443") {
		t.Fatalf("generated one-liners do not carry the request host: %.300s", body)
	}
	if strings.Contains(body, "Start-Process calc") {
		t.Fatal("injected command reached the generated one-liners")
	}
}
