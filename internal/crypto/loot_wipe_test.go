package crypto

import (
	"strings"
	"testing"
)

// TestWipeZeroesEveryByte covers the best-effort key erasure. A no-op wipe would
// leave live key material on the heap, so the test asserts the buffer really is
// zeroed, that it works for every length (including empty), and that it touches
// only the buffer it was given.
func TestWipeZeroesEveryByte(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	Wipe(key)
	for i, b := range key {
		if b != 0 {
			t.Fatalf("byte %d was not wiped: %d", i, b)
		}
	}

	// Lengths around the block boundary, and the degenerate cases.
	for _, n := range []int{0, 1, 7, 16, 31, 64, 129} {
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = 0xFF
		}
		Wipe(buf)
		for i, b := range buf {
			if b != 0 {
				t.Fatalf("len %d: byte %d survived", n, i)
			}
		}
	}

	// Wiping a slice must not touch its neighbours.
	backing := make([]byte, 8)
	for i := range backing {
		backing[i] = 0xAA
	}
	Wipe(backing[2:4])
	if backing[0] != 0xAA || backing[1] != 0xAA || backing[2] != 0 || backing[3] != 0 || backing[4] != 0xAA {
		t.Fatalf("wipe escaped its slice: % x", backing)
	}

	// A nil slice is a no-op, not a panic.
	Wipe(nil)
}

// TestEncryptLootPlaintextOnlyTouchesLegacyValues pins the migration contract of
// the vault plaintext sweep: only a genuine plaintext value is sealed (and
// reports changed=true); empty and already-sealed values pass through untouched;
// undecryptable ciphertext is an error rather than a silent re-encryption.
func TestEncryptLootPlaintextOnlyTouchesLegacyValues(t *testing.T) {
	InitLootEncryption(testHexKey)

	// A legacy plaintext value is sealed and reported as changed.
	sealed, changed, err := EncryptLootPlaintext("legacy-secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !changed {
		t.Fatal("a plaintext value was not reported as changed")
	}
	if !strings.HasPrefix(sealed, "FC2ENC:") {
		t.Fatalf("the value was not sealed: %q", sealed)
	}
	if plain, derr := DecryptLoot(sealed); derr != nil || plain != "legacy-secret" {
		t.Fatalf("round-trip = %q, %v", plain, derr)
	}

	// Already sealed: untouched and not changed (idempotent sweep).
	again, changed, err := EncryptLootPlaintext(sealed)
	if err != nil {
		t.Fatalf("re-seal: %v", err)
	}
	if changed {
		t.Fatal("an already-sealed value was reported as changed")
	}
	if again != sealed {
		t.Fatal("an already-sealed value was rewritten")
	}

	// Empty stays empty.
	empty, changed, err := EncryptLootPlaintext("")
	if err != nil || changed || empty != "" {
		t.Fatalf("empty = %q, changed=%v, err=%v", empty, changed, err)
	}

	// Undecryptable ciphertext is an error, never a re-encryption: silently
	// sealing it would destroy the only copy of the original plaintext.
	if _, _, err := EncryptLootPlaintext("FC2ENC:not-real-ciphertext"); err == nil {
		t.Fatal("undecryptable ciphertext was accepted")
	}
}
