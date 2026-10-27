package config

import "testing"

// TestAllowedOriginKeepsCaseInsensitiveMatch pins the config-level helper: it is
// deny-by-default with an empty list, and it matches hostnames
// case-insensitively (RFC 4343), so an operator who typed capitals into
// allowed_origins is not silently locked out of their own console.
func TestAllowedOriginKeepsCaseInsensitiveMatch(t *testing.T) {
	cfg := &Config{}
	if cfg.AllowedOrigin("example.com") {
		t.Fatal("an empty allowlist allowed a host")
	}

	cfg.Server.AllowedOrigins = []string{"Example.COM", " second.example "}
	if !cfg.AllowedOrigin("example.com") {
		t.Fatal("a capitalised allowlist entry did not match")
	}
	if !cfg.AllowedOrigin("EXAMPLE.COM") {
		t.Fatal("a capitalised host did not match")
	}
	if !cfg.AllowedOrigin("second.example") {
		t.Fatal("a padded allowlist entry did not match")
	}
	if cfg.AllowedOrigin("evil.test") {
		t.Fatal("an unlisted host was allowed")
	}
	// A suffix of an allowed name is not an allowed name.
	if cfg.AllowedOrigin("example.com.evil.test") {
		t.Fatal("a suffix lookalike was allowed")
	}
}
