package main

import (
	"testing"

	"github.com/forgec2/forgec2/internal/bundle"
)

// TestExitCode covers the decision a pipeline makes, which is the whole point of the flags: without -require-trusted an
// intact package passes even when nobody can prove where it came from, and a CI gate that silently accepts that is
// worse than no gate.
func TestExitCode(t *testing.T) {
	intact := &bundle.Report{OK: true}
	damaged := &bundle.Report{OK: false}
	untrusted := &bundle.Report{OK: true, Signed: true, SignatureOK: true}
	trusted := &bundle.Report{OK: true, Signed: true, SignatureOK: true, Trusted: true}

	cases := []struct {
		name           string
		report         *bundle.Report
		requireTrusted bool
		want           int
	}{
		{"intact, trust not required", intact, false, 0},
		{"intact but unproven, trust not required", untrusted, false, 0},
		{"damaged", damaged, false, 1},
		{"trusted, trust required", trusted, true, 0},
		{"intact but unproven, trust required", untrusted, true, 3},
		{"damaged, trust required", damaged, true, 1},
	}
	for _, test := range cases {
		if got := exitCode(test.report, test.requireTrusted, false); got != test.want {
			t.Fatalf("%s: exit %d, want %d", test.name, got, test.want)
		}
	}
}

// TestExitCodeDistinguishesUntrustedFromDamaged pins the separate code: a pipeline needs to tell "this package is
// broken" apart from "this package is fine but not provably ours", because the first is a transfer problem and the
// second is a trust decision.
func TestExitCodeDistinguishesUntrustedFromDamaged(t *testing.T) {
	untrusted := &bundle.Report{OK: true, Signed: true, SignatureOK: true}
	damaged := &bundle.Report{OK: false}
	if exitCode(untrusted, true, false) == exitCode(damaged, true, false) {
		t.Fatal("an unproven package and a damaged one share an exit code")
	}
}

// TestExitCodeRequireComplete covers the completeness switch: a partial bundle is a legitimate product, and a failure
// for a caller who was promised the whole handover.
//
// The separate code matters: a pipeline must be able to tell "this package is broken" from "this package is deliberately
// the summary only", because the first needs a re-send and the second needs a conversation.
func TestExitCodeRequireComplete(t *testing.T) {
	complete := &bundle.Report{OK: true}
	partial := &bundle.Report{OK: true, Missing: []string{"credentials.json", "iocs.json"}}
	broken := &bundle.Report{OK: false, Missing: []string{"summary.md"}}

	cases := []struct {
		name            string
		report          *bundle.Report
		requireComplete bool
		want            int
	}{
		{"complete, not required", complete, false, 0},
		{"partial, not required", partial, false, 0},
		{"partial, required", partial, true, 4},
		{"complete, required", complete, true, 0},
		{"broken and partial", broken, true, 1},
	}
	for _, test := range cases {
		if got := exitCode(test.report, false, test.requireComplete); got != test.want {
			t.Fatalf("%s: exit %d, want %d", test.name, got, test.want)
		}
	}

	// The incompleteness code must not collide with damage or with the trust decision.
	if exitCode(partial, false, true) == exitCode(broken, false, true) {
		t.Fatal("an incomplete package and a broken one share an exit code")
	}
	untrusted := &bundle.Report{OK: true, Signed: true, SignatureOK: true}
	if exitCode(partial, true, true) == exitCode(untrusted, true, false) {
		t.Fatal("incomplete and untrusted share an exit code")
	}
}
