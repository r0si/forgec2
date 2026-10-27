package server

import (
	"strings"
	"testing"
)

// TestValidateWebhookURLAddressPolicy pins the webhook validator's use of the
// shared address policy. The previous copy ignored DNS resolution failures —
// a host that could not be resolved at validation time was accepted and then
// dialled once it resolved — and it did not reject multicast or CGNAT targets.
func TestValidateWebhookURLAddressPolicy(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/hook",
		"http://0.0.0.0:8000/hook",
		"http://localhost/hook",
		"http://[::1]/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/hook",
		"http://192.168.0.10/hook",
		"http://172.16.4.4/hook",
		"http://100.64.0.1/hook", // CGNAT
		"http://224.0.0.1/hook",  // multicast
		"http://[fe80::1]/hook",  // link-local v6
		"http://[fd00::1]/hook",  // ULA
		"http://metadata.google.internal/hook",
	}
	for _, u := range blocked {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("validateWebhookURL(%q) = nil, want an error", u)
		}
	}

	// A name that does not resolve is refused rather than trusted.
	if err := validateWebhookURL("http://does-not-exist.invalid/hook"); err == nil {
		t.Error("an unresolvable hostname was accepted")
	}

	// Scheme enforcement still applies after the address check.
	if err := validateWebhookURL("ftp://example.com/hook"); err == nil {
		t.Error("a non-http scheme was accepted")
	}
	if err := validateWebhookURL("not a url"); err == nil {
		t.Error("garbage was accepted")
	}
}

// TestNonPublicHostError covers the shared helper directly, including the
// boundaries that a "starts with 172."-style check gets wrong.
func TestNonPublicHostError(t *testing.T) {
	for _, host := range []string{
		"127.0.0.1", "0.0.0.0", "::", "::1", "::ffff:127.0.0.1", "[::1]",
		"10.1.2.3", "192.168.1.1", "172.16.0.1", "172.31.255.255",
		"169.254.169.254", "100.64.0.1", "100.127.255.255",
		"fe80::1", "fc00::1", "ff02::1", "localhost",
	} {
		if err := nonPublicHostError(host); err == nil {
			t.Errorf("nonPublicHostError(%q) = nil, want an error", host)
		}
	}
	for _, host := range []string{
		"8.8.8.8", "1.1.1.1", "203.0.113.9",
		"172.15.0.1", "172.32.0.1", "100.63.255.255", "100.128.0.1",
		"2001:4860:4860::8888",
	} {
		if err := nonPublicHostError(host); err != nil {
			t.Errorf("nonPublicHostError(%q) = %v, want nil", host, err)
		}
	}
	if strings.Contains(nonPublicHostError("10.0.0.1").Error(), "10.0.0.1") == false {
		t.Error("the error should name the offending address")
	}
}
