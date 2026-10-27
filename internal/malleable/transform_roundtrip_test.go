package malleable

import (
	"bytes"
	"testing"
)

// TestTransformRoundTrips pins the property the profile editor relies on: for a
// single-step chain, decode(encode(x)) == x. The editor validates a whole
// server_output chain at save time, so a transform that quietly loses data shows up
// as a confusing "your profile does not round-trip" error instead of "case is
// lossy" — this test names the culprit.
func TestTransformRoundTrips(t *testing.T) {
	payloads := map[string][]byte{
		"empty":        {},
		"ascii":        []byte("GET /beacon HTTP/1.1"),
		"mixed case":   []byte("AbCdEf 0123456789"),
		"binary":       {0x00, 0x01, 0x7f, 0x80, 0xfe, 0xff, 'A', 'b'},
		"padded tail":  []byte("data=="),
		"with markers": []byte("%%%__keep__%%%"),
	}

	// Round-trips on any payload.
	lossless := []Transform{
		{Type: "base64"},
		{Type: "base64url"},
		{Type: "netbios"},
		{Type: "netbiosu"},
		{Type: "mask", Value: "0x42"},
		{Type: "xor", Value: "key"},
		{Type: "urlencode"},
		{Type: "append", Value: "TAIL"},
		{Type: "prepend", Value: "HEAD"},
	}
	for _, tr := range lossless {
		for name, payload := range payloads {
			tb := &TransformBlock{Transforms: []Transform{tr}}
			encoded, err := tb.Apply(payload, true)
			if err != nil {
				t.Errorf("%s/%s: encode: %v", tr.Type, name, err)
				continue
			}
			decoded, err := tb.Apply(encoded, false)
			if err != nil {
				t.Errorf("%s/%s: decode: %v", tr.Type, name, err)
				continue
			}
			if !bytes.Equal(decoded, payload) {
				t.Errorf("%s/%s: round-trip changed the payload\n in: %x\nout: %x", tr.Type, name, payload, decoded)
			}
		}
	}

	// Deliberately lossy or conditional transforms: assert what they actually do so
	// the behaviour is pinned rather than assumed, and the message says which.
	t.Run("case is lossy by design", func(t *testing.T) {
		tb := &TransformBlock{Transforms: []Transform{{Type: "case"}}}
		encoded, err := tb.Apply([]byte("AbC"), true)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != "ABC" {
			t.Fatalf("case encode = %q", encoded)
		}
		decoded, err := tb.Apply(encoded, false)
		if err != nil {
			t.Fatal(err)
		}
		// Uppercasing then lowercasing cannot restore the original case; profiles
		// that need it must not use "case" and expect a round-trip.
		if string(decoded) != "abc" {
			t.Fatalf("case decode = %q, want the lowercased form", decoded)
		}
	})

	t.Run("identity transforms leave data alone", func(t *testing.T) {
		for _, typ := range []string{"header", "parameter", "unknown-type"} {
			tb := &TransformBlock{Transforms: []Transform{{Type: typ, Value: "x"}}}
			for _, encode := range []bool{true, false} {
				got, err := tb.Apply([]byte("payload"), encode)
				if err != nil {
					t.Fatalf("%s: %v", typ, err)
				}
				if string(got) != "payload" {
					t.Fatalf("%s (encode=%v) = %q", typ, encode, got)
				}
			}
		}
	})

	t.Run("multi-step chains reverse in order", func(t *testing.T) {
		// base64 -> append must decode as strip-suffix -> base64, not the reverse.
		tb := &TransformBlock{Transforms: []Transform{
			{Type: "base64"},
			{Type: "append", Value: "\n"},
		}}
		payload := []byte("multi-step payload")
		encoded, err := tb.Apply(payload, true)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := tb.Apply(encoded, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("multi-step round-trip = %q, want %q (encode %q)", decoded, payload, encoded)
		}
	})
}
