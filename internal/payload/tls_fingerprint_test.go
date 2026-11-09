package payload

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNormalizeTLSFingerprint pins the option's vocabulary. An unknown value is
// an error, not a silent fallback: the failure this option exists to prevent is
// "operator asked for rotation, build shipped a stable fingerprint".
func TestNormalizeTLSFingerprint(t *testing.T) {
	stable := []string{"", "off", "none", "stable", "STABLE", " stable "}
	for _, value := range stable {
		enabled, profile, err := NormalizeTLSFingerprint(value)
		if err != nil || enabled || profile != "" {
			t.Fatalf("%q -> enabled=%v profile=%q err=%v, want stable", value, enabled, profile, err)
		}
	}
	for _, value := range []string{"random", "rotate", "randomized", "RANDOM"} {
		enabled, profile, err := NormalizeTLSFingerprint(value)
		if err != nil || !enabled || profile != TLSFingerprintRandom {
			t.Fatalf("%q -> enabled=%v profile=%q err=%v, want random rotation", value, enabled, profile, err)
		}
	}
	for _, value := range []string{"chrome", "firefox", "safari", "edge", "ios", "android", "360", "qq"} {
		enabled, profile, err := NormalizeTLSFingerprint(value)
		if err != nil || !enabled || profile != value {
			t.Fatalf("%q -> enabled=%v profile=%q err=%v, want pinned profile", value, enabled, profile, err)
		}
	}
	if _, _, err := NormalizeTLSFingerprint("opera"); err == nil {
		t.Fatal("an unknown profile must be rejected")
	}
	if _, _, err := NormalizeTLSFingerprint("ja4"); err == nil {
		t.Fatal("a bare hash name must be rejected rather than treated as a profile")
	}
}

// TestConfigBlobCarriesTLSFingerprint proves the option reaches the agent: the
// blob is the only injection path for network config, and the agent applies it
// before the first handshake.
func TestConfigBlobCarriesTLSFingerprint(t *testing.T) {
	cases := []struct {
		option   string
		wantFlag string
		wantProf string
	}{
		{"", "false", ""},
		{"stable", "false", ""},
		{"random", "true", "random"},
		{"firefox", "true", "firefox"},
	}
	for _, tc := range cases {
		t.Run("option="+tc.option, func(t *testing.T) {
			cfg := ImplantConfig{
				C2URL: "https://c2.example", Protocol: "https", BeaconTransport: "https",
				Interval: 15, Jitter: 30, TLSFingerprint: tc.option,
			}
			raw := marshalConfigBlobJSON(cfg, MalleableProfile{})
			if raw == nil {
				t.Fatal("blob marshal failed")
			}
			var got agentConfigJSON
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("blob is not JSON: %v", err)
			}
			if got.Chameleon != tc.wantFlag || got.ChameleonProfile != tc.wantProf {
				t.Fatalf("chameleon=%q profile=%q, want %q/%q",
					got.Chameleon, got.ChameleonProfile, tc.wantFlag, tc.wantProf)
			}
		})
	}
}

// TestBootstrapBlobCarriesTLSFingerprint holds the same contract for
// config-over-wire builds: the fingerprint is chosen at build time and cannot
// arrive later, so the bootstrap blob must carry it.
func TestBootstrapBlobCarriesTLSFingerprint(t *testing.T) {
	cfg := ImplantConfig{
		C2URL: "https://c2.example", Protocol: "https", BeaconTransport: "https",
		NetworkConfigOverWire: true, TLSFingerprint: "random",
	}
	raw := marshalConfigBlobJSON(cfg, MalleableProfile{})
	if raw == nil {
		t.Fatal("blob marshal failed")
	}
	var got agentConfigJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("blob is not JSON: %v", err)
	}
	if got.Chameleon != "true" || got.ChameleonProfile != "random" {
		t.Fatalf("bootstrap blob chameleon=%q profile=%q, want true/random",
			got.Chameleon, got.ChameleonProfile)
	}
	if strings.Contains(string(raw), "chameleon_profile\":\"chrome_auto\"") {
		t.Fatalf("unexpected default profile in blob: %s", raw)
	}
}
