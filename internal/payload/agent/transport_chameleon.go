//go:build linux || windows || darwin

package main

import (
	"crypto/rand"
	"math/big"
	"strings"

	utls "github.com/refraction-networking/utls"
)

// JA3 / ClientHello rotation for every TLS transport that goes through the
// utls dialer (HTTPS/WSS/mTLS/gRPC/DoT). Disabled by default: a stable,
// realistic Chrome fingerprint is what a real browser presents. When enabled
// (compile-time ChameleonStr=true or runtime FORGEC2_CHAMELEON=1) the provider
// hands out a random profile per handshake, or the fixed profile named by
// ChameleonProfileStr / FORGEC2_CHAMELEON_PROFILE.
//
// This used to sit behind a `chameleon` build tag that no build path ever
// set, so the feature was dead weight in every real binary; the tag is gone
// and the wiring runs in the default build.
var ja3ProfileName string

type ja3ProfileEntry struct {
	Name string
	ID   utls.ClientHelloID
}

var ja3Profiles = []ja3ProfileEntry{
	{"chrome_120", utls.HelloChrome_120},
	{"chrome_120_pq", utls.HelloChrome_120_PQ},
	{"chrome_auto", utls.HelloChrome_Auto},
	{"firefox_120", utls.HelloFirefox_120},
	{"firefox_auto", utls.HelloFirefox_Auto},
	{"safari_16", utls.HelloSafari_16_0},
	{"safari_auto", utls.HelloSafari_Auto},
	{"edge_85", utls.HelloEdge_85},
	{"edge_auto", utls.HelloEdge_Auto},
	{"ios_14", utls.HelloIOS_14},
	{"ios_auto", utls.HelloIOS_Auto},
	{"android_11", utls.HelloAndroid_11_OkHttp},
	{"360_auto", utls.Hello360_Auto},
	{"qq_auto", utls.HelloQQ_Auto},
	{"randomized", utls.HelloRandomized},
	{"randomized_alpn", utls.HelloRandomizedALPN},
}

// ja3ShortNames maps the operator-facing profile names documented for the
// generate config onto the full utls profile entries above.
var ja3ShortNames = map[string]string{
	"chrome":  "chrome_auto",
	"firefox": "firefox_auto",
	"safari":  "safari_auto",
	"edge":    "edge_auto",
	"ios":     "ios_auto",
	"android": "android_11",
	"360":     "360_auto",
	"qq":      "qq_auto",
}

// initJA3Rotation wires the utls ClientHello provider; called from main() once
// the config and environment are parsed. With rotation disabled the provider
// stays unregistered and utlsClientHello() falls back to HelloChrome_Auto.
func initJA3Rotation(profile string, enabled bool) {
	if !enabled {
		ja3HelloProvider = nil
		return
	}
	ja3ProfileName = profile
	ja3HelloProvider = func() utls.ClientHelloID {
		return getUTLSHelloID(getJA3Profile())
	}
}

func getJA3Profile() string {
	base := ja3ProfileName
	// In random mode, pick a fresh ClientHello on every dial so the JA3
	// fingerprint varies per connection (instead of being pinned for the whole
	// session, which is itself a high-fidelity static IOC). Fixed profiles
	// stay stable.
	if base == "random" || base == "randomized" {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(ja3Profiles))))
		if err != nil {
			return "chrome_auto"
		}
		return ja3Profiles[n.Int64()].Name
	}
	return base
}

func getUTLSHelloID(profile string) utls.ClientHelloID {
	if profile == "random" || profile == "" {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(ja3Profiles))))
		if err != nil {
			return utls.HelloChrome_Auto
		}
		return ja3Profiles[n.Int64()].ID
	}
	if mapped, ok := ja3ShortNames[strings.ToLower(profile)]; ok {
		profile = mapped
	}
	for _, p := range ja3Profiles {
		if p.Name == profile {
			return p.ID
		}
	}
	return utls.HelloChrome_Auto
}

// configuredTLSProfile reports the fingerprint this build will use.
//
// It is the CONFIGURED profile, not a measured one: the server never sees a client hello, so claiming a JA3 hash from here would
// be an assertion nobody can check. What can be proven is which uTLS preset the binary was built with - and that is the question
// an operator actually asks ("is this beacon pretending to be Chrome, and is it the version I think").
func configuredTLSProfile() string {
	if ja3ProfileName != "" {
		return ja3ProfileName
	}
	// Rotation disabled: the dialer falls back to Chrome Auto, so say that rather than reporting nothing.
	return "chrome_auto (default)"
}
