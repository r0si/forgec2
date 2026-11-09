package server

import (
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/payload"
)

// TestGenerateCacheKeyDistinguishesTLSFingerprint keeps a build that asks for
// ClientHello rotation from being served the cached stable-fingerprint binary.
// The cache returns the artifact bytes, so a missing field is a silent
// "option did nothing".
func TestGenerateCacheKeyDistinguishesTLSFingerprint(t *testing.T) {
	stable := &payload.ImplantConfig{Architecture: "amd64", C2URL: "https://c2.example", TLSFingerprint: "stable"}
	random := &payload.ImplantConfig{Architecture: "amd64", C2URL: "https://c2.example", TLSFingerprint: "random"}
	firefox := &payload.ImplantConfig{Architecture: "amd64", C2URL: "https://c2.example", TLSFingerprint: "firefox"}

	k1 := GenerateCacheKey(stable, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(random, "windows", "exe", "go1.26.0", "")
	k3 := GenerateCacheKey(firefox, "windows", "exe", "go1.26.0", "")
	if k1 == k2 || k2 == k3 || k1 == k3 {
		t.Fatalf("TLS fingerprint modes share a cache key: stable=%s random=%s firefox=%s", k1, k2, k3)
	}
}

// TestGenerateCacheKeyDistinguishesArchitecture verifies that two builds with
// the same C2 URL but different target architectures produce different cache
// keys. A false cache hit here would hand an operator an ARM64 binary when they
// asked for amd64 — a silent deployment failure.
func TestGenerateCacheKeyDistinguishesArchitecture(t *testing.T) {
	amd64Cfg := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
	}
	arm64Cfg := &payload.ImplantConfig{
		Architecture: "arm64",
		C2URL:        "https://c2.example",
	}

	k1 := GenerateCacheKey(amd64Cfg, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(arm64Cfg, "windows", "exe", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("different architectures produced the same cache key — false cache hit risk")
	}
}

// TestGenerateCacheKeyDistinguishesObfuscate verifies that obfuscation on/off
// produces different keys. An obfuscated binary and a plain binary are not
// interchangeable.
func TestGenerateCacheKeyDistinguishesObfuscate(t *testing.T) {
	plain := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		Obfuscate:    false,
	}
	obfuscated := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		Obfuscate:    true,
	}

	k1 := GenerateCacheKey(plain, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(obfuscated, "windows", "exe", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("obfuscate on/off produced the same cache key")
	}
}

// TestGenerateCacheKeyDistinguishesSigning verifies that different signing
// configurations produce different keys. A signed binary and an unsigned binary
// are not interchangeable.
func TestGenerateCacheKeyDistinguishesSigning(t *testing.T) {
	unsigned := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		SignMode:     "none",
	}
	signed := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		SignMode:     "sign",
		SignOrg:      "TestOrg",
	}

	k1 := GenerateCacheKey(unsigned, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(signed, "windows", "exe", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("different signing produced the same cache key")
	}
}

// TestGenerateCacheKeyDistinguishesUPX verifies that UPX on/off produces
// different keys. A packed binary and an unpacked binary are not
// interchangeable.
func TestGenerateCacheKeyDistinguishesUPX(t *testing.T) {
	unpacked := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		UPX:          false,
	}
	packed := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		UPX:          true,
	}

	k1 := GenerateCacheKey(unpacked, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(packed, "windows", "exe", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("UPX on/off produced the same cache key")
	}
}

// TestGenerateCacheKeyDistinguishesPlatform verifies that different target
// platforms produce different keys.
func TestGenerateCacheKeyDistinguishesPlatform(t *testing.T) {
	cfg := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
	}

	k1 := GenerateCacheKey(cfg, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(cfg, "linux", "elf", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("windows/linux produced the same cache key")
	}
}

// TestGenerateCacheKeyDistinguishesC2URL verifies that different C2 URLs
// produce different keys. Two listeners with different URLs produce
// different binaries.
func TestGenerateCacheKeyDistinguishesC2URL(t *testing.T) {
	cfg1 := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2-a.example",
	}
	cfg2 := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2-b.example",
	}

	k1 := GenerateCacheKey(cfg1, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(cfg2, "windows", "exe", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("different C2 URLs produced the same cache key")
	}
}

// TestGenerateCacheKeySameConfig verifies that identical configs produce
// identical keys (determinism).
func TestGenerateCacheKeySameConfig(t *testing.T) {
	cfg := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
		Obfuscate:    true,
		UPX:          true,
	}

	k1 := GenerateCacheKey(cfg, "windows", "exe", "go1.26.0", "")
	k2 := GenerateCacheKey(cfg, "windows", "exe", "go1.26.0", "")

	if k1 != k2 {
		t.Fatal("same config produced different cache keys — non-deterministic")
	}
}

// TestGenerateCacheKeyDifferentGoVersion verifies that different Go versions
// produce different keys. A binary built with go1.25 and go1.26 are not
// guaranteed to be byte-identical.
func TestGenerateCacheKeyDifferentGoVersion(t *testing.T) {
	cfg := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
	}

	k1 := GenerateCacheKey(cfg, "windows", "exe", "go1.25.0", "")
	k2 := GenerateCacheKey(cfg, "windows", "exe", "go1.26.0", "")

	if k1 == k2 {
		t.Fatal("different Go versions produced the same cache key")
	}
}

// TestGenerateCacheKeyIsStableHash verifies the key is a valid 32-char hex
// string.
func TestGenerateCacheKeyIsStableHash(t *testing.T) {
	cfg := &payload.ImplantConfig{
		Architecture: "amd64",
		C2URL:        "https://c2.example",
	}
	key := GenerateCacheKey(cfg, "windows", "exe", "go1.26.0", "")

	if len(key) != 32 {
		t.Fatalf("cache key length = %d, want 32", len(key))
	}
	if strings.ContainsAny(key, "ghijklmnopqrstuvwxyz") {
		t.Fatalf("cache key contains non-hex characters: %s", key)
	}
}
