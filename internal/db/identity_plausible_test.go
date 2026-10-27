package db

import (
	"encoding/base64"
	"testing"
)

// TestMaybeDecodeStoredIdentityKeepsPlausibleValues pins the other half of the
// heuristic: a value that is ALREADY a plausible identity must survive even when
// it happens to be valid base64. "DC01" is both a real Windows domain-controller
// name and a valid quartet; decoding it yields "\x0c-5", which trims to "-5".
// The damage was silent: the console showed a garbage hostname and
// hostname-based auto-tag rules stopped matching that agent.
func TestMaybeDecodeStoredIdentityKeepsPlausibleValues(t *testing.T) {
	wire := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	cases := []struct{ in, want string }{
		// Plain identities that look like base64.
		{"DC01", "DC01"},
		{"WEB1", "WEB1"},
		{"web01", "web01"},
		{"dc01.corp.local", "dc01.corp.local"},
		{"app-prod-01", "app-prod-01"},
		{"10.0.0.5", "10.0.0.5"},
		{"fe80::1", "fe80::1"},
		{"CORP\\alice", "CORP\\alice"},
		{"alice@corp.local", "alice@corp.local"},

		// Genuine wire form still decodes.
		{wire("dc01.corp.local"), "dc01.corp.local"},
		{wire("10.0.0.5"), "10.0.0.5"},
		{wire("CORP\\alice"), "CORP\\alice"},
		{wire("workstation-0042"), "workstation-0042"},
	}
	for _, tc := range cases {
		if got := maybeDecodeStoredIdentity(tc.in); got != tc.want {
			t.Errorf("maybeDecodeStoredIdentity(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPlausibleIdentity documents what the guard considers an identity.
func TestPlausibleIdentity(t *testing.T) {
	for _, in := range []string{"DC01", "web01", "10.0.0.5", "dc01.corp.local", `CORP\alice`, "alice@corp.local", "app-prod-01"} {
		if !plausibleIdentity(in) {
			t.Errorf("plausibleIdentity(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "a", "-5", ".hidden", "a b", "()"} {
		if plausibleIdentity(in) {
			t.Errorf("plausibleIdentity(%q) = true, want false", in)
		}
	}
}
