package server

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/forgec2/forgec2/internal/infrastructure"
)

// redirectorHostPattern is a hostname or IPv4 literal. Deliberately strict: the value is written
// into nginx/apache/haproxy directives verbatim.
var redirectorHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9\-]{0,62}))*$`)

// validateRedirectorConfig rejects input that would make the generated config invalid or
// injectable.
//
// The generators write every field into nginx/apache/haproxy syntax with no escaping, and the
// handlers take those fields straight from a JSON body. A domain such as
// `c2.example.test;\n    server attacker.example;` therefore produced a config the operator would
// paste onto their own redirector with the extra directive in it. Nothing here is attacker-reachable
// without an operator, but "the tool emitted a config with injected directives" is still a defect in
// the tool - and the same fields can arrive from a saved template or an AI tool call.
func validateRedirectorConfig(rc infrastructure.RedirectorConfig) error {
	// Validate the RAW value: trimming first let "c2.example.test\n" through, and the generator
	// then wrote the newline into the directive - the exact injection the check exists to stop.
	domain := rc.Domain
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	if domain != strings.TrimSpace(domain) {
		return fmt.Errorf("domain must not contain whitespace")
	}
	if !redirectorHostPattern.MatchString(domain) {
		return fmt.Errorf("domain %q is not a hostname", domain)
	}
	backend := strings.TrimSpace(rc.BackendURL)
	if backend != "" {
		u, err := url.Parse(backend)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("backend_url must be an http(s) URL")
		}
		if strings.ContainsAny(backend, " \t\r\n;{}") {
			return fmt.Errorf("backend_url contains characters that cannot appear in a proxy directive")
		}
	}
	for _, path := range rc.ExtC2Paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return fmt.Errorf("extc2_paths contains an empty entry")
		}
		if strings.ContainsAny(trimmed, " \t\r\n;{}?#") || !strings.HasPrefix(trimmed, "/") {
			return fmt.Errorf("extc2_paths entry %q must be a plain path starting with /", trimmed)
		}
	}
	for _, ip := range rc.BlockedIPs {
		trimmed := strings.TrimSpace(ip)
		if trimmed == "" {
			return fmt.Errorf("blocked_ips contains an empty entry")
		}
		if net.ParseIP(trimmed) == nil && !isCIDR(trimmed) {
			return fmt.Errorf("blocked_ips entry %q is not an IP or CIDR", trimmed)
		}
	}
	if strings.ContainsAny(rc.UserAgent, "\r\n") {
		return fmt.Errorf("user_agent must be a single line")
	}
	if rc.CertPath != "" && strings.ContainsAny(rc.CertPath, "\r\n;{}") {
		return fmt.Errorf("cert_path contains characters that cannot appear in a directive")
	}
	if rc.KeyPath != "" && strings.ContainsAny(rc.KeyPath, "\r\n;{}") {
		return fmt.Errorf("key_path contains characters that cannot appear in a directive")
	}
	return nil
}

func isCIDR(s string) bool {
	_, _, err := net.ParseCIDR(s)
	return err == nil
}
