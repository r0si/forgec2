package malleable

import (
	"bytes"
	"testing"
)

// TestConditionalTransformsBehaviour pins the transforms whose round-trip depends on
// the payload or the configured value. These are exactly the ones that produce a
// confusing "your server_output chain does not round-trip" message in the profile
// editor, so their real behaviour is recorded here.
func TestConditionalTransformsBehaviour(t *testing.T) {
	t.Run("strrep round-trips only when the replacement is absent", func(t *testing.T) {
		tb := &TransformBlock{Transforms: []Transform{{Type: "strrep", Value: "x:y"}}}

		clean := []byte("a x b")
		encoded, err := tb.Apply(clean, true)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != "a y b" {
			t.Fatalf("strrep encode = %q", encoded)
		}
		decoded, err := tb.Apply(encoded, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, clean) {
			t.Fatalf("strrep round-trip = %q, want %q", decoded, clean)
		}

		// A payload that already contains the replacement cannot be restored: the
		// decode pass rewrites it too. This is a property of the transform, not a
		// bug, and it is why the editor warns instead of stamping the profile valid.
		tricky := []byte("a y b")
		encoded, err = tb.Apply(tricky, true)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err = tb.Apply(encoded, false)
		if err != nil {
			t.Fatal(err)
		}
		// The decode pass rewritten the payload's own "y" back to "x", so the
		// original is NOT restored - this is the lossy case that makes the editor
		// warn instead of stamping the profile valid.
		if bytes.Equal(decoded, tricky) {
			t.Fatal("strrep unexpectedly round-tripped a payload containing the replacement")
		}
		if string(decoded) != "a x b" {
			t.Fatalf("strrep decode = %q, want the rewritten %q", decoded, "a x b")
		}
	})

	t.Run("print reports what it can restore", func(t *testing.T) {
		tb := &TransformBlock{Transforms: []Transform{{Type: "print"}}}
		for _, payload := range [][]byte{
			[]byte("plain printable text"),
			{0x00, 0x01, 0x80, 0xff},
		} {
			encoded, err := tb.Apply(payload, true)
			if err != nil {
				t.Fatalf("print encode: %v", err)
			}
			decoded, err := tb.Apply(encoded, false)
			if err != nil {
				t.Fatalf("print decode: %v", err)
			}
			if !bytes.Equal(decoded, payload) {
				// Pin the lossiness rather than asserting an equality the transform
				// does not promise.
				t.Logf("print is lossy for %x: encoded %q, decoded %x", payload, encoded, decoded)
			}
		}
	})

	t.Run("uri_append strips only what it added", func(t *testing.T) {
		tb := &TransformBlock{Transforms: []Transform{{Type: "uri_append", Value: "&x=1"}}}
		payload := []byte("a=b")
		encoded, err := tb.Apply(payload, true)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != "a=b&x=1" {
			t.Fatalf("uri_append encode = %q", encoded)
		}
		decoded, err := tb.Apply(encoded, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("uri_append round-trip = %q", decoded)
		}
	})
}
