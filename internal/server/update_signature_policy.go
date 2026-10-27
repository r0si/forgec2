package server

import (
	"errors"
	"strings"
)

// errUpdateSignatureRequired is the fail-closed refusal: the operator asked for signed
// releases but no key can verify them.
var errUpdateSignatureRequired = errors.New(
	"hot update refused: crypto.require_release_signature is set but crypto.update_signing_key is empty")

// updateSignatureEnforced reports whether the hot update must verify a release signature
// with the configured key.
//
// Split out of performHotUpdate so the three-way policy can be tested without downloading
// a release: key present -> verify; key absent + require_release_signature -> refuse; key
// absent + not required -> warn and fall back to same-origin checksums only.
func updateSignatureEnforced(signingKey string, requireSig bool) (enforce bool, err error) {
	if strings.TrimSpace(signingKey) != "" {
		return true, nil
	}
	if requireSig {
		return false, errUpdateSignatureRequired
	}
	return false, nil
}
