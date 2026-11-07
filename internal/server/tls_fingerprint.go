package server

import (
	"crypto/tls"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// tlsProfile is a real, externally-observable server-side TLS fingerprint:
// an ordered cipher-suite preference list, a version range, and ALPN
// next-protos. Rotating between browser-like profiles changes what a
// JA3/JARM scanner sees on the wire. (uTLS cannot be applied server-side;
// the previous implementation claimed randomization but only cloned the
// config, so the fingerprint never actually changed.)
type tlsProfile struct {
	name         string
	minVersion   uint16
	maxVersion   uint16
	cipherSuites []uint16
	nextProtos   []string
}

func (p tlsProfile) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   p.minVersion,
		MaxVersion:   p.maxVersion,
		CipherSuites: p.cipherSuites,
		NextProtos:   p.nextProtos,
	}
}

// Browser-like profiles. Ordering of the TLS 1.3 suites is advisory in Go
// (GODEBUG defaults apply), but TLS 1.2 suite order and the version range are
// faithfully rendered in the ServerHello, which is what JA3/JARM observe.
var tlsProfiles = []tlsProfile{
	{
		name:       "chrome",
		minVersion: tls.VersionTLS12,
		maxVersion: tls.VersionTLS13,
		cipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
		},
		nextProtos: []string{"h2", "http/1.1"},
	},
	{
		name:       "firefox",
		minVersion: tls.VersionTLS12,
		maxVersion: tls.VersionTLS13,
		cipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
		},
		nextProtos: []string{"h2", "http/1.1"},
	},
	{
		name:       "edge",
		minVersion: tls.VersionTLS12,
		maxVersion: tls.VersionTLS13,
		cipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
		},
		nextProtos: []string{"h2", "http/1.1"},
	},
	{
		name:       "safari",
		minVersion: tls.VersionTLS12,
		maxVersion: tls.VersionTLS13,
		cipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
		},
		nextProtos: []string{"h2", "http/1.1"},
	},
}

type TLSFingerprintManager struct {
	mu        sync.RWMutex
	profiles  []tlsProfile
	pool      []int // indices into profiles eligible for rotation
	current   int
	rotateAt  time.Time
	enabled   bool
	rotateDur time.Duration
}

func profileIndexByName(name string) int {
	for i, p := range tlsProfiles {
		if p.name == name {
			return i
		}
	}
	return -1
}

// NewTLSFingerprintManager builds the rotation manager. A profileName of ""
// or "random" rotates across all browser profiles; a known name pins the
// pool to it (rotation becomes a no-op). Unknown names fall back to the
// full pool (config validation rejects them at startup separately).
func NewTLSFingerprintManager(jarmEnabled, ja3Enabled bool, rotateInterval, profileName string) *TLSFingerprintManager {
	if !jarmEnabled && !ja3Enabled {
		return nil
	}

	dur := 24 * time.Hour
	if rotateInterval != "" {
		if parsed, err := time.ParseDuration(rotateInterval); err == nil && parsed > 0 {
			dur = parsed
		}
	}

	pool := make([]int, len(tlsProfiles))
	for i := range tlsProfiles {
		pool[i] = i
	}
	if idx := profileIndexByName(profileName); idx >= 0 {
		pool = []int{idx}
	}

	tfm := &TLSFingerprintManager{
		profiles:  tlsProfiles,
		pool:      pool,
		enabled:   true,
		rotateDur: dur,
		rotateAt:  time.Now().Add(dur),
	}
	// #nosec G404 -- cipher-order profile pick: OPSEC shape, not key material.
	tfm.current = pool[rand.Intn(len(pool))] // #nosec G404
	return tfm
}

// CurrentProfile returns the active profile (for tests and stats).
func (tfm *TLSFingerprintManager) CurrentProfile() string {
	if tfm == nil || !tfm.enabled {
		return "none"
	}
	tfm.mu.RLock()
	defer tfm.mu.RUnlock()
	if time.Now().After(tfm.rotateAt) {
		go tfm.rotate()
	}
	return tfm.profiles[tfm.current].name
}

