// Package bundle verifies an engagement handover archive independently of the server that produced it.
//
// It exists because the receiving side of a handover is usually NOT a ForgeC2 operator: a customer, an after-action
// reviewer or a lawyer holds a zip and needs to answer three questions without a running console - did the files survive
// transit, does the manifest still describe them, and was the manifest signed by the key we already trust.
//
// The canonical payload lives here rather than in the server so the producing side and `cmd/verify-bundle` cannot drift
// apart: a payload built two ways would verify in one place and fail in the other.
package bundle

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Member is one entry of MANIFEST.json.
type Member struct {
	Name        string `json:"name"`
	Purpose     string `json:"purpose"`
	Source      string `json:"source"`
	Sensitivity string `json:"sensitivity"`
	SHA256      string `json:"sha256"`
	Bytes       int    `json:"bytes"`
}

// Signature describes how a manifest was signed.
type Signature struct {
	Algorithm     string `json:"algorithm"`
	PublicKey     string `json:"public_key"`
	Value         string `json:"value"`
	SignedMembers int    `json:"signed_members"`
}

// Manifest is MANIFEST.json.
type Manifest struct {
	Engagement struct {
		ID        uint   `json:"id"`
		Name      string `json:"name"`
		StartsAt  string `json:"starts_at"`
		EndsAt    string `json:"ends_at"`
		WindowEnd string `json:"window_end"`
	} `json:"engagement"`
	GeneratedAt   string     `json:"generated_at"`
	ServerVersion string     `json:"server_version"`
	Signed        bool       `json:"signed"`
	Signature     *Signature `json:"signature"`
	Members       []Member   `json:"members"`
}

// Payload builds the canonical string for a manifest assembled elsewhere. The server's manifest writer signs through
// this function and cmd/verify-bundle checks through it, so the two cannot build different payloads - which would
// verify on one side and look like tampering on the other.
func Payload(engagementID uint, generatedAt string, members []Member) []byte {
	manifest := Manifest{GeneratedAt: generatedAt, Members: members}
	manifest.Engagement.ID = engagementID
	return manifest.Payload()
}

// Payload is the canonical string a signature covers.
//
// An engagement id, the generation time and every member digest, joined with NUL: names and digests are arbitrary text,
// so a printable separator could let two different manifests produce one payload.
func (m Manifest) Payload() []byte {
	parts := make([]string, 0, len(m.Members)+2)
	parts = append(parts, fmt.Sprintf("engagement=%d", m.Engagement.ID), "generated_at="+m.GeneratedAt)
	ordered := append([]Member(nil), m.Members...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	for _, member := range ordered {
		parts = append(parts, member.Name+"="+member.SHA256)
	}
	return []byte(strings.Join(parts, "\x00"))
}

// SignatureValid reports whether the manifest's own signature verifies, and why not when it does not.
func (m Manifest) SignatureValid() (bool, string) {
	if m.Signature == nil {
		return false, "the manifest is unsigned"
	}
	if m.Signature.Algorithm != "ed25519" {
		return false, "unsupported signature algorithm " + m.Signature.Algorithm
	}
	publicKey, err := hex.DecodeString(m.Signature.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return false, "the manifest carries a malformed public key"
	}
	value, err := hex.DecodeString(m.Signature.Value)
	if err != nil || len(value) != ed25519.SignatureSize {
		return false, "the manifest carries a malformed signature"
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), m.Payload(), value) {
		return false, "the signature does not verify against the manifest contents"
	}
	return true, ""
}

// Problem is one member that did not check out.
type Problem struct {
	Name    string `json:"name"`
	Problem string `json:"problem"`
}

