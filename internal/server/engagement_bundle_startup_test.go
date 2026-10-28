package server

import (
	"strings"
	"testing"
)

// TestBundleSigningStartupNotice covers the boot-time warning about unsigned handovers.
//
// An unsigned bundle is a legitimate configuration - the per-member digests still detect damage - but "the handovers this
// exercise produced cannot be proven to come from us" is something an operator should learn at boot rather than after
// delivering one. The decision is a function so that the wording and the condition are both testable.
func TestBundleSigningStartupNotice(t *testing.T) {
	// 1. Unconfigured: a warning that names the variable and the consequence.
	t.Setenv(bundleSigningKeyEnv, "")
	message, ok := bundleSigningStartupNotice()
	if !ok {
		t.Fatal("no notice on an unconfigured deployment")
	}
	if !strings.Contains(message, "UNSIGNED") || !strings.Contains(message, bundleSigningKeyEnv) {
		t.Fatalf("the notice does not name the problem and the fix: %q", message)
	}
	if !strings.Contains(message, "integrity but not origin") {
		t.Fatalf("the notice does not say what is lost: %q", message)
	}

	// 2. Configured: silence. A warning that fires even when signing works trains operators to ignore it.
	t.Setenv(bundleSigningKeyEnv, "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if message, ok := bundleSigningStartupNotice(); ok {
		t.Fatalf("a warning was produced for a configured key: %q", message)
	}

	// 3. A malformed key is treated as unconfigured - the signer refuses it - so the warning comes back rather than
	// the deployment believing it signs while producing plain bundles.
	t.Setenv(bundleSigningKeyEnv, "not-a-hex-seed")
	if _, ok := bundleSigningStartupNotice(); !ok {
		t.Fatal("a malformed key silenced the warning even though nothing will be signed")
	}
}