func (tfm *TLSFingerprintManager) rotate() {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()
	if time.Now().Before(tfm.rotateAt) {
		return
	}
	prev := tfm.current
	cands := make([]int, 0, len(tfm.pool))
	for _, next := range tfm.pool {
		if next != prev {
			cands = append(cands, next)
		}
	}
	if len(cands) > 0 {
		// #nosec G404 -- profile rotation pick, same rationale as New.
		tfm.current = cands[rand.Intn(len(cands))] // #nosec G404
	}
	// Single-entry pool (pinned profile): current unchanged, deadline extends.
	tfm.rotateAt = time.Now().Add(tfm.rotateDur)
	slog.Info("TLS fingerprint rotated", "profile", tfm.profiles[tfm.current].name)
}

// maybeRotate advances the profile when the deadline passed. Called from the
// per-handshake path so rotation is self-driving (no background goroutine to
// leak across listener restarts); the fast path is a single RLock.
func (tfm *TLSFingerprintManager) maybeRotate() {
	if tfm == nil || !tfm.enabled {
		return
	}
	tfm.mu.RLock()
	expired := time.Now().After(tfm.rotateAt)
	tfm.mu.RUnlock()
	if expired {
		tfm.rotate()
	}
}

// currentConfig clones base with the active profile applied. Pure per-call
// allocation: safe to hand to concurrent handshakes.
func (tfm *TLSFingerprintManager) currentConfig(base *tls.Config) *tls.Config {
	tfm.maybeRotate()
	tfm.mu.RLock()
	p := tfm.profiles[tfm.current]
	tfm.mu.RUnlock()

	cfg := base.Clone()
	profileCfg := p.tlsConfig()
	cfg.MinVersion = profileCfg.MinVersion
	cfg.MaxVersion = profileCfg.MaxVersion
	cfg.CipherSuites = profileCfg.CipherSuites
	cfg.NextProtos = profileCfg.NextProtos
	return cfg
}

// Live wraps a listener TLS config so every handshake negotiates the CURRENT
// profile: crypto/tls invokes GetConfigForClient per handshake, which mints a
// fresh clone. Without this, WrapTLSConfig's one-time clone baked the startup
// profile forever (rotation changed nothing on the wire).
//
// QUIC is intentionally excluded: quic-go ignores GetConfigForClient and pins
// ALPN to h3/fc2, so profile NextProtos would break it (see Snapshot usage).
func (tfm *TLSFingerprintManager) Live(base *tls.Config) *tls.Config {
	if tfm == nil || !tfm.enabled {
		return base
	}
	shim := base.Clone()
	shim.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return tfm.currentConfig(base), nil
	}
	return shim
}

// Snapshot applies the current browser-like profile to a TLS config ONCE,
// for transports that cannot negotiate per handshake (QUIC ignores
// GetConfigForClient and pins its own ALPN). Prefer Live everywhere else.
func (tfm *TLSFingerprintManager) Snapshot(base *tls.Config) *tls.Config {
	if tfm == nil || !tfm.enabled {
		return base
	}
	tfm.mu.RLock()
	p := tfm.profiles[tfm.current]
	tfm.mu.RUnlock()

	wrapped := base.Clone()
	profileCfg := p.tlsConfig()
	wrapped.MinVersion = profileCfg.MinVersion
	wrapped.MaxVersion = profileCfg.MaxVersion
	wrapped.CipherSuites = profileCfg.CipherSuites
	wrapped.NextProtos = profileCfg.NextProtos
	return wrapped
}

func (s *Server) initTLSFingerprint() { // config-lock: boot, the fingerprint manager is built before serving
	if s.cfg.TLSFingerprint.JARMEnabled || s.cfg.TLSFingerprint.JA3Enabled {
		rotateInterval := s.cfg.TLSFingerprint.JA3Rotate
		if rotateInterval == "" {
			rotateInterval = s.cfg.TLSFingerprint.JARMRotate
		}
		s.tlsFingerprint = NewTLSFingerprintManager(
			s.cfg.TLSFingerprint.JARMEnabled,
			s.cfg.TLSFingerprint.JA3Enabled,
			rotateInterval,
			s.cfg.TLSFingerprint.JA3Profile,
		)
		if s.tlsFingerprint != nil {
			slog.Info("Server TLS fingerprint rotation enabled (cipher order/version/ALPN)",
				"profile", s.tlsFingerprint.CurrentProfile(),
				"rotate", s.cfg.TLSFingerprint.JARMRotate,
			)
		}
	}
}
