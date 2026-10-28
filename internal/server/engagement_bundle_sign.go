package server

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"strings"

	"github.com/forgec2/forgec2/internal/bundle"
)

// Bundle manifest signing.
//
// The digests in MANIFEST.json are unkeyed and the manifest travels inside the archive, so a package rebuilt wholesale
// verifies against itself. A signature over the manifest fixes half of that - it detects any change to the manifest
// after signing - and the other half depends on the receiver: a public key carried in the same archive proves nothing
// about origin, so authenticity only comes from a receiver comparing it with a key they already trust.
//
// This is why the verify endpoint distinguishes `signature_valid` (internally consistent) from `trusted` (matches the
// key the caller expected). Stating the difference is the point; collapsing it into "signed" would be a false comfort.
//
// Key source: FORGEC2_BUNDLE_SIGNING_KEY, a hex-encoded 32-byte Ed25519 seed, the same convention the plugin signing
// tool uses. Unset means bundles are produced unsigned, and the manifest says so.

const bundleSigningKeyEnv = "FORGEC2_BUNDLE_SIGNING_KEY"

// bundleSigner returns the configured signing key, or nil when the deployment has not set one.
func bundleSigner() ed25519.PrivateKey {
	raw := strings.TrimSpace(os.Getenv(bundleSigningKeyEnv))
	if raw == "" {
		return nil
	}
	seed, err := hex.DecodeString(raw)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil
	}
	return ed25519.NewKeyFromSeed(seed)
}

// bundleManifestPayload is the canonical string that gets signed.
//
// It covers what a receiver relies on - the engagement identity, when the manifest was produced, and the digest of
// every member - and it is built from the same list that is written into the manifest, so the signature cannot cover a
// different set from the one described. Fields are joined with NUL: a name or a digest containing a printable separator
// could otherwise make two different manifests produce one payload.
func bundleManifestPayload(engagementID uint, generatedAt string, members []bundleMemberDescription) []byte {
	// The canonical form lives in internal/bundle, which cmd/verify-bundle also uses: a payload built two ways would
	// verify on one side and fail on the other, and the failure would look like tampering.
	shared := make([]bundle.Member, 0, len(members))
	for _, member := range members {
		shared = append(shared, bundle.Member{
			Name: member.Name, Purpose: member.Purpose, Source: member.Source,
			Sensitivity: member.Sensitivity, SHA256: member.SHA256, Bytes: member.Bytes,
		})
	}
	return bundle.Payload(engagementID, generatedAt, shared)
}

// bundleSignature describes how a manifest was signed.
type bundleSignature struct {
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Value     string `json:"value"`
	// SignedMembers is how many member digests the signature covers, so a truncated manifest cannot claim to cover
	// everything.
	SignedMembers int `json:"signed_members"`
}

// signBundleManifest signs the payload when a key is configured.
func signBundleManifest(key ed25519.PrivateKey, payload []byte, members int) *bundleSignature {
	if key == nil {
		return nil
	}
	return &bundleSignature{
		Algorithm:     "ed25519",
		PublicKey:     hex.EncodeToString(key.Public().(ed25519.PublicKey)),
		Value:         hex.EncodeToString(ed25519.Sign(key, payload)),
		SignedMembers: members,
	}
}

// verifyBundleSignature checks a manifest signature against the key it carries.
func verifyBundleSignature(signature *bundleSignature, payload []byte) (bool, string) {
	if signature == nil {
		return false, "the manifest is unsigned"
	}
	if signature.Algorithm != "ed25519" {
		return false, "unsupported signature algorithm " + signature.Algorithm
	}
	publicKey, err := hex.DecodeString(signature.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return false, "the manifest carries a malformed public key"
	}
	value, err := hex.DecodeString(signature.Value)
	if err != nil || len(value) != ed25519.SignatureSize {
		return false, "the manifest carries a malformed signature"
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, value) {
		return false, "the signature does not verify against the manifest contents"
	}
	return true, ""
}
