package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAIWebSearchAllowedHost pins the search-tool allowlist. The check is a
// suffix match on "."+suffix, so a lookalike domain that merely *contains* an
// allowlisted name must be refused, while real subdomains pass.
func TestAIWebSearchAllowedHost(t *testing.T) {
	allowed := []string{
		"wikipedia.org",
		"en.wikipedia.org",
		"attack.mitre.org",
		"mitre.org",
		"github.com",
		"lite.duckduckgo.com",
		"duckduckgo.com",
		"nvd.nist.gov",
		"cve.mitre.org",
		"microsoft.com",
		"WIKIPEDIA.ORG", // case-insensitive
	}
	for _, host := range allowed {
		if !aiWebSearchAllowedHost(host) {
			t.Errorf("aiWebSearchAllowedHost(%q) = false, want true", host)
		}
	}

	blocked := []string{
		"",
		"evilwikipedia.org",          // no dot boundary
		"wikipedia.org.evil.example", // allowlisted name in the middle
		"notduckduckgo.com",
		"duckduckgo.com.evil.test",
		"lite.duckduckgo.com.evil.test",
		"example.com",
		"127.0.0.1",
		"localhost",
	}
	for _, host := range blocked {
		if aiWebSearchAllowedHost(host) {
			t.Errorf("aiWebSearchAllowedHost(%q) = true, want false", host)
		}
	}
}

// TestExecuteAIWebSearchToolValidation covers the refusals that happen before
// any network call: malformed arguments, a blank query and an over-long query.
// A query at the limit would reach duckduckgo, so the happy path is exercised by
// hand rather than in the suite.
func TestExecuteAIWebSearchToolValidation(t *testing.T) {
	s := mustTenantServer(t)
	ctx := tenantOperatorCtx()

	cases := []struct{ name, args, wantSubstring string }{
		{"malformed", `{`, "invalid arguments"},
		{"blank query", `{"query":"   "}`, "query must be"},
		{"empty query", `{"query":""}`, "query must be"},
		{"over-long query", `{"query":"` + strings.Repeat("a", AIWebSearchMaxQueryLen+1) + `"}`, "query must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := s.executeAIWebSearchTool(ctx, tc.args)
			if !json.Valid([]byte(out)) {
				t.Fatalf("result is not JSON: %q", out)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
				t.Fatal(err)
			}
			msg, _ := payload["error"].(string)
			if !strings.Contains(msg, tc.wantSubstring) {
				t.Fatalf("error = %q, want it to mention %q", msg, tc.wantSubstring)
			}
		})
	}
}

// TestAIToolErrorJSON pins the escaping fix: an error message that contains a
// quote (a URL, for instance) must still produce valid JSON. The previous
// Sprintf form emitted it raw, so the tool-result parser received a broken
// document.
func TestAIToolErrorJSON(t *testing.T) {
	for _, msg := range []string{
		`plain error`,
		`quote " inside`,
		"newline\ninside",
		`backslash \ inside`,
		`Get "https://example.test/x": timeout`,
	} {
		out := aiToolErrorJSON(msg)
		if !json.Valid([]byte(out)) {
			t.Fatalf("aiToolErrorJSON(%q) produced invalid JSON: %s", msg, out)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		if payload["error"] != msg {
			t.Fatalf("round-trip = %q, want %q", payload["error"], msg)
		}
	}
}
