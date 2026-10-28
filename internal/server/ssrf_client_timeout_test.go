package server

import (
	"net/http"
	"testing"
	"time"
)

// TestSSRFSafeClientAlwaysHasATimeout pins the fallback on a client that otherwise waits forever.
//
// ssrfSafeClient copies base.Timeout verbatim, and its nil fallback is http.DefaultClient, whose
// Timeout is ZERO - meaning no timeout at all. Every other outbound client in the tree sets one
// (30s short, 5min long, or a feature-specific value), so the nil/zero case was the only way to get a
// request that hangs indefinitely, which is what a blackholed webhook or stager host looks like.
func TestSSRFSafeClientAlwaysHasATimeout(t *testing.T) {
	cases := []struct {
		name string
		base *http.Client
		want time.Duration
	}{
		{"nil falls back to the short bound", nil, HTTPClientShortTimeout},
		{"zero timeout is replaced", &http.Client{}, HTTPClientShortTimeout},
		{"an explicit timeout is preserved", &http.Client{Timeout: 5 * time.Minute}, 5 * time.Minute},
		{"a feature-specific timeout is preserved", &http.Client{Timeout: 90 * time.Second}, 90 * time.Second},
	}
	for _, tc := range cases {
		got := ssrfSafeClient(tc.base).Timeout
		if got != tc.want {
			t.Errorf("%s: Timeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSSRFSafeClientStillGuardsRedirects keeps the SSRF property while touching the timeout: the
// client must refuse a redirect to an internal address.
func TestSSRFSafeClientStillGuardsRedirects(t *testing.T) {
	c := ssrfSafeClient(&http.Client{Timeout: time.Second})
	if c.CheckRedirect == nil {
		t.Fatal("no redirect guard")
	}
	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := c.CheckRedirect(req, []*http.Request{req}); err == nil {
		t.Fatal("a redirect to the metadata address was allowed")
	}
}
