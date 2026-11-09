package server

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestParseBinaryFormTLSFingerprint pins the request boundary for the
// JA3/JA4 ClientHello option: known values are carried into the build config,
// and a typo is refused before a registration secret is minted or a job is
// queued (a silently stable fingerprint is exactly the failure this option is
// meant to prevent).
func TestParseBinaryFormTLSFingerprint(t *testing.T) {
	s := mustTenantServer(t)
	listener := db.Listener{
		Name:    "tls-fp",
		Scheme:  "https",
		Host:    "tls-fp.example.com",
		Port:    8443,
		Enabled: true,
	}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatalf("create listener: %v", err)
	}

	for _, tc := range []struct {
		value string
		want  string
	}{
		{"", ""},
		{"stable", "stable"},
		{"random", "random"},
		{"firefox", "firefox"},
	} {
		body := url.Values{
			"listener_id":     {strconv.FormatUint(uint64(listener.ID), 10)},
			"interval":        {"10"},
			"tls_fingerprint": {tc.value},
		}
		c, w := tenantCtx(http.MethodPost, "/api/generate/exe", "tls-fp-op", body.Encode())
		form, ok := s.parseBinaryForm(c)
		if !ok {
			t.Fatalf("tls_fingerprint=%q rejected: %s", tc.value, w.Body.String())
		}
		if form.TLSFingerprint != tc.want {
			t.Fatalf("tls_fingerprint=%q parsed to %q, want %q", tc.value, form.TLSFingerprint, tc.want)
		}
	}

	body := url.Values{
		"listener_id":     {strconv.FormatUint(uint64(listener.ID), 10)},
		"interval":        {"10"},
		"tls_fingerprint": {"opera"},
	}
	c, w := tenantCtx(http.MethodPost, "/api/generate/exe", "tls-fp-bad-op", body.Encode())
	if form, ok := s.parseBinaryForm(c); ok || form != nil {
		t.Fatal("parseBinaryForm accepted an unknown tls_fingerprint")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
