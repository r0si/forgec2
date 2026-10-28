package server

import (
	"errors"
	"net/http"
	"time"
)

// guardedClient builds an outbound client whose redirects are validated hop by hop.
//
// It exists because the same four-line construct appeared twice - ssrfSafeClient for webhooks and
// fetchers, aiSSRFClient for AI providers - and the second copy missed the timeout guard the first one
// had been given, so a nil/zero base produced a client that waits forever (http.DefaultClient has no
// Timeout). A single implementation means a fix lands in both, and the shape is easy to check with a
// gate: nothing else in the tree may write `Timeout: base.Timeout`.
func guardedClient(base *http.Client, fallbackTimeout time.Duration, validate func(string) error) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	// A zero Timeout means "no timeout": the default client has none, and neither does a hand-built
	// one. Fall back to the caller's bound instead of inheriting "forever".
	timeout := base.Timeout
	if timeout <= 0 {
		timeout = fallbackTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: base.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxGuardedRedirects {
				return errors.New("too many redirects")
			}
			return validate(req.URL.String())
		},
	}
}

// maxGuardedRedirects caps a redirect chain so an allowlisted host cannot be used as a springboard.
const maxGuardedRedirects = 5
