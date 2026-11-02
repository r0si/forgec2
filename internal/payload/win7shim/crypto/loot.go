package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
)

var (
	lootKeyMu sync.RWMutex
	lootKey   []byte
	// lootKeyID is the fingerprint (first 8 hex chars of SHA-256) of the
	// active loot key. prevLootKey/prevLootKeyID retain the replaced key so
	// a rotation does not orphan rows encrypted under it: reads fall back
	// to it, and the re-encrypt job normalizes rows to the active key.
	lootKeyID     string
	prevLootKey   []byte
	prevLootKeyID string

	extc2KeyMu sync.RWMutex
	extc2Key   []byte
)

// lootFingerprint identifies a key without exposing it.
func lootFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])[:8]
}

// LootKeyID returns the fingerprint of the active loot key ("" if unset).
func LootKeyID() string {
	lootKeyMu.RLock()
	defer lootKeyMu.RUnlock()
	return lootKeyID
}

// PrevLootKeyID returns the fingerprint of the retained previous loot key.
func PrevLootKeyID() string {
	lootKeyMu.RLock()
	defer lootKeyMu.RUnlock()
	return prevLootKeyID
}

// InitLootEncryption initializes (or re-initializes) the loot encryption key.
// It is intentionally re-entrant so config reloads / key rotation can install
// a new key without requiring a process restart.
// The key MUST be a 32-byte hex string: the legacy SHA-256(jwt_secret)
// derivation was removed so loot ciphertext is cryptographically independent
// of the JWT secret.
//
// A valid key is always adopted. An empty/invalid value is only allowed to
// Wipe zeroes key material in place. Best-effort by construction: Go's GC
// may retain copies elsewhere on the heap (no mlock, no guaranteed erasure),
// so this narrows heap-dump exposure rather than eliminating it. Call it
// wherever a key buffer is genuinely dropped (never on retained/rotated
// keys that fallback reads still need).
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
	// KeepAlive defeats dead-store elimination of the loop above: the
	// slice must still be live here, so the zeroes must have happened.
	runtime.KeepAlive(b)
}

// CLEAR the key when no key is currently active (e.g. first init with no
// configured key) so encryption fails loudly. If a key is ALREADY active, an
// empty/invalid reload value is ignored (with a warning) rather than silently
// wiping it — otherwise a config reload that dropped crypto.loot_key would
// leave every already-encrypted task result (FC2ENC:) permanently undecryptable
// while new results would be stored as plaintext, producing inconsistent output.
func InitLootEncryption(lootKeyHex string) {
	lootKeyMu.Lock()
	defer lootKeyMu.Unlock()
	if b, err := hex.DecodeString(lootKeyHex); err == nil && len(b) == 32 {
		// Rotation: retain the replaced key so rows encrypted under it stay
		// readable (and re-encryptable) until the background job normalizes
		// them. Same value re-init is a no-op for the previous slot.
		if lootKey != nil && lootKeyID != lootFingerprint(b) {
			prevLootKey = lootKey
			prevLootKeyID = lootKeyID
			slog.Info("Loot key rotated, previous key retained for fallback reads",
				"active", lootFingerprint(b), "prev", prevLootKeyID)
		}
		lootKey = b
		lootKeyID = lootFingerprint(b)
		return
	}
	if lootKey != nil {
		slog.Warn("InitLootEncryption called with empty/invalid key; keeping existing key so stored loot stays decryptable",
			"provided", lootKeyHex)
		return
	}
	lootKey = nil
}

// InitExtC2Encryption initializes (or re-initializes) a separate encryption key
// for ExtC2 channels, derived independently to limit key compromise blast radius.
// The key MUST be a 32-byte hex string (no legacy JWT derivation).
func InitExtC2Encryption(extc2KeyHex string) {
	extc2KeyMu.Lock()
	defer extc2KeyMu.Unlock()
	if b, err := hex.DecodeString(extc2KeyHex); err == nil && len(b) == 32 {
		extc2Key = b
		return
	}
	extc2Key = nil
}

// EncryptLoot encrypts a plaintext string using AES-256-GCM.
func EncryptLoot(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	lootKeyMu.RLock()
	key := lootKey
	lootKeyMu.RUnlock()
	if key == nil {
		return "", errors.New("loot encryption not initialized")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), nil)
	return "FC2ENC:" + base64.StdEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

// LootState classifies a stored loot value for reads and rotation tooling.
type LootState int

const (
	// LootEmpty: empty string, nothing stored.
	LootEmpty LootState = iota
	// LootPlaintextLegacy: stored without FC2ENC: marker (pre-encryption rows).
	LootPlaintextLegacy
	// LootDecryptable: FC2ENC: ciphertext readable with the active key.
	LootDecryptable
	// LootDecryptablePrevKey: readable only with the retained previous key;
	// the re-encrypt job should normalize it to the active key.
	LootDecryptablePrevKey
	// LootUndecryptable: FC2ENC: ciphertext readable with neither key
	// (wrong key after rotation without fallback, or corruption).
	LootUndecryptable
)

