//go:build windows

package main

import (
	"encoding/binary"
	"testing"
)

// ntlmNegotiate is a minimal valid Type-1 (negotiate) NTLMSSP header.
func ntlmType3(base []byte) []byte {
	d := make([]byte, len(base))
	copy(d, base)
	binary.LittleEndian.PutUint32(d[8:12], 3) // msgType = Authenticate
	return d
}

// TestParseNTLMAuthMalformedIsBounded locks the security invariant that parsing
// a victim-controlled NTLM Type-3 Authenticate message never panics -- a panic
// here is a remote DoS of the relay handler (which parses untrusted bytes on a
// live connection). The function must return nil on every malformed / truncated
// / adversarial offset combination. This is the only coverage for the relay's
// parse path, which is //go:build windows and therefore never executed by the
// Linux CI agent leg.
func TestParseNTLMAuthMalformedIsBounded(t *testing.T) {
	// header signature + a 64-byte body room for the field offsets.
	sig := []byte("NTLMSSP\x00")
	cases := []struct {
		name  string
		build func() []byte
	}{
		{"too short", func() []byte { return sig[:4] }},
		{"empty", func() []byte { return nil }},
		{"wrong type", func() []byte {
			d := make([]byte, 64)
			copy(d, sig)
			binary.LittleEndian.PutUint32(d[8:12], 1) // negotiate, not auth
			return d
		}},
		{"offsets point past end", func() []byte {
			d := ntlmType3(make([]byte, 64))
			copy(d, sig)
			// userOff huge, userLen nonzero -> off+len > len -> must reject
			binary.LittleEndian.PutUint16(d[36:38], 200)    // userLen
			binary.LittleEndian.PutUint32(d[40:44], 100000) // userOff
			return d
		}},
		{"len fields at max uint16", func() []byte {
			d := ntlmType3(make([]byte, 64))
			copy(d, sig)
			binary.LittleEndian.PutUint16(d[12:14], 0xFFFF) // lmRespLen
			binary.LittleEndian.PutUint16(d[20:22], 0xFFFF) // ntRespLen
			binary.LittleEndian.PutUint16(d[28:30], 0xFFFF) // domainLen
			binary.LittleEndian.PutUint16(d[36:38], 0xFFFF) // userLen
			return d
		}},
		{"truncated mid-header", func() []byte {
			d := ntlmType3(make([]byte, 40)) // < 64 -> rejected
			copy(d, sig)
			return d
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No panic is the assertion; parseNTLMAuth must return nil for all
			// malformed inputs (a valid capture would return non-nil).
			res := parseNTLMAuth(tc.build())
			if res != nil {
				t.Errorf("parseNTLMAuth returned non-nil for malformed input %q: %+v", tc.name, res)
			}
		})
	}
}
