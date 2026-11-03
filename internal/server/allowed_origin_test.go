package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
)

// TestAllowedOrigin pins the WebSocket origin gate. Hostnames are
// case-insensitive (RFC 4343), so an allowlist entry typed with capitals must
// still match the browser's lowercase Origin — before this was normalised, an
// operator who had just added their host was still refused, with no clue why.
func TestAllowedOrigin(t *testing.T) {
	withOrigins := func(origins ...string) *config.Config {
		cfg := &config.Config{}
		cfg.Server.AllowedOrigins = origins
		return cfg
	}
	req := func(origin string, host ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if len(host) > 0 {
			r.Host = host[0]
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	// No Origin header is not a cross-origin request.
	if !allowedOrigin(withOrigins("example.com"), req("")) {
		t.Fatal("a request without Origin was refused")
	}

	// Exact match.
	if !allowedOrigin(withOrigins("example.com"), req("https://example.com")) {
		t.Fatal("an exactly allowed origin was refused")
	}
	// Case variants in BOTH directions.
	if !allowedOrigin(withOrigins("Example.COM"), req("https://example.com")) {
		t.Fatal("a capitalised allowlist entry did not match")
	}
	if !allowedOrigin(withOrigins("example.com"), req("https://EXAMPLE.com")) {
		t.Fatal("a capitalised Origin did not match")
	}
	// Surrounding whitespace from hand-edited YAML.
	if !allowedOrigin(withOrigins("  example.com  "), req("https://example.com")) {
		t.Fatal("a padded allowlist entry did not match")
	}
	// An origin with an explicit port still matches on hostname.
	if !allowedOrigin(withOrigins("example.com"), req("https://example.com:8443")) {
		t.Fatal("an origin with a port was refused")
	}

	// Anything else is refused.
	for _, origin := range []string{
		"https://evil.example",
		"https://example.com.evil.test",
		"https://notexample.com",
	} {
		if allowedOrigin(withOrigins("example.com"), req(origin)) {
			t.Fatalf("origin %q was allowed", origin)
		}
	}
	// A malformed Origin is refused rather than guessed.
	if allowedOrigin(withOrigins("example.com"), req("http://[::1")) {
		t.Fatal("a malformed Origin was allowed")
	}

	// With no allowlist, strict same-origin public access and local development
	// across loopback ports pass; unrelated public origins do not.
	empty := withOrigins()
	if !allowedOrigin(empty, req("http://203.0.113.10:8000", "203.0.113.10:8000")) {
		t.Fatal("a same-origin public console was refused with an empty allowlist")
	}
	if !allowedOrigin(empty, req("http://localhost:5173", "127.0.0.1:8000")) {
		t.Fatal("loopback Vite origin was refused with an empty allowlist")
	}
	if allowedOrigin(empty, req("http://localhost:5173", "203.0.113.10:8000")) {
		t.Fatal("a localhost origin was allowed to reach a public host")
	}
	if allowedOrigin(empty, req("https://example.com", "203.0.113.10:8000")) {
		t.Fatal("a public origin was allowed with an empty allowlist")
	}
}
