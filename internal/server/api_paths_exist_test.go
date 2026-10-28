package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/testutil"
)

// TestFrontendAPIPathsExist checks the paths the console actually calls against the routes the
// engine actually has.
//
// Every `/api/...` literal in frontend/src/lib/api-paths.ts must resolve to a registered route, with
// `:param` and `${...}` both treated as one wildcard segment. A path that no longer exists is a
// button that 404s, and the `.catch()` blocks in the pages make it look like "no data" rather than
// an error - which is how a whole feature can die quietly.
//
// Two failures this check has to avoid, both of which I hit by hand while building it:
//   - a text parser of routes*.go is not enough: routes are registered on GROUPS
//     (`auth.Group("/api/plugins")` + `GET("/import")`), and `registerAPIRoutes(v1Group)` hides the
//     /api/v1 surface entirely, so the regex version reported 90 false positives;
//   - `/api/lateral/history/all` looks like a call with a literal id, but the handler special-cases
//     `agentID == "all"`, so pattern matching alone would call it a bug.
//
// Using the router itself answers both: the set comes from gin, not from source text.
func TestFrontendAPIPathsExist(t *testing.T) {
	// SetupRoutes needs the full server the other router tests build: New() wires the middleware
	// state (rate limiter, activity map) that SetupRoutes's groups depend on.
	database := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "test-secret-for-api-paths-32chars!!"
	cfg.Server.OperatorAllowedCIDRs = []string{"10.0.0.0/8"}
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)

	s := New(cfg, database)
	s.SetupRoutes()

	registered := make(map[string]bool)
	for _, route := range s.router.Routes() {
		if route.Method != http.MethodGet && route.Method != http.MethodPost &&
			route.Method != http.MethodPut && route.Method != http.MethodPatch && route.Method != http.MethodDelete {
			continue
		}
		registered[normaliseAPIPath(route.Path)] = true
	}
	if len(registered) == 0 {
		t.Fatal("the router reports no routes; SetupRoutes did not run")
	}

	apiPaths := readAPIPathsFile(t)
	if len(apiPaths) == 0 {
		t.Fatal("no /api paths found in api-paths.ts; the extractor broke")
	}

	// Paths that are deliberately not registered routes, each with the reason it is safe.
	allowed := map[string]string{
		"/api/lateral/history/all": "the :id handler special-cases the literal \"all\" (handleLateralHistory)",
	}

	var missing []string
	for _, path := range apiPaths {
		normalised := normaliseAPIPath(path)
		// Illustrative templates in comments (open-ended, ending in a wildcard after an
		// interpolation) are not calls.
		if normalised == "/api" || normalised == "/api/*" || strings.HasSuffix(normalised, "/*") && strings.Contains(path, "${") {
			continue
		}
		if _, ok := allowed[path]; ok {
			continue
		}
		if !registered[normalised] {
			missing = append(missing, path+"  (normalised: "+normalised+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d frontend path(s) have no registered route:\n  %s\n"+
			"(paths: pass an id or drop the call; routes: register the endpoint)",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// normaliseAPIPath collapses parameters so a frontend template and a gin route compare equal:
// "/api/agents/${id}/timeline" and "/api/agents/:id/timeline" both become "/api/agents/*/timeline".
func normaliseAPIPath(path string) string {
	path = strings.TrimSuffix(path, "/")
	path = regexp.MustCompile(`\$\{[^}]*\}`).ReplaceAllString(path, "*")
	path = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`).ReplaceAllString(path, "*")
	// An unterminated interpolation (a nested template such as `${query ? `?${query}` : ""}`) leaves
	// this trailing fragment; it is a query, not a path segment.
	if i := strings.Index(path, "${"); i >= 0 {
		path = path[:i]
	}
	path = regexp.MustCompile(`\*+`).ReplaceAllString(path, "*")
	path = strings.TrimSuffix(path, "/")
	return path
}

// readAPIPathsFile pulls the /api/... string and template literals out of api-paths.ts.
func readAPIPathsFile(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "lib", "api-paths.ts"))
	if err != nil {
		t.Skipf("api-paths.ts not readable from this checkout: %v", err)
	}
	// Go's regexp has no backreferences, so scan the three literal kinds separately.
	patterns := []*regexp.Regexp{
		regexp.MustCompile("`([^`]*/api/[^`]*)`"),
		regexp.MustCompile(`"([^"]*/api/[^"]*)"`),
		regexp.MustCompile(`'([^']*/api/[^']*)'`),
	}
	seen := make(map[string]bool)
	var paths []string
	var rawMatches []string
	for _, line := range strings.Split(string(source), "\n") {
		// Documentation uses backticked paths too (`/agents/:id/*`), and a comment is not a call.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		for _, pattern := range patterns {
			for _, m := range pattern.FindAllStringSubmatch(line, -1) {
				rawMatches = append(rawMatches, m[1])
			}
		}
	}
	for _, raw := range rawMatches {
		// Documentation placeholders ("/api/...") are not calls.
		if strings.Contains(raw, "...") {
			continue
		}
		// Query strings are not part of the route.
		if i := strings.IndexAny(raw, "?"); i >= 0 {
			raw = raw[:i]
		}
		// A template that interpolates mid-path still has to resolve, but stop at the first
		// interpolation only when it is followed by a query fragment.
		if !strings.HasPrefix(raw, "/api/") {
			continue
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		paths = append(paths, raw)
	}
	return paths
}
