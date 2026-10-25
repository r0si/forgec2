package server

import (
	"encoding/json"
	"testing"
)

// unmarshalData decodes a response body into out.
//
// Success responses are {"success": true, "data": <payload>} (respond wraps
// exactly once), so the payload — not the wrapper — is what most assertions
// are about. Bodies that are not the envelope (a 202/4xx body, or an endpoint
// that documents a bare shape) are decoded as-is, so a test can still assert
// on those.
func unmarshalData(t *testing.T, body []byte, out any) {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Success &&
		len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("decode envelope data: %v (%s)", err, body)
		}
		return
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode body: %v (%s)", err, body)
	}
}
