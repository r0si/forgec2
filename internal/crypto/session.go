package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	DefaultSessionMaxAge = 10 * time.Minute
	// DefaultMaxSessions bounds the session map so a flood of handshakes from
	// unique (or spoofed) agent IDs cannot grow memory without limit. When at
	// capacity the least-recently-used session is evicted to make room.
	DefaultMaxSessions = 100000
	// sessionEvictionSample is how many entries are sampled when evicting at
	// capacity, keeping the cost of a full-cache handshake O(sample).
	sessionEvictionSample = 32

	// Session AEAD suites. Wire format is identical for both (nonce||ct,
	// 12-byte nonce); the suite is agreed at handshake so decrypt knows which
	// AEAD to use. Empty / AES-GCM is the default for upgrade compatibility.
	SessionCipherAESGCM   = "aes-gcm"
	SessionCipherChaCha20 = "chacha20"
)

var (
	ErrNoSession          = errors.New("no session for agent")
	ErrSessionActive      = errors.New("an active session already exists for agent")
	ErrCiphertextTooShort = errors.New("ciphertext too short")
	ErrDecryptFailed      = errors.New("decryption failed")
)

// SessionManager manages ECDH key exchange and session keys with PFS
type SessionManager struct {
	privateKey  *ecdh.PrivateKey
	sessions    map[string]*Session
	maxAge      time.Duration
	maxSessions int
	// cipher is the AEAD suite stamped onto every new Session at
	// EstablishSession (SessionCipherAESGCM or SessionCipherChaCha20).
	cipher string
	mu     sync.RWMutex
}

// Session represents a single agent session with PFS
type Session struct {
	AgentID      string
	SessionKey   []byte
	CreatedAt    time.Time
	MessageCount int
	LastUsed     time.Time
	// Cipher is the AEAD suite for this session ("" / aes-gcm / chacha20).
	// Stamped from SessionManager.cipher at EstablishSession so a live session
	// keeps its suite even if the operator later flips the config.
	Cipher string
	// RekeyCount / LastRekeyAt track session rotations: every EstablishSession
	// that overwrites an existing session for the agent is a rekey (driven by
	// the server's message-count threshold or by restart recovery).
	RekeyCount  int
	LastRekeyAt time.Time
}

// NewSessionManager creates a new session manager with default thresholds
func NewSessionManager() (*SessionManager, error) {
	return NewSessionManagerWithConfig(DefaultSessionMaxAge)
}

// NewSessionManagerWithConfig creates a new session manager with a custom session max age
func NewSessionManagerWithConfig(maxAge time.Duration) (*SessionManager, error) {
	return NewSessionManagerWithCipher(maxAge, SessionCipherAESGCM)
}

// NewSessionManagerWithCipher creates a session manager that stamps suite onto
// every new Session. Empty suite is normalized to AES-GCM.
func NewSessionManagerWithCipher(maxAge time.Duration, suite string) (*SessionManager, error) {
	curve := ecdh.X25519()
	privateKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if maxAge <= 0 {
		maxAge = DefaultSessionMaxAge
	}
	return &SessionManager{
		privateKey:  privateKey,
		sessions:    make(map[string]*Session),
		maxAge:      maxAge,
		maxSessions: DefaultMaxSessions,
		cipher:      NormalizeSessionCipher(suite),
	}, nil
}

// NormalizeSessionCipher maps a config/wire cipher name to a known suite.
// Unknown values fall back to AES-GCM so a typo cannot brick every beacon.
func NormalizeSessionCipher(suite string) string {
	switch suite {
	case SessionCipherChaCha20, "chacha20-poly1305":
		return SessionCipherChaCha20
	default:
		return SessionCipherAESGCM
	}
}

