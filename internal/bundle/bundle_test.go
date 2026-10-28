package bundle

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// TestVerifyAcceptsAGoodBundle is the receiving side's happy path: every published digest matches and the signature is
// internally consistent.
func TestVerifyAcceptsAGoodBundle(t *testing.T) {
	raw, manifest := sampleBundle(t, true)

	report, err := Verify(raw, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.OK {
		t.Fatalf("a good bundle failed: %+v", report)
	}
	if report.Checked != len(manifest.Members) || report.Verified != len(manifest.Members)-1 {
		t.Fatalf("checked = %d, verified = %d, want every member but the manifest", report.Checked, report.Verified)
	}
	if !report.Signed || !report.SignatureOK {
		t.Fatalf("signature = signed %v valid %v", report.Signed, report.SignatureOK)
	}
	// A key that travelled inside the archive is not proof of origin.
	if report.Trusted {
		t.Fatalf("a self-carried key was reported as trusted: %q", report.TrustedNote)
	}
	if !strings.Contains(report.TrustedNote, "no trusted key supplied") {
		t.Fatalf("the limitation is not explained: %q", report.TrustedNote)
	}
}

// TestVerifyTrustsTheCallersKey covers the only path to authenticity: the caller supplies the key they already hold.
func TestVerifyTrustsTheCallersKey(t *testing.T) {
	raw, manifest := sampleBundle(t, true)

	report, err := Verify(raw, manifest.Signature.PublicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.Trusted {
		t.Fatalf("the caller's own key did not make it trusted: %q", report.TrustedNote)
	}

	// A different key must be refused even though the signature itself is fine.
	other, err := Verify(raw, strings.Repeat("11", 32))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if other.Trusted || !strings.Contains(other.TrustedNote, "different key") {
		t.Fatalf("a foreign key = trusted %v (%q)", other.Trusted, other.TrustedNote)
	}
	if !other.SignatureOK {
		t.Fatalf("the signature should still be valid on its own terms: %q", other.SignatureNote)
	}
}

// TestVerifyDetectsDamage covers the whole point of publishing digests: a changed member is reported, and an unlisted
// member is too.
func TestVerifyDetectsDamage(t *testing.T) {
	raw, _ := sampleBundle(t, true)

	damaged := rebuild(t, raw, map[string][]byte{"tasks.csv": []byte("tampered")})
	report, err := Verify(damaged, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK {
		t.Fatal("a damaged member verified")
	}
	if len(report.Mismatched) == 0 || report.Mismatched[0].Name != "tasks.csv" {
		t.Fatalf("mismatched = %+v", report.Mismatched)
	}

	extra := rebuild(t, raw, map[string][]byte{"planted.txt": []byte("hello")})
	report, err = Verify(extra, "")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK || len(report.Extra) != 1 || report.Extra[0] != "planted.txt" {
		t.Fatalf("an unlisted member was accepted: %+v", report)
	}
}

// TestVerifyRejectsNonBundles keeps the error path honest: a file that is not a bundle says so instead of producing a
// misleading report.
func TestVerifyRejectsNonBundles(t *testing.T) {
	if _, err := Verify([]byte("not a zip"), ""); err == nil {
		t.Fatal("a non-zip was accepted")
	}
	noManifest := rebuild(t, mustZip(t, map[string][]byte{"readme.txt": []byte("x")}), nil)
	if _, err := Verify(noManifest, ""); err == nil {
		t.Fatal("a zip without MANIFEST.json was accepted")
	}
}

// sampleBundle builds an archive with two members and an optional signature.
func sampleBundle(t *testing.T, sign bool) ([]byte, Manifest) {
	t.Helper()

	files := map[string][]byte{
		"summary.md": []byte("# Summary\n"),
		"tasks.csv":  []byte("Time,Agent\n"),
	}
	manifest := Manifest{GeneratedAt: "2026-10-29T00:00:00Z"}
	manifest.Engagement.ID = 7
	manifest.Engagement.Name = "DMZ exercise"
	for _, name := range []string{"summary.md", "tasks.csv"} {
		digest := sha256.Sum256(files[name])
		manifest.Members = append(manifest.Members, Member{
			Name: name, Purpose: "test member", Source: "test", Sensitivity: "shareable",
			SHA256: hex.EncodeToString(digest[:]), Bytes: len(files[name]),
		})
	}
	// The manifest travels with the archive and carries no digest of itself, exactly as the server produces it.
	manifest.Members = append(manifest.Members, Member{Name: "MANIFEST.json", Purpose: "this file", Source: "generated", Sensitivity: "shareable"})

	if sign {
		seed := make([]byte, ed25519.SeedSize)
		for i := range seed {
			seed[i] = byte(i + 1)
		}
		key := ed25519.NewKeyFromSeed(seed)
		manifest.Signed = true
		manifest.Signature = &Signature{
			Algorithm:     "ed25519",
			PublicKey:     hex.EncodeToString(key.Public().(ed25519.PublicKey)),
			Value:         hex.EncodeToString(ed25519.Sign(key, manifest.Payload())),
			SignedMembers: len(manifest.Members),
		}
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	files["MANIFEST.json"] = encoded
	return mustZip(t, files), manifest
}

// mustZip packs the members.
func mustZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	// Deterministic order, so the archive digest is stable across runs.
	for _, name := range []string{"summary.md", "tasks.csv", "MANIFEST.json", "planted.txt", "readme.txt"} {
		content, ok := files[name]
		if !ok {
			continue
		}
		out, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := out.Write(content); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// rebuild re-packs an archive, replacing or adding the given members.
func rebuild(t *testing.T, raw []byte, replacements map[string][]byte) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	files := map[string][]byte{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content := make([]byte, file.UncompressedSize64)
		n, err := handle.Read(content)
		_ = handle.Close()
		if err != nil && n == 0 {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		files[file.Name] = content[:n]
	}
	for name, content := range replacements {
		files[name] = content
	}
	return mustZip(t, files)
}
