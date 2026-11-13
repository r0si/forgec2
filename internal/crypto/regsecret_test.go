package crypto

import (
	"bytes"
	"testing"
)

// TestRegSecretStoreSealUnseal verifies a sealed registration secret round-
// trips and that the ciphertext is not plaintext.
func TestRegSecretStoreSealUnseal(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	store := NewRegSecretStore(master)
	secret, err := store.GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret must be 32 bytes, got %d", len(secret))
	}
	sealed, err := store.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed == "" || bytes.Contains([]byte(sealed), secret) {
		t.Fatalf("sealed value must not leak the plaintext secret")
	}
	back, err := store.Unseal(sealed)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if !bytes.Equal(back, secret) {
		t.Fatalf("round-trip mismatch")
	}
}

// TestRegSecretStoreSealUnique verifies two seals of the same secret produce
// different ciphertexts (random nonce), so DB snapshots do not reveal secret
// equality.
func TestRegSecretStoreSealUnique(t *testing.T) {
	store := NewRegSecretStore([]byte("master-key"))
	secret := bytes.Repeat([]byte{0x11}, 32)
	a, err := store.Seal(secret)
	if err != nil {
		t.Fatalf("Seal a: %v", err)
	}
	b, err := store.Seal(secret)
	if err != nil {
		t.Fatalf("Seal b: %v", err)
	}
	if a == b {
		t.Fatalf("two seals of the same secret must differ (nonce randomization)")
	}
}

// TestRegSecretStoreDifferentMaster verifies the store derived from a
// different master key cannot unseal a value sealed by another key.
func TestRegSecretStoreDifferentMaster(t *testing.T) {
	storeA := NewRegSecretStore([]byte("master-a-key-1234567890abcdef"))
	storeB := NewRegSecretStore([]byte("master-b-key-1234567890abcdef"))
	secret := bytes.Repeat([]byte{0x33}, 32)
	sealed, err := storeA.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := storeB.Unseal(sealed); err == nil {
		t.Fatalf("Unseal with a different master key must fail")
	}
}

// TestRegSecretStoreUninitialized verifies an empty-master store refuses to
// seal/unseal (server with no beacon key configured).
func TestRegSecretStoreUninitialized(t *testing.T) {
	store := NewRegSecretStore(nil)
	if _, err := store.Seal([]byte("x")); err == nil {
		t.Fatalf("Seal without a store key must fail")
	}
	if _, err := store.Unseal("FC2REG:AAAA"); err == nil {
		t.Fatalf("Unseal without a store key must fail")
	}
}

// TestRegSecretStoreTamper verifies a modified ciphertext fails to unseal.
func TestRegSecretStoreTamper(t *testing.T) {
	store := NewRegSecretStore([]byte("master-key"))
	secret := bytes.Repeat([]byte{0x55}, 32)
	sealed, err := store.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Flip a character in the base64 payload (after the FC2REG: marker). Use a
	// character that differs from the one already there: the old "replace the
	// last byte with X" form was a no-op whenever the last character happened to
	// be X, which made this test flaky in CI.
	const marker = "FC2REG:"
	at := len(marker) + (len(sealed)-len(marker))/2
	replacement := byte('A')
	if sealed[at] == replacement {
		replacement = 'B'
	}
	tampered := sealed[:at] + string(replacement) + sealed[at+1:]
	if tampered == sealed {
		t.Fatal("tamper helper did not change the ciphertext")
	}
	if _, err := store.Unseal(tampered); err == nil {
		t.Fatalf("tampered ciphertext must fail to unseal")
	}
	if _, err := store.Unseal("FC2REG:not-base64!"); err == nil {
		t.Fatalf("invalid base64 must fail to unseal")
	}
}
