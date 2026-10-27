package server

import (
	"encoding/hex"
	"testing"

	"github.com/forgec2/forgec2/internal/crypto"
)

// TestPushChainCarriesAcrossChunks pins the fix for a chain that was only a chain in
// name: the push path called chainForPush per chunk, keyed by that chunk's own task id,
// so every chunk started from the zero seed. Each chunk stayed protected against content
// tampering, but nothing tied chunk N to chunk N-1 - a relay could reorder or replace
// chunk 2 onwards and every one still verified on its own.
func TestPushChainCarriesAcrossChunks(t *testing.T) {
	s := testServerForChain(t)
	key := s.fileChainKey("agent-push")

	chunk1 := []byte("chunk one")
	chunk2 := []byte("chunk two")
	chunk3 := []byte("chunk three")

	prev1, mac1hex, err := chainForPushSequence(key, nil, chunk1)
	if err != nil {
		t.Fatal(err)
	}
	if prev1 != hex.EncodeToString(make([]byte, 32)) {
		t.Fatalf("first chunk prev = %q, want the zero seed", prev1)
	}
	mac1, err := hex.DecodeString(mac1hex)
	if err != nil {
		t.Fatal(err)
	}

	prev2, mac2hex, err := chainForPushSequence(key, mac1, chunk2)
	if err != nil {
		t.Fatal(err)
	}
	if prev2 != mac1hex {
		t.Fatalf("second chunk prev = %q, want the first chunk's mac %q", prev2, mac1hex)
	}
	mac2, err := hex.DecodeString(mac2hex)
	if err != nil {
		t.Fatal(err)
	}

	prev3, mac3hex, err := chainForPushSequence(key, mac2, chunk3)
	if err != nil {
		t.Fatal(err)
	}
	if prev3 != mac2hex {
		t.Fatalf("third chunk prev = %q, want the second chunk's mac %q", prev3, mac2hex)
	}

	// The links are exactly what the agent recomputes, in order.
	if got := hex.EncodeToString(crypto.FileChunkMAC(key, mac1, chunk2)); got != mac2hex {
		t.Fatalf("agent-side recomputation of chunk2 = %q, want %q", got, mac2hex)
	}
	if got := hex.EncodeToString(crypto.FileChunkMAC(key, mac2, chunk3)); got != mac3hex {
		t.Fatalf("agent-side recomputation of chunk3 = %q, want %q", got, mac3hex)
	}
}

// TestPushChainCatchesReordering shows what the chaining buys: with the chunks swapped,
// the links recorded for the original order no longer verify.
func TestPushChainCatchesReordering(t *testing.T) {
	s := testServerForChain(t)
	key := s.fileChainKey("agent-push-order")

	chunk1 := []byte("AAAA")
	chunk2 := []byte("BBBB")

	_, mac1hex, err := chainForPushSequence(key, nil, chunk1)
	if err != nil {
		t.Fatal(err)
	}
	mac1, _ := hex.DecodeString(mac1hex)
	_, mac2hex, err := chainForPushSequence(key, mac1, chunk2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = hex.DecodeString(mac2hex)

	// A relay swapping the two chunks: the second slot now carries chunk1's data, and
	// the agent recomputes over the previous link it was given.
	swapped := hex.EncodeToString(crypto.FileChunkMAC(key, mac1, chunk1))
	if swapped == mac2hex {
		t.Fatal("a swapped chunk produced the same link; the chain does not bind order")
	}
}

// TestPushChainWithoutKeyFailsClosed keeps a missing chain key from silently producing
// unsigned chunks.
func TestPushChainWithoutKeyFailsClosed(t *testing.T) {
	if _, _, err := chainForPushSequence(nil, nil, []byte("data")); err == nil {
		t.Fatal("a nil chain key produced a link")
	}
}
