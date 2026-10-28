package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Bundle verification: does this archive still hold what its manifest says it holds.
//
// A handover travels - emailed, copied, re-zipped, sometimes transcoded by a mail gateway - and MANIFEST.json publishes
// a SHA-256 per member so a receiver can check. This endpoint is the server side of that check, for the same reason the
// plugin index has one: the party who produced a package is the party who can say whether a copy of it is intact.
//
// **What it proves and what it does not.** The digests are unkeyed and the manifest travels INSIDE the archive, so a
// deliberate rebuild of every member and every digest verifies. Like the audit chain, this detects damage and partial
// edits, not an adversary who redoes the whole package; only a signature made with a key that never travelled would
// close that gap, and saying so is more useful than implying otherwise.
//
// POST /api/engagements/:id/bundle/verify   (body: the zip, application/zip)

const engagementBundleVerifyMaxBytes = 128 << 20

// handleVerifyEngagementBundle re-computes the manifest digests of an uploaded bundle.
func (s *Server) handleVerifyEngagementBundle(c *gin.Context) {
	if _, _, ok := s.buildEngagementSummary(c); !ok {
		return // unknown engagement for this tenant; the summary helper already answered
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, engagementBundleVerifyMaxBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to read the uploaded bundle (limit 128 MiB)")
		return
	}
	if len(raw) == 0 {
		respondError(c, http.StatusBadRequest, "empty body: send the bundle zip as application/zip")
		return
	}

	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		respondError(c, http.StatusBadRequest, "the body is not a zip archive")
		return
	}

	contents := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			respondError(c, http.StatusBadRequest, "failed to open a member of the uploaded bundle")
			return
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			respondError(c, http.StatusBadRequest, "failed to read a member of the uploaded bundle")
			return
		}
		contents[file.Name] = content
	}

	manifestRaw, hasManifest := contents["MANIFEST.json"]
	if !hasManifest {
		respondError(c, http.StatusBadRequest, "the archive has no MANIFEST.json, so there is nothing to verify against")
		return
	}
	var manifest struct {
		Engagement struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"engagement"`
		GeneratedAt string           `json:"generated_at"`
		Signature   *bundleSignature `json:"signature"`
		Members     []struct {
			Name        string `json:"name"`
			Purpose     string `json:"purpose"`
			Source      string `json:"source"`
			Sensitivity string `json:"sensitivity"`
			SHA256      string `json:"sha256"`
			Bytes       int    `json:"bytes"`
		} `json:"members"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		respondError(c, http.StatusBadRequest, "MANIFEST.json is not valid JSON")
		return
	}

	type problem struct {
		Name    string `json:"name"`
		Problem string `json:"problem"`
	}
	checked := 0
	verified := 0
	unverifiable := make([]problem, 0, 2)
	problems := make([]problem, 0, 4)
	for _, entry := range manifest.Members {
		checked++
		content, present := contents[entry.Name]
		if !present {
			problems = append(problems, problem{entry.Name, "listed in the manifest but missing from the archive"})
			continue
		}
		if entry.SHA256 == "" {
			// MANIFEST.json is the legitimate case: it cannot contain its own digest.
			unverifiable = append(unverifiable, problem{entry.Name, "no digest published"})
			continue
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != entry.SHA256 {
			problems = append(problems, problem{entry.Name, fmt.Sprintf("digest mismatch: archive %s, manifest %s", got[:16], entry.SHA256[:16])})
			continue
		}
		if entry.Bytes != 0 && entry.Bytes != len(content) {
			problems = append(problems, problem{entry.Name, fmt.Sprintf("size mismatch: archive %d, manifest %d", len(content), entry.Bytes)})
			continue
		}
		verified++
	}
	// A member that is present but not listed is also worth reporting: it arrived from somewhere.
	listed := make(map[string]bool, len(manifest.Members))
	for _, entry := range manifest.Members {
		listed[entry.Name] = true
	}
	extra := make([]string, 0, 2)
	for name := range contents {
		if !listed[name] {
			extra = append(extra, name)
		}
	}

	// The signature is checked LAST, against the digests as they are written in the manifest - and the payload is
	// rebuilt from those digests, so a manifest whose numbers were edited after signing fails here even when every
	// member still matches its (edited) digest.
	descriptions := make([]bundleMemberDescription, 0, len(manifest.Members))
	for _, entry := range manifest.Members {
		descriptions = append(descriptions, bundleMemberDescription{
			Name: entry.Name, Purpose: entry.Purpose, Source: entry.Source,
			Sensitivity: entry.Sensitivity, SHA256: entry.SHA256, Bytes: entry.Bytes,
		})
	}
	payload := bundleManifestPayload(manifest.Engagement.ID, manifest.GeneratedAt, descriptions)
	signatureValid, signatureProblem := verifyBundleSignature(manifest.Signature, payload)
	// A key carried in the same archive proves nothing about origin, so `trusted` requires the caller to state the key
	// they already trust. Without that, a swapped manifest and a swapped key verify together.
	expectedKey := strings.ToLower(strings.TrimSpace(c.Query("public_key")))
	trusted := signatureValid && expectedKey != "" && strings.ToLower(manifest.Signature.PublicKey) == expectedKey
	signatureReport := gin.H{
		"present": manifest.Signature != nil,
		"valid":   signatureValid,
		"trusted": trusted,
		"problem": signatureProblem,
	}
	if manifest.Signature != nil {
		signatureReport["public_key"] = manifest.Signature.PublicKey
		signatureReport["algorithm"] = manifest.Signature.Algorithm
		signatureReport["signed_members"] = manifest.Signature.SignedMembers
	}
	if expectedKey != "" && manifest.Signature != nil && strings.ToLower(manifest.Signature.PublicKey) != expectedKey {
		signatureReport["problem"] = "the manifest is signed by a different key than the one supplied"
	}

	inSync := len(problems) == 0 && len(extra) == 0
	s.LogAuditRecord(c, "engagement_bundle_verify", "engagement", fmt.Sprintf("%d", manifest.Engagement.ID),
		fmt.Sprintf("verified uploaded bundle: %d of %d member(s) intact, %d problem(s)", verified, checked, len(problems)), true, nil)

	respond(c, gin.H{
		"ok":           inSync,
		"checked":      checked,
		"verified":     verified,
		"mismatched":   problems,
		"unverifiable": unverifiable,
		"extra":        extra,
		"engagement":   manifest.Engagement,
		"generated_at": manifest.GeneratedAt,
		"signature":    signatureReport,
		"note": "digests are unkeyed and the manifest travels inside the archive, so this detects damage and " +
			"partial edits, not a package rebuilt wholesale. A signature closes that gap only when the receiver " +
			"compares it with a key they already trust, which is why trusted requires public_key",
	})
}
