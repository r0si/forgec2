package server

import (
	"strings"
	"testing"
)

// TestValidateExternalURLPortPolicy pins the port policy exactly as it behaves.
// The comments used to claim a default deny for non-allowlisted ports while the
// code allowed every port above 1024 — a reader would have believed in a
// protection that was not there. The test makes the real policy explicit so a
// future tightening is a deliberate, visible change.
func TestValidateExternalURLPortPolicy(t *testing.T) {
	// Allowlisted service ports pass.
	for _, u := range []string{
		"https://example.com/",
		"http://example.com:80/",
		"https://example.com:443/",
		"http://ai.internal.example:8000/v1/chat",
		"http://ai.internal.example:11434/api/generate",
		"http://ai.internal.example:3000/v1",
		"http://ai.internal.example:5000/v1",
		"http://ai.internal.example:9000/v1",
	} {
		// Resolvable public host: use an IP literal so the test needs no DNS.
		u = strings.Replace(u, "example.com", "203.0.113.9", 1)
		u = strings.Replace(u, "ai.internal.example", "203.0.113.9", 1)
		if err := validateExternalURL(u); err != nil {
			t.Errorf("validateExternalURL(%q) = %v, want nil", u, err)
		}
	}

	// Other high ports are allowed: AI gateways legitimately use them.
	for _, u := range []string{
		"http://203.0.113.9:1234/v1",
		"http://203.0.113.9:8081/v1",
		"http://203.0.113.9:65535/v1",
	} {
		if err := validateExternalURL(u); err != nil {
			t.Errorf("validateExternalURL(%q) = %v, want nil (high ports are not deny-by-default)", u, err)
		}
	}

	// Sensitive service ports and privileged ports are refused.
	for _, u := range []string{
		"http://203.0.113.9:22/",
		"http://203.0.113.9:23/",
		"http://203.0.113.9:25/",
		"http://203.0.113.9:3306/",
		"http://203.0.113.9:5432/",
		"http://203.0.113.9:6379/",
		"http://203.0.113.9:27017/",
		"http://203.0.113.9:11211/",
		"http://203.0.113.9:808/",   // privileged, not 80/443
		"http://203.0.113.9:1023/",  // just below the boundary
		"http://203.0.113.9:0/",     // invalid
		"http://203.0.113.9:70000/", // out of range
	} {
		if err := validateExternalURL(u); err == nil {
			t.Errorf("validateExternalURL(%q) = nil, want an error", u)
		}
	}

	// Scheme and host checks still apply.
	for _, u := range []string{"ftp://203.0.113.9/", "gopher://203.0.113.9/", "http:///x", "http://127.0.0.1/", "http://[::1]/"} {
		if err := validateExternalURL(u); err == nil {
			t.Errorf("validateExternalURL(%q) = nil, want an error", u)
		}
	}
}
