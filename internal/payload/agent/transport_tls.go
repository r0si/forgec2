//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
)

var pinnedCertSHA256 []byte // decoded from PinnedCertSHA256Str in init

func initTLSPinning() {
	if PinnedCertSHA256Str == "" {
		return
	}
	h, err := hex.DecodeString(PinnedCertSHA256Str)
	if err != nil || len(h) != 32 {
		if Debug {
			fmt.Printf("[!] Invalid pinned cert hash (need 64 hex chars): %v\n", err)
		}
		return
	}
	pinnedCertSHA256 = h
	if Debug {
		fmt.Printf("[+] Certificate pinning enabled: %s\n", PinnedCertSHA256Str)
	}
}

func newAgentTLSConfig(serverName string) *tls.Config {
	cfg := &tls.Config{
		// #nosec G402 -- operator option; pinned-cert mode below hard-fails on mismatch.
		InsecureSkipVerify: SkipTLSVerify, // #nosec G402
	}
	if serverName != "" {
		cfg.ServerName = serverName
	}
	if len(pinnedCertSHA256) > 0 {
		// A pin replaces chain validation: the standard verifier would reject a
		// self-signed teamserver certificate before VerifyPeerCertificate ran,
		// which made pinning useless on exactly the deployments that need it
		// (ForgeC2 auto-generates a self-signed cert). Verification moves into
		// the callback below, which is mandatory and additionally checks the
		// hostname when one is configured.
		cfg.InsecureSkipVerify = true
		// Read ServerName at verification time: quic-go fills it in after the
		// config is built, and the pin check should still see the real name.
		// #nosec G123 -- pin mode replaces chain validation by design; the callback below is the check.
		cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error { // #nosec G123
			return verifyPinnedCert(rawCerts, cfg.ServerName)
		}
	}
	return cfg
}

// pinnedCertVerifier adapts verifyPinnedCert to the stdlib/utls callback
// signature, binding the expected hostname for this dial.
func pinnedCertVerifier(serverName string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		return verifyPinnedCert(rawCerts, serverName)
	}
}

// verifyPinnedCert authenticates the peer by leaf-certificate hash and, when a
// server name is configured, by DNS name. Both checks are mandatory.
func verifyPinnedCert(rawCerts [][]byte, serverName string) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("no server certificate presented")
	}
	certHash := sha256.Sum256(rawCerts[0])
	if !bytes.Equal(certHash[:], pinnedCertSHA256) {
		return fmt.Errorf("certificate pin mismatch: got %s, want %s",
			hex.EncodeToString(certHash[:]), hex.EncodeToString(pinnedCertSHA256))
	}
	if serverName != "" {
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("pinned certificate could not be parsed: %w", err)
		}
		if err := leaf.VerifyHostname(serverName); err != nil {
			return fmt.Errorf("pinned certificate does not cover %q: %w", serverName, err)
		}
	}
	return nil
}
