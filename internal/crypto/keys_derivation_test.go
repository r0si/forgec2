package crypto

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func masterKey(t *testing.T) []byte {
	t.Helper()
	k, err := hex.DecodeString(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestDeriveRegistrationKey pins the per-agent key derivation: deterministic for
// the same inputs, distinct across agents (so one implant's key cannot stand in
// for another's), 32 bytes, and nil for a missing master key.
func TestDeriveRegistrationKey(t *testing.T) {
	master := masterKey(t)

	agentA := DeriveRegistrationKey(master, "agent-a")
	agentB := DeriveRegistrationKey(master, "agent-b")
	if len(agentA) != 32 || len(agentB) != 32 {
		t.Fatalf("key lengths = %d, %d", len(agentA), len(agentB))
	}
	if !bytes.Equal(agentA, DeriveRegistrationKey(master, "agent-a")) {
		t.Fatal("derivation is not deterministic")
	}
	if bytes.Equal(agentA, agentB) {
		t.Fatal("two agents share a registration key")
	}
	if bytes.Equal(agentA, master) {
		t.Fatal("the derived key equals the master key")
	}
	if got := DeriveRegistrationKey(nil, "agent-a"); got != nil {
		t.Fatalf("empty master produced %x", got)
	}
	if got := DeriveRegistrationKey([]byte{}, "agent-a"); got != nil {
		t.Fatalf("zero-length master produced %x", got)
	}

	// The hex wrapper agrees with the byte form and rejects bad input.
	if !bytes.Equal(DeriveRegistrationKeyFromHex(strings.Repeat("ab", 32), "agent-a"), agentA) {
		t.Fatal("the hex wrapper derived a different key")
	}
	if got := DeriveRegistrationKeyFromHex("not-hex", "agent-a"); got != nil {
		t.Fatalf("invalid hex produced %x", got)
	}
	if got := DeriveRegistrationKeyFromHex("", "agent-a"); got != nil {
		t.Fatalf("empty hex produced %x", got)
	}
}

// TestComputeRegHMAC pins the handshake MAC: every field is bound, so a captured
// frame cannot be replayed with a changed sequence number or claimed for another
// agent/identity key.
func TestComputeRegHMAC(t *testing.T) {
	key := DeriveRegistrationKey(masterKey(t), "agent-a")
	base := ComputeRegHMAC(key, "agent-a", "pubkey", 1700000000, 7)
	if len(base) != 32 {
		t.Fatalf("mac length = %d", len(base))
	}
	if !bytes.Equal(base, ComputeRegHMAC(key, "agent-a", "pubkey", 1700000000, 7)) {
		t.Fatal("the MAC is not deterministic")
	}

	variants := map[string][]byte{
		"agent":    ComputeRegHMAC(key, "agent-b", "pubkey", 1700000000, 7),
		"identity": ComputeRegHMAC(key, "agent-a", "other-pub", 1700000000, 7),
		"ts":       ComputeRegHMAC(key, "agent-a", "pubkey", 1700000001, 7),
		"seq":      ComputeRegHMAC(key, "agent-a", "pubkey", 1700000000, 8),
		"key":      ComputeRegHMAC(DeriveRegistrationKey(masterKey(t), "agent-b"), "agent-a", "pubkey", 1700000000, 7),
	}
	for name, got := range variants {
		if bytes.Equal(got, base) {
			t.Errorf("changing %s did not change the MAC", name)
		}
	}
}

// TestFileChainMACDetectsTampering pins the chunk-chain property the transfer
// integrity relies on: a changed chunk, a changed previous link, or a changed
// key all break the chain, and the chain does not accept a swapped order.
func TestFileChainMACDetectsTampering(t *testing.T) {
	chainKey := DeriveFileChainKey(DeriveRegistrationKey(masterKey(t), "agent-a"))
	if len(chainKey) != 32 {
		t.Fatalf("chain key length = %d", len(chainKey))
	}
	if got := DeriveFileChainKey(nil); got != nil {
		t.Fatal("an empty registration key produced a chain key")
	}

	seed := make([]byte, 32)
	first := FileChunkMAC(chainKey, seed, []byte("chunk-1"))
	if !bytes.Equal(first, FileChunkMAC(chainKey, seed, []byte("chunk-1"))) {
		t.Fatal("the MAC is not deterministic")
	}

	// Chained: the second link depends on the first.
	second := FileChunkMAC(chainKey, first, []byte("chunk-2"))
	if bytes.Equal(second, FileChunkMAC(chainKey, first, []byte("chunk-2-tampered"))) {
		t.Fatal("a tampered chunk kept the MAC")
	}
	if bytes.Equal(second, FileChunkMAC(chainKey, seed, []byte("chunk-2"))) {
		t.Fatal("dropping the previous link kept the MAC")
	}
	otherKey := DeriveFileChainKey(DeriveRegistrationKey(masterKey(t), "agent-b"))
	if bytes.Equal(second, FileChunkMAC(otherKey, first, []byte("chunk-2"))) {
		t.Fatal("another agent's chain key produced the same MAC")
	}
	// Swapped order breaks the chain.
	swapped := FileChunkMAC(chainKey, FileChunkMAC(chainKey, seed, []byte("chunk-2")), []byte("chunk-1"))
	if bytes.Equal(swapped, second) {
		t.Fatal("swapping chunk order produced the same chain")
	}
	if got := FileChunkMAC(nil, seed, []byte("x")); got != nil {
		t.Fatal("an empty chain key produced a MAC")
	}
}

// TestKillSwitchHMACPinsTokenBinding pins the kill-switch tag: it is bound to the
// per-implant key and to the token, so one implant's tag never verifies for
// another, and a stale token cannot be re-tagged.
func TestKillSwitchHMACPinsTokenBinding(t *testing.T) {
	regA := DeriveRegistrationKey(masterKey(t), "agent-a")
	regB := DeriveRegistrationKey(masterKey(t), "agent-b")
	ksA := DeriveKillSwitchKey(regA)
	ksB := DeriveKillSwitchKey(regB)
	if len(ksA) != 32 || bytes.Equal(ksA, ksB) {
		t.Fatalf("kill-switch keys are not per-implant (len %d)", len(ksA))
	}
	if bytes.Equal(ksA, regA) {
		t.Fatal("the kill-switch key equals the registration key")
	}

	token := []byte("arm-token-1")
	tag := KillSwitchHMAC(ksA, token)
	if len(tag) != 32 {
		t.Fatalf("tag length = %d", len(tag))
	}
	if !bytes.Equal(tag, KillSwitchHMAC(ksA, token)) {
		t.Fatal("the tag is not deterministic")
	}
	if bytes.Equal(tag, KillSwitchHMAC(ksB, token)) {
		t.Fatal("two implants produced the same kill-switch tag")
	}
	if bytes.Equal(tag, KillSwitchHMAC(ksA, []byte("arm-token-2"))) {
		t.Fatal("a different token produced the same tag")
	}
	if got := KillSwitchHMAC(nil, token); got != nil {
		t.Fatal("an empty key produced a tag")
	}
	if got := DeriveKillSwitchKey(nil); got != nil {
		t.Fatal("an empty registration key produced a kill-switch key")
	}
}

// TestDeriveSessionKey pins the per-session key: deterministic for the same
// shared secret and agent, distinct across agents (so one agent cannot decrypt
// another's session), 32 bytes, and nil for a missing shared secret.
func TestDeriveSessionKey(t *testing.T) {
	secret := []byte("x25519-shared-secret-bytes")
	a := DeriveSessionKey(secret, "agent-a")
	b := DeriveSessionKey(secret, "agent-b")
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("key lengths = %d, %d", len(a), len(b))
	}
	if !bytes.Equal(a, DeriveSessionKey(secret, "agent-a")) {
		t.Fatal("derivation is not deterministic")
	}
	if bytes.Equal(a, b) {
		t.Fatal("two agents share a session key")
	}
	if bytes.Equal(a, secret) {
		t.Fatal("the session key equals the shared secret")
	}
	if got := DeriveSessionKey(nil, "agent-a"); got != nil {
		t.Fatalf("empty secret produced %x", got)
	}
	// A different shared secret must not produce the same key.
	other := DeriveSessionKey([]byte("a-different-shared-secret"), "agent-a")
	if bytes.Equal(a, other) {
		t.Fatal("two secrets produced the same session key")
	}
}
