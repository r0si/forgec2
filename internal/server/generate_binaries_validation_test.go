package server

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/gin-gonic/gin"
)

// binFormCtx builds a form POST context for parseBinaryForm.
func binFormCtx(t *testing.T, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/generate/exe", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("user", "admin")
	return c, w
}

// binIconCtx builds a multipart POST carrying an icon file.
func binIconCtx(t *testing.T, fields map[string]string, icon []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("icon", "icon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(icon); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/generate/exe", &body)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("user", "admin")
	return c, w
}

func seedBinaryListener(t *testing.T, s *Server) db.Listener {
	t.Helper()
	l := db.Listener{Name: "bin-listener", TenantID: 1, Scheme: "http", Host: "10.0.0.1", Port: 8080, Enabled: true}
	if err := s.db.Create(&l).Error; err != nil {
		t.Fatal(err)
	}
	return l
}

// TestParseBinaryFormValidation covers the rejections that happen before any
// build work: a missing listener, an unknown listener, bad mTLS material, a
// bad icon and an unparsable C2 URL.
func TestParseBinaryFormValidation(t *testing.T) {
	s := mustTenantServer(t)
	listener := seedBinaryListener(t, s)

	// No listener, DNS or P2P config.
	c, w := binFormCtx(t, url.Values{})
	if _, ok := s.parseBinaryForm(c); ok {
		t.Fatal("a form without a listener was accepted")
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "listener or DNS domain is required") {
		t.Fatalf("missing listener = %d %s", w.Code, w.Body.String())
	}

	// Unknown listener id.
	c, w = binFormCtx(t, url.Values{"listener_id": {"9999"}})
	if _, ok := s.parseBinaryForm(c); ok {
		t.Fatal("an unknown listener was accepted")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown listener = %d", w.Code)
	}

	// mTLS material that cannot complete a handshake (cert with no key).
	c, w = binFormCtx(t, url.Values{
		"listener_id": {jsonNumber(listener.ID)},
		"mtls_cert":   {"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"},
	})
	if _, ok := s.parseBinaryForm(c); ok {
		t.Fatal("incomplete mTLS material was accepted")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad mTLS = %d %s", w.Code, w.Body.String())
	}

	// An icon whose magic is neither ICO nor PNG.
	c, w = binIconCtx(t, map[string]string{"listener_id": jsonNumber(listener.ID)}, []byte("GIF89a-not-an-icon"))
	if _, ok := s.parseBinaryForm(c); ok {
		t.Fatal("an invalid icon was accepted")
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "icon must be") {
		t.Fatalf("bad icon = %d %s", w.Code, w.Body.String())
	}

	// A PNG icon is accepted and re-encoded into the form.
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	c, w = binIconCtx(t, map[string]string{"listener_id": jsonNumber(listener.ID)}, png)
	form, ok := s.parseBinaryForm(c)
	if !ok {
		t.Fatalf("a PNG icon was rejected: %d %s", w.Code, w.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(form.IconB64)
	if err != nil || !bytes.Equal(decoded, png) {
		t.Fatalf("icon round-trip failed: %q err=%v", form.IconB64, err)
	}
	if form.mintTenant != 0 {
		t.Fatalf("mintTenant = %d for an unscoped context", form.mintTenant)
	}

	// An oversized icon is refused.
	big := bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47}, 70*1024)
	c, w = binIconCtx(t, map[string]string{"listener_id": jsonNumber(listener.ID)}, big)
	if _, ok := s.parseBinaryForm(c); ok {
		t.Fatal("an oversized icon was accepted")
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too large") {
		t.Fatalf("big icon = %d %s", w.Code, w.Body.String())
	}

	// An unparsable failover URL is rejected.
	c, w = binFormCtx(t, url.Values{
		"listener_id": {jsonNumber(listener.ID)},
		"c2_url":      {"http://["},
	})
	if _, ok := s.parseBinaryForm(c); ok {
		t.Fatal("an unparsable C2 URL was accepted")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad c2 url = %d %s", w.Code, w.Body.String())
	}
}

// TestProfileAuditHash pins the audit fingerprint: deterministic for the same
// shape and different as soon as a malleable field changes.
func TestProfileAuditHash(t *testing.T) {
	base := payload.ImplantConfig{BeaconURI: "/api/v1/beacon", Method: "POST", UserAgent: "Mozilla/5.0"}
	first := profileAuditHash(base)
	if len(first) != 12 {
		t.Fatalf("hash length = %d, want 12", len(first))
	}
	if again := profileAuditHash(base); again != first {
		t.Fatalf("hash is not deterministic: %q vs %q", first, again)
	}
	if other := profileAuditHash(payload.ImplantConfig{BeaconURI: "/api/v1/beacon", Method: "GET", UserAgent: "Mozilla/5.0"}); other == first {
		t.Fatal("changing the method did not change the audit hash")
	}
	if other := profileAuditHash(payload.ImplantConfig{BeaconURI: "/other", Method: "POST", UserAgent: "Mozilla/5.0"}); other == first {
		t.Fatal("changing the URI did not change the audit hash")
	}
}

// TestHandleGetBeaconKey covers the key endpoint's shape.
func TestHandleGetBeaconKey(t *testing.T) {
	s := mustTenantServer(t)
	c, w := missingAPICtx(http.MethodGet, "/api/generate/beacon-key", "", nil)
	s.handleGetBeaconKey(c)
	if w.Code != http.StatusOK {
		t.Fatalf("beacon key = %d %s", w.Code, w.Body.String())
	}
	// The endpoint mirrors whatever the server has configured (an empty value
	// means "PSK auth disabled" rather than an error).
	want := s.cfg.Server.BeaconKey
	var out struct {
		Key string `json:"beacon_key"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Key != want {
		t.Fatalf("beacon key = %q, want the configured %q", out.Key, want)
	}

	s.cfg.Server.BeaconKey = "configured-psk"
	c, w = missingAPICtx(http.MethodGet, "/api/generate/beacon-key", "", nil)
	s.handleGetBeaconKey(c)
	if w.Code != http.StatusOK {
		t.Fatalf("configured beacon key = %d", w.Code)
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Key != "configured-psk" {
		t.Fatalf("beacon key = %q, want the configured value", out.Key)
	}
}

// TestGenerateHandlersRejectMissingForm proves each entry point validates before
// doing any build work.
func TestGenerateHandlersRejectMissingForm(t *testing.T) {
	s := mustTenantServer(t)
	cases := []struct {
		name    string
		handler func(*gin.Context)
	}{
		{"exe", s.handleGenerateEXE},
		{"linux", s.handleGenerateLinux},
		{"macos", s.handleGenerateMacOS},
		{"dll", s.handleGenerateDLL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := binFormCtx(t, url.Values{})
			tc.handler(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s without a form = %d %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}
