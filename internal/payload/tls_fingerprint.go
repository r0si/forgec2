package payload

import (
	"fmt"
	"strings"
)

// TLS fingerprint options for generated implants.
//
// JA3 and JA4 are both derived from the same TLS ClientHello (cipher suites,
// extensions, curves, ALPN, SNI presence), so a ClientHello change moves both
// hashes. That is why this is one option, not two: "randomize JA3 but keep JA4
// stable" is not something a client can honestly do. The agent implements it
// with uTLS browser profiles, one per handshake in random mode.
const (
	// TLSFingerprintStable keeps the fixed Chrome Auto hello the shipped
	// builds have always presented.
	TLSFingerprintStable = "stable"
	// TLSFingerprintRandom picks a random browser profile per handshake.
	TLSFingerprintRandom = "random"
)

// tlsFingerprintProfiles are the named, stable profiles an operator can pin
// instead of rotating. They mirror the agent's short profile names.
var tlsFingerprintProfiles = map[string]bool{
	"chrome": true, "firefox": true, "safari": true, "edge": true,
	"ios": true, "android": true, "360": true, "qq": true,
}

// NormalizeTLSFingerprint validates the operator-facing option and returns the
// agent's (enabled, profile) pair. An unknown value is an error rather than a
// silent fall back to stable: a typo that ships a stable fingerprint is exactly
// the failure this option exists to prevent.
func NormalizeTLSFingerprint(value string) (enabled bool, profile string, err error) {
	switch v := strings.ToLower(strings.TrimSpace(value)); v {
	case "", "off", "none", TLSFingerprintStable:
		return false, "", nil
	case TLSFingerprintRandom, "rotate", "randomized":
		return true, TLSFingerprintRandom, nil
	}
	if tlsFingerprintProfiles[strings.ToLower(strings.TrimSpace(value))] {
		return true, strings.ToLower(strings.TrimSpace(value)), nil
	}
	return false, "", fmt.Errorf("tls_fingerprint must be empty, %q, %q, or one of chrome, firefox, safari, edge, ios, android, 360, qq",
		TLSFingerprintStable, TLSFingerprintRandom)
}

// TLSFingerprintValid reports whether a value can be used, for request
// validation paths that only need a boolean.
func TLSFingerprintValid(value string) bool {
	_, _, err := NormalizeTLSFingerprint(value)
	return err == nil
}

// tlsFingerprintBlobValues returns the agent config-blob values for this build.
// Unknown values fall back to stable here because the request layer rejects
// them first; the blob builder must never fail a build over an option that was
// already validated.
func (c ImplantConfig) tlsFingerprintBlobValues() (chameleon, profile string) {
	enabled, profile, err := NormalizeTLSFingerprint(c.TLSFingerprint)
	if err != nil || !enabled {
		return "false", ""
	}
	return "true", profile
}
