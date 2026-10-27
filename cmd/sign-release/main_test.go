package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	errSigSize     = errors.New("wrong signature size")
	errSigMismatch = errors.New("signature mismatch")
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSignFileRoundTrips(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "forgec2-server-linux-amd64.sha256")
	writeFile(t, path, "abc123  forgec2-server-linux-amd64\n")

	if err := signFile(priv, path); err != nil {
		t.Fatalf("signFile: %v", err)
	}
	if _, err := os.Stat(path + ".sig"); err != nil {
		t.Fatalf("signature file missing: %v", err)
	}
	if err := verifyFile(pub, path); err != nil {
		t.Fatalf("verifyFile rejected a freshly signed file: %v", err)
	}
}

func TestVerifyFileRejectsTampering(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "forgec2-v2.7.0-linux-amd64.tar.gz.sha256")
	writeFile(t, path, "abc123  forgec2-v2.7.0-linux-amd64.tar.gz\n")
	if err := signFile(priv, path); err != nil {
		t.Fatalf("signFile: %v", err)
	}

	// A modified checksum file must not verify, even with the right key.
	writeFile(t, path, "deadbeef  forgec2-v2.7.0-linux-amd64.tar.gz\n")
	if err := verifyFile(pub, path); err == nil {
		t.Fatal("verifyFile accepted a modified file")
	}

	// Restoring the content does not help with a different key.
	writeFile(t, path, "abc123  forgec2-v2.7.0-linux-amd64.tar.gz\n")
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	if err := verifyFile(otherPub, path); err == nil {
		t.Fatal("verifyFile accepted a signature from another key")
	}
}

func TestVerifyFileRejectsMissingOrMalformedSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.sha256")
	writeFile(t, missing, "abc\n")
	if err := verifyFile(pub, missing); err == nil {
		t.Fatal("verifyFile accepted a file without a .sig")
	}

	malformed := filepath.Join(dir, "malformed.sha256")
	writeFile(t, malformed, "abc\n")
	writeFile(t, malformed+".sig", "not-hex")
	if err := verifyFile(pub, malformed); err == nil {
		t.Fatal("verifyFile accepted a non-hex signature")
	}

	short := filepath.Join(dir, "short.sha256")
	writeFile(t, short, "abc\n")
	writeFile(t, short+".sig", "00112233")
	if err := verifyFile(pub, short); err == nil {
		t.Fatal("verifyFile accepted a truncated signature")
	}
}

func TestParsePublicKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	hexKey := ""
	for _, b := range pub {
		hexKey += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0x0f])
	}
	parsed, err := parsePublicKey(hexKey)
	if err != nil {
		t.Fatalf("parsePublicKey rejected a valid key: %v", err)
	}
	if !parsed.Equal(pub) {
		t.Fatal("parsePublicKey returned a different key")
	}
	for _, bad := range []string{"", "zz", hexKey[:62], hexKey + "00"} {
		if _, err := parsePublicKey(bad); err == nil {
			t.Fatalf("parsePublicKey accepted %q", bad)
		}
	}
}

// TestSignatureInteropWithTheUpdater pins the cross-tool contract: the updater
// verifies the detached "<bin>.sha256.sig" asset over the EXACT bytes of the
// checksum file, reading the signature as hex text from an HTTP body (so a trailing
// newline must be tolerated). If sign-release ever changed its encoding - or the
// updater stopped trimming - releases would be published but every upgrade would be
// refused, and neither package's own tests would notice.
func TestSignatureInteropWithTheUpdater(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Same shape the release workflow signs: "<hash>  <name>\n".
	path := filepath.Join(dir, "forgec2-server-linux-amd64.sha256")
	checksum := "abc123  forgec2-server-linux-amd64\n"
	writeFile(t, path, checksum)

	if err := signFile(priv, path); err != nil {
		t.Fatalf("signFile: %v", err)
	}

	// Reproduce the updater: fetch the .sig body, trim it, hex-decode, verify over
	// the raw checksum bytes.
	sigBody, err := os.ReadFile(path + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	verify := func(body []byte, data string) error {
		sig, derr := hex.DecodeString(strings.TrimSpace(string(body)))
		if derr != nil {
			return derr
		}
		if len(sig) != ed25519.SignatureSize {
			return errSigSize
		}
		if !ed25519.Verify(pub, []byte(data), sig) {
			return errSigMismatch
		}
		return nil
	}
	if err := verify(sigBody, checksum); err != nil {
		t.Fatalf("the updater would reject a fresh signature: %v", err)
	}
	// A trailing newline added in transit must not break verification.
	if err := verify(append(append([]byte{}, sigBody...), byte(10)), checksum); err != nil {
		t.Fatalf("a trailing newline broke verification: %v", err)
	}
	// One changed byte of the checksum file invalidates it.
	if err := verify(sigBody, "abd123  forgec2-server-linux-amd64\n"); err == nil {
		t.Fatal("a tampered checksum file still verified")
	}
	// The wrong key does not verify.
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := hex.DecodeString(strings.TrimSpace(string(sigBody)))
	if err != nil {
		t.Fatal(err)
	}
	if ed25519.Verify(other, []byte(checksum), sig) {
		t.Fatal("a signature verified under an unrelated key")
	}
}
