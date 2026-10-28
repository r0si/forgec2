package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// AI provider endpoints get their own SSRF wrapper instead of reusing
// validateExternalURL directly, for two reasons:
//
//  1. Local models. A red-team teamserver is frequently air-gapped or runs the
//     assistant against a local Ollama/vLLM. The shared validator rejects every
//     loopback and private address, which made that impossible. Operators can
//     now list specific host:port targets under ai.allowed_endpoints (or set
//     ai.allow_private_endpoint) and only those are exempted.
//  2. Blast radius. validateExternalURL also guards webhooks, notification
//     routes, update checks, file fetches and the stager. Relaxing it for the
//     AI feature would silently widen reach for all of those, so the relaxation
//     is confined to AI provider calls here.
//
// Everything not on the allowlist behaves exactly as before: loopback, private,
// link-local, CGNAT, cloud-metadata and 0.0.0.0/8 targets are refused, as are
// redirects into them.

// aiEndpointAllowlist is the set of host:port (or bare host) entries the
// operator has authorised for AI provider traffic. Rebuilt from config on every
// call so a config reload takes effect without a restart.
func (s *Server) aiEndpointAllowlist() map[string]bool {
	out := make(map[string]bool)
	if s == nil || s.cfg == nil {
		return out
	}
	s.configMu.RLock()
	allowAll := s.cfg.AI.AllowPrivateEndpoint
	entries := append([]string(nil), s.cfg.AI.AllowedEndpoints...)
	s.configMu.RUnlock()
	if allowAll {
		// Explicit opt-in to any private target. Still requires the URL to be
		// well-formed http/https and still blocks nothing else, but the
		// operator has taken responsibility for the whole private range.
		out["*"] = true
		return out
	}
	for _, raw := range entries {
		for _, key := range aiAllowlistKeys(raw) {
			out[key] = true
		}
	}
	return out
}

// aiAllowlistKeys reduces a configured allowlist entry to the key(s) used for
// matching. Entries may be a bare host ("ollama.internal"), a host:port
// ("localhost:11434") or a full URL ("http://localhost:11434/v1").
//
// The port is significant: "localhost:11434" yields the single key
// "localhost:11434" so it permits that port only. Only an entry written
// without a port yields a bare host key, which then matches any port on that
// host. Collapsing "host:port" to just "host" would silently widen the
// allowlist to every service on the machine, including SSH and the database.
func aiAllowlistKeys(raw string) []string {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return nil
	}
	var host, port string
	if strings.Contains(entry, "://") {
		u, err := url.Parse(entry)
		if err != nil || u.Hostname() == "" {
			return nil
		}
		host = strings.ToLower(u.Hostname())
		port = u.Port()
		if port == "" {
			port = defaultPortForScheme(u.Scheme)
		}
	} else if h, p, err := net.SplitHostPort(entry); err == nil {
		// Bare "host:port". url.Parse would have read "localhost:11434" as a
		// scheme, which is why this is split by hand.
		host, port = strings.ToLower(h), p
	} else {
		host = strings.ToLower(entry)
	}
	if host == "" {
		return nil
	}
	if port == "" {
		return []string{host}
	}
	return []string{host + ":" + port}
}

// aiEndpointAllowed reports whether raw's host:port matches an allowlist key.
// Keys are either "host" (any port) or "host:port" (that port only).
func aiEndpointAllowed(raw string, allow map[string]bool) bool {
	if len(allow) == 0 {
		return false
	}
	if allow["*"] {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if allow[host] {
		return true
	}
	port := u.Port()
	if port == "" {
		port = defaultPortForScheme(u.Scheme)
	}
	return port != "" && allow[host+":"+port]
}

func defaultPortForScheme(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// validateAIEndpoint applies the shared outbound-URL rules to an AI provider
// endpoint, honouring the operator's allowlist. The allowlisted case still
// parses and still requires http/https, it only skips the public-address
// requirement.
func (s *Server) validateAIEndpoint(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("AI endpoint is empty")
	}
	// Always enforce well-formedness and the scheme, allowlisted or not.
	u, err := url.Parse(trimmed)
	if err != nil {
		return errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL must be http or https")
	}
	if u.Hostname() == "" {
		return errors.New("URL has no host")
	}
	if aiEndpointAllowed(trimmed, s.aiEndpointAllowlist()) {
		// An allowlisted internal target is intentional. Log it so an operator
		// reviewing logs can see the assistant talking to a private address
		// while carrying an API key.
		slog.Info("AI endpoint is operator-allowlisted private address", "endpoint", trimmed)
		return nil
	}
	if err := validateExternalURL(trimmed); err != nil {
		return fmt.Errorf("AI endpoint blocked: %w%s", err, aiAllowlistHint(u))
	}
	return nil
}

// aiAllowlistHint appends the remedy to a blocked-endpoint error, because the
// most common cause by far is pointing the assistant at a local model.
func aiAllowlistHint(u *url.URL) string {
	hostPort := u.Host
	if p := u.Port(); p == "" {
		if p = defaultPortForScheme(u.Scheme); p != "" {
			hostPort = u.Hostname() + ":" + p
		}
	}
	return fmt.Sprintf(" (to permit a local model, add %q to ai.allowed_endpoints in config.yaml)", hostPort)
}

// aiSSRFClient wraps base with redirect validation that honours the same
// allowlist as validateAIEndpoint, so an allowlisted endpoint cannot be used
// as a springboard: a redirect to a non-allowlisted private address is still
// refused.
func (s *Server) aiSSRFClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	// Same hole ssrfSafeClient had (round 232): a zero Timeout means "wait forever", and the nil
	// fallback is http.DefaultClient, which has none. AI endpoints are slow, so the fallback is the
	// LONG bound rather than the short one - but it is a bound.
	timeout := base.Timeout
	if timeout <= 0 {
		timeout = HTTPClientLongTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: base.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return s.validateAIEndpoint(req.URL.String())
		},
	}
}
