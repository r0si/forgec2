package crypto

import (
	"strings"
	"testing"
)

// TestExtC2CiphertextIsNotLootPlaintext pins the classification fix: an FC2EXT:
// value used to fall through to "legacy plaintext", so DecryptLoot returned the
// ciphertext as if it were the secret and the vault plaintext sweep would wrap it
// inside FC2ENC - after which every read returned the FC2EXT *string*. Refusing it
// makes the sweep report an error instead of silently double-encoding.
func TestExtC2CiphertextIsNotLootPlaintext(t *testing.T) {
	InitLootEncryption(testHexKey)
	InitExtC2Encryption(strings.Repeat("cd", 32))

	sealed, err := EncryptExtC2("channel-token")
	if err != nil {
		t.Fatalf("EncryptExtC2: %v", err)
	}
	if !strings.HasPrefix(sealed, "FC2EXT:") {
		t.Fatalf("ExtC2 value has the wrong marker: %q", sealed)
	}

	// The loot decoder must refuse it, not hand back the ciphertext.
	if plain, derr := DecryptLoot(sealed); derr == nil {
		t.Fatalf("DecryptLoot returned an ExtC2 ciphertext as plaintext: %q", plain)
	}
	if _, state := DecryptLootState(sealed); state != LootUndecryptable {
		t.Fatalf("DecryptLootState classified an ExtC2 value as %v", state)
	}
	// The sweep must error rather than re-encrypt it.
	if out, changed, serr := EncryptLootPlaintext(sealed); serr == nil || changed || out != "" {
		t.Fatalf("the plaintext sweep re-sealed an ExtC2 value: out=%q changed=%v err=%v", out, changed, serr)
	}

	// And the two directions stay separated: the loot key cannot open an ExtC2
	// value, and the ExtC2 key cannot open a loot value.
	if _, derr := DecryptExtC2(sealed); derr != nil {
		t.Fatalf("the ExtC2 round-trip broke: %v", derr)
	}
	loot, err := EncryptLoot("vault-secret")
	if err != nil {
		t.Fatalf("EncryptLoot: %v", err)
	}
	// The ExtC2 decoder passes unknown values through (legacy plaintext tokens
	// predate FC2EXT:), so the isolation property is that it CANNOT open the loot
	// ciphertext: the value comes back still sealed, never as the secret.
	if got, derr := DecryptExtC2(loot); derr == nil && got == "vault-secret" {
		t.Fatal("the ExtC2 key decrypted loot ciphertext: the keys are not independent")
	} else if derr == nil && !strings.HasPrefix(got, "FC2ENC:") {
		t.Fatalf("the ExtC2 decoder returned something unexpected: %q", got)
	}

	// A genuine legacy plaintext still passes through untouched.
	if plain, derr := DecryptLoot("plain-value"); derr != nil || plain != "plain-value" {
		t.Fatalf("legacy plaintext = %q, %v", plain, derr)
	}
}