// Cipher returns the AEAD suite stamped onto new sessions.
func (sm *SessionManager) Cipher() string {
	if sm == nil {
		return SessionCipherAESGCM
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.cipher == "" {
		return SessionCipherAESGCM
	}
	return sm.cipher
}

// GetPublicKey returns the server's public key for distribution to agents
func (sm *SessionManager) GetPublicKey() []byte {
	if sm == nil {
		return nil
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.privateKey == nil {
		return nil
	}
	return sm.privateKey.PublicKey().Bytes()
}

// MaxAge returns the configured max session age
func (sm *SessionManager) MaxAge() time.Duration { return sm.maxAge }

// EstablishSession performs ECDH key exchange with an agent and derives the
// v2 session key (HKDF bound to the agent ID, replacing the bare SHA-256).
//
// v2 semantics: handshake frames are authenticated by the caller (HMAC with
// the per-agent registration key, enforced in the beacon handler), so an
// existing session may be overwritten — that is exactly how rekey and
// server-restart recovery work. Callers MUST NOT invoke this without prior
// authentication of the handshake frame.
func (sm *SessionManager) EstablishSession(agentID string, agentPublicKey []byte) error {
	curve := ecdh.X25519()

	agentPub, err := curve.NewPublicKey(agentPublicKey)
	if err != nil {
		return err
	}

	// Lock around privateKey access to prevent race with key rotation
	sm.mu.Lock()

	// Bounded session map: a flood of handshakes from spoofed agent IDs must not
	// grow the map without limit. When at capacity, evict the least-recently-used
	// session (sampled, so eviction stays O(sample) under load).
	if sm.maxSessions > 0 && len(sm.sessions) >= sm.maxSessions {
		if victim := sm.oldestSampledSessionLocked(); victim != "" {
			delete(sm.sessions, victim)
		}
	}

	sharedSecret, err := sm.privateKey.ECDH(agentPub)
	if err != nil {
		sm.mu.Unlock()
		return err
	}

	sessionKey := DeriveSessionKey(sharedSecret, agentID)
	if sessionKey == nil {
		sm.mu.Unlock()
		return errors.New("session key derivation failed")
	}

	// Bump rekey bookkeeping when overwriting an existing session
	// (rekey / restart recovery per the v2 semantics above).
	suite := sm.cipher
	if suite == "" {
		suite = SessionCipherAESGCM
	}
	if existing, ok := sm.sessions[agentID]; ok {
		existing.RekeyCount++
		existing.LastRekeyAt = time.Now()
		sm.sessions[agentID] = &Session{
			AgentID:      agentID,
			SessionKey:   sessionKey,
			CreatedAt:    time.Now(),
			MessageCount: 0,
			LastUsed:     time.Now(),
			Cipher:       suite,
			RekeyCount:   existing.RekeyCount,
			LastRekeyAt:  existing.LastRekeyAt,
		}
		sm.mu.Unlock()
		return nil
	}

	sm.sessions[agentID] = &Session{
		AgentID:      agentID,
		SessionKey:   sessionKey,
		CreatedAt:    time.Now(),
		MessageCount: 0,
		LastUsed:     time.Now(),
		Cipher:       suite,
	}
	sm.mu.Unlock()

	return nil
}

// oldestSampledSessionLocked returns the agent ID of the session with the oldest
// LastUsed timestamp among a random sample of up to sessionEvictionSample
// entries. Caller must hold sm.mu. Empty string when the map is empty.
func (sm *SessionManager) oldestSampledSessionLocked() string {
	var victim string
	var oldest time.Time
	sample := sessionEvictionSample
	if len(sm.sessions) < sample {
		sample = len(sm.sessions)
	}
	i := 0
	for id, sess := range sm.sessions {
		if victim == "" || sess.LastUsed.Before(oldest) {
			victim, oldest = id, sess.LastUsed
		}
		i++
		if i >= sample {
			break
		}
	}
	return victim
}

// GetSession retrieves a copy of the agent's session to prevent data races
// from callers reading Session fields after RUnlock while writer mutates them.
func (sm *SessionManager) GetSession(agentID string) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if s, ok := sm.sessions[agentID]; ok && s != nil {
		c := *s
		// Deep copy SessionKey slice to avoid sharing underlying array
		if s.SessionKey != nil {
			c.SessionKey = append([]byte(nil), s.SessionKey...)
		}
		return &c
	}
	return nil
}

// HasSession reports whether an active session exists for the agent.
func (sm *SessionManager) HasSession(agentID string) bool {
	return sm.GetSession(agentID) != nil
}

// RemoveSession drops the agent's session entirely. Called when an implant is
// deleted so its session key cannot outlive the row it belonged to (the
// periodic expiry sweep would otherwise keep it up to maxAge).
func (sm *SessionManager) RemoveSession(agentID string) {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	delete(sm.sessions, agentID)
	sm.mu.Unlock()
}

// SessionStats is a snapshot of rekey activity across live sessions.
type SessionStats struct {
	ActiveSessions int `json:"active_sessions"`
	TotalRekeys    int `json:"total_rekeys"`
	RekeyCounts    []struct {
		AgentID      string    `json:"agent_id"`
		RekeyCount   int       `json:"rekey_count"`
		LastRekeyAt  time.Time `json:"last_rekey_at,omitempty"`
		MessageCount int       `json:"message_count"`
		LastUsed     time.Time `json:"last_used"`
	} `json:"rekeys_by_agent,omitempty"`
}

// Stats returns aggregated rekey metrics across all live sessions. Used to
// feed monitoring endpoints and the Prometheus collector without leaking key
// material — only counts and timestamps.
func (sm *SessionManager) Stats() SessionStats {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var st SessionStats
	st.ActiveSessions = len(sm.sessions)
	for id, sess := range sm.sessions {
		st.TotalRekeys += sess.RekeyCount
		if sess.RekeyCount > 0 {
			st.RekeyCounts = append(st.RekeyCounts, struct {
				AgentID      string    `json:"agent_id"`
				RekeyCount   int       `json:"rekey_count"`
				LastRekeyAt  time.Time `json:"last_rekey_at,omitempty"`
				MessageCount int       `json:"message_count"`
				LastUsed     time.Time `json:"last_used"`
			}{AgentID: id, RekeyCount: sess.RekeyCount, LastRekeyAt: sess.LastRekeyAt, MessageCount: sess.MessageCount, LastUsed: sess.LastUsed})
		}
	}
	return st
}

// IncrementMessageCount tracks message count for session freshness
func (sm *SessionManager) IncrementMessageCount(agentID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if session, exists := sm.sessions[agentID]; exists {
		session.MessageCount++
		session.LastUsed = time.Now()
	}
}

// NeedsRekey reports whether the agent session has exceeded the message count
// threshold and should rotate its keying material via a new handshake.
func (sm *SessionManager) NeedsRekey(agentID string, threshold int) bool {
	if threshold <= 0 {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	session := sm.sessions[agentID]
	return session != nil && session.MessageCount >= threshold
}

// Encrypt encrypts data using AES-256-GCM with the session key, returns raw bytes (nonce+ciphertext)
func (sm *SessionManager) Encrypt(agentID string, plaintext []byte) ([]byte, error) {
	return sm.EncryptWithAAD(agentID, plaintext, nil)
}

// EncryptWithAAD encrypts data with the session's AEAD suite and authenticates
// the provided additional data (protocol v2 binds agentID and sequence number
// so ciphertext cannot be transplanted across frames).
func (sm *SessionManager) EncryptWithAAD(agentID string, plaintext, aad []byte) ([]byte, error) {
	session := sm.GetSession(agentID)
	if session == nil {
		return nil, ErrNoSession
	}

	nonceSize := sessionNonceSize(session.Cipher)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	ciphertext, err := sealSession(session.Cipher, session.SessionKey, nonce, plaintext, aad)
	if err != nil {
		return nil, err
	}

	sm.IncrementMessageCount(agentID)

	return append(nonce, ciphertext...), nil
}

// EncryptB64 encrypts and returns base64-encoded ciphertext
func (sm *SessionManager) EncryptB64(agentID string, plaintext []byte) (string, error) {
	raw, err := sm.Encrypt(agentID, plaintext)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// EncryptB64WithAAD encrypts and returns base64-encoded ciphertext, binding
// the provided additional data (protocol v2 frame binding).
func (sm *SessionManager) EncryptB64WithAAD(agentID string, plaintext, aad []byte) (string, error) {
	raw, err := sm.EncryptWithAAD(agentID, plaintext, aad)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// sessionNonceSize returns the AEAD nonce length for a suite (12 for both
// AES-GCM and ChaCha20-Poly1305).
func sessionNonceSize(suite string) int {
	_ = aes.BlockSize // keep crypto/aes import for AES-GCM seal path
	return 12
}

// sealSession authenticates+encrypts under suite. Output is ciphertext only
// (caller prepends the nonce).
func sealSession(suite string, key, nonce, plaintext, aad []byte) ([]byte, error) {
	if NormalizeSessionCipher(suite) == SessionCipherChaCha20 {
		aead, err := chacha20poly1305.New(key)
		if err != nil {
			return nil, err
		}
		if len(nonce) != aead.NonceSize() {
			return nil, ErrCiphertextTooShort
		}
		return aead.Seal(nil, nonce, plaintext, aad), nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aesGCM.Seal(nil, nonce, plaintext, aad), nil
}

// openSession authenticates+decrypts nonce||ct under suite.
func openSession(suite string, key, ciphertext, aad []byte) ([]byte, error) {
	if NormalizeSessionCipher(suite) == SessionCipherChaCha20 {
		aead, err := chacha20poly1305.New(key)
		if err != nil {
			return nil, err
		}
		nonceSize := aead.NonceSize()
		if len(ciphertext) < nonceSize {
			return nil, ErrCiphertextTooShort
		}
		nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]
		plaintext, err := aead.Open(nil, nonce, body, aad)
		if err != nil {
			return nil, ErrDecryptFailed
		}
		return plaintext, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrCiphertextTooShort
	}
	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, body, aad)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}

// Decrypt decrypts data using AES-256-GCM with the session key
// Input is raw bytes: nonce(12) + ciphertext
func (sm *SessionManager) Decrypt(agentID string, ciphertext []byte) ([]byte, error) {
	return sm.DecryptWithAAD(agentID, ciphertext, nil)
}

// DecryptWithAAD decrypts data with the session's AEAD suite, authenticating
// the provided additional data (protocol v2 frame binding).
func (sm *SessionManager) DecryptWithAAD(agentID string, ciphertext, aad []byte) ([]byte, error) {
	session := sm.GetSession(agentID)
	if session == nil {
		return nil, ErrNoSession
	}

	plaintext, err := openSession(session.Cipher, session.SessionKey, ciphertext, aad)
	if err != nil {
		return nil, err
	}

	sm.IncrementMessageCount(agentID)

	return plaintext, nil
}

// DecryptB64 decrypts base64-encoded ciphertext
func (sm *SessionManager) DecryptB64(agentID string, encodedCiphertext string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encodedCiphertext)
	if err != nil {
		return nil, err
	}
	return sm.DecryptWithAAD(agentID, ciphertext, nil)
}

// DecryptWithAADB64 decrypts base64-encoded ciphertext authenticating the AAD.
func (sm *SessionManager) DecryptWithAADB64(agentID string, encodedCiphertext string, aad []byte) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encodedCiphertext)
	if err != nil {
		return nil, err
	}
	return sm.DecryptWithAAD(agentID, ciphertext, aad)
}