// Report is the verdict on one archive.
type Report struct {
	OK            bool      `json:"ok"`
	Checked       int       `json:"checked"`
	Verified      int       `json:"verified"`
	Mismatched    []Problem `json:"mismatched"`
	Unverifiable  []Problem `json:"unverifiable"`
	Extra         []string  `json:"extra"`
	Signed        bool      `json:"signed"`
	SignatureOK   bool      `json:"signature_valid"`
	SignatureNote string    `json:"signature_problem,omitempty"`
	PublicKey     string    `json:"public_key,omitempty"`
	// Trusted is only meaningful when the caller supplied the key they already trust.
	Trusted      bool   `json:"trusted"`
	TrustedNote  string `json:"trusted_problem,omitempty"`
	EngagementID uint   `json:"engagement_id"`
	Engagement   string `json:"engagement_name"`
	GeneratedAt  string `json:"generated_at"`
	// ServerVersion is the release that produced the archive, empty for a package made before it was recorded.
	ServerVersion string `json:"server_version,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

// Verify checks an archive against its own manifest.
//
// trustedKey is the hex Ed25519 public key the caller already trusts; empty means "none supplied", in which case a valid
// signature only proves internal consistency - the key travelled inside the archive.
func Verify(raw []byte, trustedKey string) (*Report, error) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("not a zip archive: %w", err)
	}
	contents := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", file.Name, err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file.Name, err)
		}
		contents[file.Name] = content
	}

	manifestRaw, ok := contents["MANIFEST.json"]
	if !ok {
		return nil, fmt.Errorf("the archive has no MANIFEST.json, so there is nothing to verify against")
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return nil, fmt.Errorf("MANIFEST.json is not valid JSON: %w", err)
	}

	report := &Report{
		Mismatched:    []Problem{},
		Unverifiable:  []Problem{},
		Extra:         []string{},
		EngagementID:  manifest.Engagement.ID,
		Engagement:    manifest.Engagement.Name,
		GeneratedAt:   manifest.GeneratedAt,
		ServerVersion: manifest.ServerVersion,
	}
	sum := sha256.Sum256(raw)
	report.ArchiveSHA256 = hex.EncodeToString(sum[:])

	for _, entry := range manifest.Members {
		report.Checked++
		content, present := contents[entry.Name]
		if !present {
			report.Mismatched = append(report.Mismatched, Problem{entry.Name, "listed in the manifest but missing from the archive"})
			continue
		}
		if entry.SHA256 == "" {
			// Legitimately true for MANIFEST.json: it cannot contain its own digest.
			report.Unverifiable = append(report.Unverifiable, Problem{entry.Name, "no digest published"})
			continue
		}
		digest := sha256.Sum256(content)
		if got := hex.EncodeToString(digest[:]); got != entry.SHA256 {
			report.Mismatched = append(report.Mismatched, Problem{entry.Name, "digest mismatch: the file differs from the manifest"})
			continue
		}
		if entry.Bytes != 0 && entry.Bytes != len(content) {
			report.Mismatched = append(report.Mismatched, Problem{entry.Name, "size mismatch: the file differs from the manifest"})
			continue
		}
		report.Verified++
	}

	listed := make(map[string]bool, len(manifest.Members))
	for _, entry := range manifest.Members {
		listed[entry.Name] = true
	}
	for name := range contents {
		if !listed[name] {
			report.Extra = append(report.Extra, name)
		}
	}
	sort.Strings(report.Extra)

	report.Signed = manifest.Signature != nil
	valid, problem := manifest.SignatureValid()
	report.SignatureOK = valid
	report.SignatureNote = problem
	if manifest.Signature != nil {
		report.PublicKey = manifest.Signature.PublicKey
	}
	switch {
	case trustedKey == "":
		report.TrustedNote = "no trusted key supplied: a valid signature only proves internal consistency, because the key travelled inside the archive"
	case manifest.Signature == nil:
		report.TrustedNote = "the bundle is unsigned"
	case !strings.EqualFold(manifest.Signature.PublicKey, trustedKey):
		report.TrustedNote = "the manifest is signed by a different key than the one supplied"
	case !valid:
		report.TrustedNote = "the signature does not verify"
	default:
		report.Trusted = true
	}

	report.OK = len(report.Mismatched) == 0 && len(report.Extra) == 0
	return report, nil
}
