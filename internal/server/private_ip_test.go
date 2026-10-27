package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/scripting"
)

// TestIsPrivateIP pins the outbound-fetch guard used by the scripting HTTP
// bridge and the GeoIP lookup. It is the thing standing between a plugin and
// the server's own loopback services, so every bypass class is spelled out:
// the unspecified address (which dials the local host on Linux), IPv4-mapped
// IPv6, bracketed literals, and the ranges the string-prefix version missed.
func TestIsPrivateIP(t *testing.T) {
	private := []string{
		"", "localhost", "LOCALHOST",
		"127.0.0.1", "127.255.255.254",
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.1.1",
		"169.254.169.254", // cloud metadata
		"100.64.0.1",      // CGNAT
		"0.0.0.0",         // unspecified: dials the local host
		"::",
		"::1",
		"[::1]",
		"fe80::1",            // link-local
		"fc00::1", "fd00::1", // ULA
		"ff02::1",          // multicast
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"::ffff:10.0.0.1",  // IPv4-mapped private
		"::ffff:192.168.1.1",
	}
	for _, ip := range private {
		if !isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = false, want true", ip)
		}
	}

	public := []string{
		"8.8.8.8", "1.1.1.1", "203.0.113.9",
		"172.15.0.1", "172.32.0.1", // just outside RFC1918
		"100.63.255.255", "100.128.0.1", // just outside CGNAT
		"2001:4860:4860::8888",
		"93.184.216.34",
	}
	for _, ip := range public {
		if isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = true, want false", ip)
		}
	}

	// A bare hostname is not guessed here: the callers resolve it and re-check
	// every answer, so a name that resolves to a public address must pass this
	// stage.
	if isPrivateIP("example.com") {
		t.Error("isPrivateIP(example.com) = true, want false (resolved by the caller)")
	}
}

// TestScriptHTTPBridgeBlocksPrivateTargets is the end-to-end guard: the bridge
// refuses private hosts before any request is made.
func TestScriptHTTPBridgeBlocksPrivateTargets(t *testing.T) {
	s := mustTenantServer(t)
	b := &scriptingBridge{s: s}
	caller := scripting.Caller{Username: "admin1", Role: db.RoleAdmin}
	for _, target := range []string{
		"http://127.0.0.1:8000/health",
		"http://0.0.0.0:8000/health",
		"http://localhost:8000/",
		"http://[::1]:8000/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
	} {
		if _, err := b.HTTPRequest(caller, "GET", target, nil, "", 2); err == nil {
			t.Fatalf("bridge allowed %s", target)
		}
	}
}
