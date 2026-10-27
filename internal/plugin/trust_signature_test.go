package plugin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestPackage writes a minimal package (manifest + one script) and returns its
// directory and canonical digest. manifest.yaml is excluded from the digest, which
// is what lets a package carry its own digest without becoming self-referential.
func newTestPackage(t *testing.T) (dir, digest string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("name: p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.ps1"), []byte("Write-Output 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := PackageDigest(dir, nil, dir)
	if err != nil {
		t.Fatalf("PackageDigest: %v", err)
	}
	return dir, d
}

// TestVerifyPackageSignatureBranches pins the branches that decide whether an
// untrusted plugin loads. The order matters: the digest is recomputed from disk
// first (so edited files are caught) and the signature covers the digest string
// (so a swapped digest invalidates the signature too).
func TestVerifyPackageSignatureBranches(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	dir, digest := newTestPackage(t)
	sig := ed25519.Sign(priv, []byte(digest))

	signed := func() *Manifest {
		return &Manifest{Name: "p", Digest: digest, Signature: hex.EncodeToString(sig)}
	}

	// A signature from a trusted key verifies.
	status, err := VerifyPackageWithPolicy(dir, dir, signed(), VerificationPolicy([]ed25519.PublicKey{pub}, true))
	if err != nil || !status.Verified {
		t.Fatalf("trusted signature = %+v, %v", status, err)
	}

	// A signature from a DIFFERENT key must be refused, even though the digest is
	// correct: this is the "signed, but not by anyone we trust" case.
	if _, err := VerifyPackageWithPolicy(dir, dir, signed(), VerificationPolicy([]ed25519.PublicKey{otherPub}, true)); err == nil {
		t.Fatal("a signature from an untrusted key was accepted")
	} else if !strings.Contains(err.Error(), "not valid for any configured") {
		t.Fatalf("unexpected error for an untrusted key: %v", err)
	}

	// A signature without any configured key is an error, not a pass.
	if _, err := VerifyPackageWithPolicy(dir, dir, signed(), VerificationPolicy(nil, true)); err == nil {
		t.Fatal("a signed package passed with no trusted keys configured")
	}

	// A malformed signature is refused.
	malformed := &Manifest{Name: "p", Digest: digest, Signature: "deadbeef"}
	if _, err := VerifyPackageWithPolicy(dir, dir, malformed, VerificationPolicy([]ed25519.PublicKey{pub}, true)); err == nil {
		t.Fatal("a malformed signature was accepted")
	}

	// Digest only: accepted when signatures are not required (the operator pinned
	// it out of band), refused when they are.
	unsigned := &Manifest{Name: "p", Digest: digest}
	if _, err := VerifyPackageWithPolicy(dir, dir, unsigned, VerificationPolicy(nil, false)); err != nil {
		t.Fatalf("an unsigned digest-only package was refused with require_signed off: %v", err)
	}
	if _, err := VerifyPackageWithPolicy(dir, dir, unsigned, VerificationPolicy(nil, true)); err == nil {
		t.Fatal("an unsigned package passed with require_signed on")
	}

	// A file edited after signing invalidates the digest before the signature is
	// even consulted.
	if err := os.WriteFile(filepath.Join(dir, "main.ps1"), []byte("Write-Output 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageWithPolicy(dir, dir, signed(), VerificationPolicy([]ed25519.PublicKey{pub}, true)); err == nil {
		t.Fatal("an edited file still verified")
	} else if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("unexpected error for an edited file: %v", err)
	}
}
