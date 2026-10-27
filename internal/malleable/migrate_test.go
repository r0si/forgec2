package malleable

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMigrateProfileJSONPicksTheRightShape pins the path detection, which is the
// whole job of this function: a v2 profile must not be pushed through the legacy
// converter, and a legacy http_get/http_post profile must not be read as v2 —
// either mistake silently produces an implant with the wrong URIs or transforms.
func TestMigrateProfileJSONPicksTheRightShape(t *testing.T) {
	// v2: identified by any v2-only key (here "uris").
	v2Raw := `{"name":"v2prof","uris":["/api/v2/beacon"],"method":"POST","sleep":30}`
	v2, err := MigrateProfileJSON([]byte(v2Raw), "fallback")
	if err != nil {
		t.Fatalf("v2 migrate: %v", err)
	}
	if v2.Name != "v2prof" {
		t.Fatalf("v2 name = %q", v2.Name)
	}
	if len(v2.URIs) != 1 || v2.URIs[0] != "/api/v2/beacon" {
		t.Fatalf("v2 uris = %v", v2.URIs)
	}

	// Legacy http_get/http_post: identified by those keys, and its GET URI must
	// survive the conversion.
	legacyRaw := `{"name":"legacyprof","http_get":{"uri":["/legacy/get"]},"http_post":{"uri":["/legacy/post"]}}`
	legacy, err := MigrateProfileJSON([]byte(legacyRaw), "fallback")
	if err != nil {
		t.Fatalf("legacy migrate: %v", err)
	}
	if legacy.Name != "legacyprof" {
		t.Fatalf("legacy name = %q", legacy.Name)
	}
	if !strings.Contains(strings.Join(legacy.URIs, ",")+strings.Join(legacy.BeaconURIs, ","), "/legacy/get") {
		t.Fatalf("the legacy GET URI was lost: uris=%v beacon_uris=%v", legacy.URIs, legacy.BeaconURIs)
	}

	// v1 payload JSON: neither v2-only keys nor http_get/http_post, so it takes
	// the v1 path — and its wire-form transforms must be parsed, not copied raw.
	v1Raw := `{"name":"v1prof","user_agent":"Mozilla/5.0","beacon_uri":"/v1/beacon","method":"POST","sleep":60,"jitter":20,"server_output":"base64","client_metadata":"base64"}`
	v1, err := MigrateProfileJSON([]byte(v1Raw), "fallback")
	if err != nil {
		t.Fatalf("v1 migrate: %v", err)
	}
	if v1.Name != "v1prof" || v1.BeaconURI != "/v1/beacon" || v1.Sleep != 60 || v1.Jitter != 20 {
		t.Fatalf("v1 fields = %+v", v1)
	}
	if len(v1.ServerOutput) == 0 || v1.ServerOutput[0].Type != "base64" {
		t.Fatalf("server_output wire was not parsed: %+v", v1.ServerOutput)
	}
	if len(v1.ClientMetadata) == 0 || v1.ClientMetadata[0].Type != "base64" {
		t.Fatalf("client_metadata wire was not parsed: %+v", v1.ClientMetadata)
	}

	// The fallback name applies whenever the JSON has none.
	nameless, err := MigrateProfileJSON([]byte(`{"uris":["/x"]}`), "from-fallback")
	if err != nil {
		t.Fatalf("nameless migrate: %v", err)
	}
	if nameless.Name != "from-fallback" {
		t.Fatalf("fallback name = %q", nameless.Name)
	}

	// Invalid JSON is a hard error, not a silent empty profile.
	if _, err := MigrateProfileJSON([]byte(`{`), "x"); err == nil {
		t.Fatal("invalid JSON was accepted")
	}

	// A profile that parses but fails validation returns both the profile and
	// the error, so the editor can still show what was read.
	bad, err := MigrateProfileJSON([]byte(`{"name":"has space","uris":["/x"]}`), "x")
	if err == nil {
		t.Fatal("an invalid profile name was accepted")
	}
	if bad == nil {
		t.Fatal("the partially-parsed profile was dropped")
	}
}

// TestHasAnyKey is the tiny predicate the detection above depends on.
func TestHasAnyKey(t *testing.T) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"a":1,"b":2}`), &probe); err != nil {
		t.Fatal(err)
	}
	if !hasAnyKey(probe, "zzz", "b") {
		t.Fatal("an existing key was not found")
	}
	if hasAnyKey(probe, "zzz", "yyy") {
		t.Fatal("a missing key was reported present")
	}
	if hasAnyKey(probe) {
		t.Fatal("an empty key list matched")
	}
}

// TestFromLegacyProfile covers the converter directly, including the nil guard.
func TestFromLegacyProfile(t *testing.T) {
	if got := FromLegacyProfile(nil); got == nil || got.Name != "" {
		t.Fatalf("FromLegacyProfile(nil) = %+v", got)
	}

	legacy := &Profile{
		Name:        "conv",
		Description: "desc",
		HTTPGet:     HTTPGet{URI: []string{"/a", "/b"}},
		HTTPPost:    HTTPPost{URI: []string{"/c"}},
		HTTPConfig:  HTTPConfig{UserAgent: "UA/1.0"},
	}
	v2 := FromLegacyProfile(legacy)
	if v2.Name != "conv" || v2.Description != "desc" {
		t.Fatalf("v2 = %+v", v2)
	}
	if v2.UserAgent != "UA/1.0" {
		t.Fatalf("user agent = %q", v2.UserAgent)
	}
	joined := strings.Join(v2.URIs, ",") + strings.Join(v2.BeaconURIs, ",")
	for _, want := range []string{"/a", "/b", "/c"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("URI %q was dropped: %v", want, joined)
		}
	}
}