// DecryptLootState decrypts s and reports how: legacy plaintext passes
// through, FC2ENC: is tried against the active key then the retained
// previous key. The returned error is non-nil only for LootUndecryptable
// (and for uninitialized keys).
func DecryptLootState(s string) (string, LootState) {
	if s == "" {
		return "", LootEmpty
	}
	lootKeyMu.RLock()
	key := lootKey
	prev := prevLootKey
	lootKeyMu.RUnlock()
	if key == nil {
		return "", LootUndecryptable
	}
	// An ExtC2 ciphertext is NOT loot plaintext. Treating it as legacy plaintext
	// was wrong in a damaging way: the vault plaintext sweep would seal it inside
	// FC2ENC, and every later read would return the FC2EXT *string* instead of the
	// secret. Refuse it instead, so the sweep reports an error rather than
	// silently wrapping ciphertext.
	if strings.HasPrefix(s, "FC2EXT:") {
		return "", LootUndecryptable
	}
	const marker = "FC2ENC:"
	if len(s) < len(marker) || s[:len(marker)] != marker {
		return s, LootPlaintextLegacy
	}
	if plain, err := decryptLootWith(s[len(marker):], key); err == nil {
		return plain, LootDecryptable
	}
	if prev != nil {
		if plain, err := decryptLootWith(s[len(marker):], prev); err == nil {
			return plain, LootDecryptablePrevKey
		}
	}
	return "", LootUndecryptable
}

func decryptLootWith(body string, key []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("decryption failed: invalid ciphertext encoding: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := aead.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("decryption failed: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}
	return string(plaintext), nil
}

// ReencryptLoot normalizes s to the active key: values already decryptable
// with it pass through untouched; previous-key values are re-encrypted.
// Returns (normalized, changed, error). Undecryptable values error out.
func ReencryptLoot(s string) (string, bool, error) {
	plain, state := DecryptLootState(s)
	switch state {
	case LootEmpty, LootPlaintextLegacy, LootDecryptable:
		return s, false, nil
	case LootDecryptablePrevKey:
		enc, err := EncryptLoot(plain)
		if err != nil {
			return "", false, err
		}
		return enc, true, nil
	default:
		return "", false, errors.New("loot value undecryptable with active or previous key")
	}
}

// EncryptLootPlaintext seals a legacy plaintext value with the active key.
// Only LootPlaintextLegacy inputs are touched (changed=true); empty strings
// and already-sealed FC2ENC: values pass through with changed=false, and
// undecryptable ciphertext errors out. Used by the vault plaintext sweep to
// migrate pre-encryption rows that the write-path hook never re-touches.
func EncryptLootPlaintext(s string) (string, bool, error) {
	_, state := DecryptLootState(s)
	switch state {
	case LootEmpty, LootDecryptable, LootDecryptablePrevKey:
		return s, false, nil
	case LootPlaintextLegacy:
		enc, err := EncryptLoot(s)
		if err != nil {
			return "", false, err
		}
		return enc, true, nil
	default:
		return "", false, errors.New("loot value undecryptable with active or previous key")
	}
}

// DecryptLoot decrypts a ciphertext string using AES-256-GCM.
// Falls back to returning the raw string if it's not encrypted (backward compat
// with old plaintext credentials in the database). FC2ENC: values are tried
// against the active key first, then the retained previous key, so reads
// survive a rotation until the re-encrypt job normalizes the row.
func DecryptLoot(s string) (string, error) {
	plain, state := DecryptLootState(s)
	switch state {
	case LootEmpty, LootPlaintextLegacy, LootDecryptable, LootDecryptablePrevKey:
		return plain, nil
	default:
		if s == "" {
			return "", nil
		}
		return "", errors.New("loot decryption failed: undecryptable with active or previous key")
	}
}

func EncryptExtC2(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	extc2KeyMu.RLock()
	key := extc2Key
	extc2KeyMu.RUnlock()
	if key == nil {
		return "", errors.New("extc2 encryption not initialized")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), nil)
	return "FC2EXT:" + base64.StdEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func DecryptExtC2(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	extc2KeyMu.RLock()
	key := extc2Key
	extc2KeyMu.RUnlock()
	if key == nil {
		return "", errors.New("extc2 encryption not initialized")
	}
	const marker = "FC2EXT:"
	if len(s) < len(marker) || s[:len(marker)] != marker {
		return s, nil
	}
	data, err := base64.StdEncoding.DecodeString(s[len(marker):])
	if err != nil {
		return "", fmt.Errorf("extc2 decryption failed: invalid ciphertext encoding: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := aead.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("extc2 decryption failed: ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("extc2 decryption failed: %w", err)
	}
	return string(plaintext), nil
}
