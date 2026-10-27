package server

import (
	"errors"
	"testing"
)

// TestUpdateSignaturePolicy pins the fail-closed contract that AGENTS.md and the release
// workflow both rely on: with crypto.require_release_signature set and no key to verify
// with, a hot update must be REFUSED rather than silently falling back to a checksum from
// the same origin it is downloading from.
func TestUpdateSignaturePolicy(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		require bool
		want    bool
		wantErr bool
	}{
		{"key configured", "abcd", false, true, false},
		{"key configured, signature required", "abcd", true, true, false},
		{"no key, signature required", "", true, false, true},
		{"no key, signature required (whitespace)", "   ", true, false, true},
		{"no key, signature optional", "", false, false, false},
		{"no key, signature optional (whitespace)", "  ", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := updateSignatureEnforced(tc.key, tc.require)
			if got != tc.want {
				t.Fatalf("enforce = %v, want %v", got, tc.want)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("a missing key with require_release_signature set was accepted")
				}
				if !errors.Is(err, errUpdateSignatureRequired) {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
