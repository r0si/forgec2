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
		if got := exitCode(test.report, test.requireTrusted); got != test.want {
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
	if exitCode(untrusted, true) == exitCode(damaged, true) {
		t.Fatal("an unproven package and a damaged one share an exit code")
	}
}
