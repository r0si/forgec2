package server

import (
	"strings"
	"testing"
)

// TestNormaliseFailureText pins the signature normalisation, and exists because the patterns were once mangled.
//
// They were written through a shell that ate backslashes, so `\s` became "backslash, then s" - a class that matches the
// paths in a fixture and mangles real messages. The end-to-end test passed anyway. This one asserts the behaviour directly,
// including the two path styles and the whitespace states, so the same accident fails loudly.
func TestNormaliseFailureText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "posix path collapses",
			in:   "open /tmp/a/file.txt: permission denied",
			want: "open <path>: permission denied",
		},
		{
			name: "windows path collapses",
			in:   `open C:\Users\alice\secret.txt: access is denied`,
			want: "open <path>: access is denied",
		},
		{
			name: "two hosts, one problem",
			in:   "open /var/log/b/app.log: input/output error",
			want: normaliseFailureText("open /var/log/a/app.log: input/output error"),
		},
		{
			name: "pid and port collapse",
			in:   "connect to 127.0.0.1:445 failed after 3 attempts",
			want: "connect to <n>.<n>.<n>.<n>:<n> failed after <n> attempts",
		},
		{
			name: "hex handle collapses",
			in:   "NtOpenProcess failed with status 0xC0000022 for handle 0x00000000000000ff",
			want: "ntopenprocess failed with status <hex> for handle <hex>",
		},
		{
			name: "whitespace runs collapse",
			in:   "  task    failed \n after   retry  ",
			want: "task failed after retry",
		},
		{
			name: "case is folded",
			in:   "ACCESS IS DENIED",
			want: "access is denied",
		},
	}

	for _, test := range cases {
		if got := normaliseFailureText(test.in); got != test.want {
			t.Fatalf("%s:\n  in   %q\n  got  %q\n  want %q", test.name, test.in, got, test.want)
		}
	}

	// The property that makes the feature work: two hosts, one signature.
	left := normaliseFailureText(`open C:\tmp\a\file.txt: denied by policy`)
	right := normaliseFailureText(`open C:\tmp\b\file.txt: denied by policy`)
	if left != right {
		t.Fatalf("the same failure with a different path produced two signatures:\n  %q\n  %q", left, right)
	}
	if strings.Contains(left, `tmp`) {
		t.Fatalf("the path survived normalisation: %q", left)
	}
}
