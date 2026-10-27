package db

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

func hasMixedCase(s string) bool {
	hasU, hasL := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			hasU = true
		}
		if c >= 'a' && c <= 'z' {
			hasL = true
		}
		if hasU && hasL {
			return true
		}
	}
	return false
}

// maybeDecodeStoredIdentity undoes implant base64 identity fields that were
// persisted without decoding. Plain hostnames/IPs contain '.', '-', ':' so
// they fail the base64 charset check. EncodeToString of typical names is the
// base64 alphabet; when it has no padding (len%3==0) we still accept mixed
// case wire form decoding to a non-mixed identity, or a decoded IP/host
// with '.', '-', ':' or '\\'.
// plausibleIdentity reports whether s could be a real hostname, username or IP
// on its own. It is what stops the decoder from mangling values that merely
// look like base64: "DC01" is valid base64, and decoding it yields "\x0c-5"
// which trims to "-5" — a common Windows domain-controller name would then be
// displayed as garbage and silently stop matching hostname-based rules.
func plausibleIdentity(s string) bool {
	if len(s) < 3 {
		return false
	}
	first := s[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return false
	}
	letters := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			letters++
		case c == '.', c == '-', c == '_', c == '\\', c == ':', c == '@', c == '$':
		default:
			return false
		}
	}
	return letters >= 2
}

func maybeDecodeStoredIdentity(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v)%4 != 0 || len(v) < 4 {
		return v
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
		default:
			return v
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(decoded) == 0 || !utf8.Valid(decoded) {
		return v
	}
	s := strings.TrimSpace(string(decoded))
	for _, r := range s {
		if r < 32 || r == 127 {
			return v
		}
	}
	// Only accept a decoded value that could itself be an identity; anything
	// else (junk such as "-5" from "DC01") means the original was not wire form.
	if !plausibleIdentity(s) {
		return v
	}
	// Padding or the wire-only alphabet is proof of wire form.
	if strings.ContainsAny(v, "+/=") {
		return s
	}
	// Without that proof, the decode must carry structure a plain name lacks:
	// an address or path separator. Otherwise "WEB1" would decode to "X@u".
	if strings.ContainsAny(s, ".:\\") {
		return s
	}
	if hasMixedCase(v) && !hasMixedCase(s) {
		return s
	}
	return v
}

// AfterFind restores operator-facing hostname/username/IP when a prior
// check-in stored the implant's base64 wire form.
func (i *Implant) AfterFind(_ *gorm.DB) error {
	i.Hostname = maybeDecodeStoredIdentity(i.Hostname)
	i.Username = maybeDecodeStoredIdentity(i.Username)
	i.IP = maybeDecodeStoredIdentity(i.IP)
	return nil
}