// CleanupExpiredSessions removes old sessions
func (sm *SessionManager) CleanupExpiredSessions(maxAge time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for agentID, session := range sm.sessions {
		if time.Since(session.LastUsed) > maxAge {
			delete(sm.sessions, agentID)
		}
	}
}

// SessionsLastUsed snapshots agent -> last-use time for interval-aware
// sweeping by the caller (e.g. long-sleep agents must not be evicted by a
// static idle timeout). Copies timestamps only, never key material.
func (sm *SessionManager) SessionsLastUsed() map[string]time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[string]time.Time, len(sm.sessions))
	for id, sess := range sm.sessions {
		if sess != nil {
			out[id] = sess.LastUsed
		}
	}
	return out
}

// SetSessionLastUsedForTest backdates one session's last-use time and reports
// whether the session existed. The interval-aware sweep evicts only after
// max(30m, 3x the agent interval), so a test cannot reach the eviction branch by
// waiting; this is the narrow seam it uses instead.
func (sm *SessionManager) SetSessionLastUsedForTest(agentID string, at time.Time) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sess, ok := sm.sessions[agentID]
	if !ok || sess == nil {
		return false
	}
	sess.LastUsed = at
	return true
}

// DropSession removes one agent's session. Used by interval-aware sweeps.
func (sm *SessionManager) DropSession(agentID string) {
	sm.mu.Lock()
	delete(sm.sessions, agentID)
	sm.mu.Unlock()
}
