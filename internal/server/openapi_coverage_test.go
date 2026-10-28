package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/testutil"
)

// TestDocumentedAPIRoutesAreInTheSpec is the other direction from TestFrontendAPIPathsExist: the
// console's calls are checked against the router, and this checks the router against the published
// API contract.
//
// It exists because three endpoints added in this batch were code-only. `check:openapi-types` compares
// the generated types to the spec, so it stays green when a route is missing from the spec entirely -
// the gate cannot see what the spec never mentions. An API consumer reading api/openapi.yaml simply
// could not discover them.
//
// Scope, stated on purpose: only routes registered under /api are checked, and the allowlist below
// records the ones that are deliberately undocumented. The list is shrink-only - when a route gains a
// spec entry, delete its line here.
func TestDocumentedAPIRoutesAreInTheSpec(t *testing.T) {
	database := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "test-secret-for-openapi-coverage-32!!"
	cfg.Server.OperatorAllowedCIDRs = []string{"10.0.0.0/8"}
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)

	s := New(cfg, database)
	s.SetupRoutes()

	specPaths := readSpecPaths(t)

	// Routes that are intentionally not in the published spec, each with the reason.
	undocumented := map[string]string{
		// The implant-facing endpoints authenticate a beacon, not a session, and are not part of the
		// operator API surface an external consumer codes against.
		"/api/report":        "beacon-facing (implant reports its result here)",
		"/api/task":          "beacon-facing (implant polls for work here)",
		"/api/register":      "beacon-facing (implant registration handshake)",
		"/api/download":      "beacon-facing (implant downloads a staged file)",
		"/api/upload":        "beacon-facing (implant uploads a collected file)",
		"/api/v1/report":     "beacon-facing, versioned alias",
		"/api/v1/task":       "beacon-facing, versioned alias",
		"/api/v1/register":   "beacon-facing, versioned alias",
		"/api/v1/download":   "beacon-facing, versioned alias",
		"/api/v1/upload":     "beacon-facing, versioned alias",
		"/api/login":         "documented separately as /login in the spec",
		"/api/logout":        "documented separately as /logout in the spec",
		"/api/ws":            "WebSocket upgrade; the spec covers the REST surface",
		"/api/ws/operator":   "WebSocket upgrade; the spec covers the REST surface",
		"/api/phishing/l/:t": "token-bearing public landing page, not part of the authenticated API",
		"/api/public/:token": "public landing page, not part of the authenticated API",
	}

	var missing []string
	seen := make(map[string]bool)
	for _, route := range s.router.Routes() {
		if route.Method != http.MethodGet && route.Method != http.MethodPost &&
			route.Method != http.MethodPut && route.Method != http.MethodPatch && route.Method != http.MethodDelete {
			continue
		}
		if !strings.HasPrefix(route.Path, "/api") {
			continue
		}
		path := normaliseAPIPath(route.Path)
		if seen[path] || undocumented[path] != "" {
			continue
		}
		seen[path] = true
		if !specPaths[path] {
			missing = append(missing, path)
		}
	}

	// Guard against a vacuous pass: if SetupRoutes registered nothing (or the spec parser broke), every
	// assertion below would hold for the wrong reason.
	if len(seen) < 50 {
		t.Fatalf("only %d /api paths reached the comparison; the router or the spec parser broke", len(seen))
	}
	if len(missing) > 0 {
		for _, path := range missing {
			t.Errorf("%s is registered but not in api/openapi.yaml (add it there, or record why it is undocumented)", path)
		}
		t.Logf("%d of the registered /api paths are missing from the spec", len(missing))
	}
	t.Logf("checked %d distinct /api paths against %d documented paths", len(seen)+len(undocumented), len(specPaths))
}

var specPathLine = regexp.MustCompile(`^  (/[^:]+):\s*$`)

// readSpecPaths returns the documented paths, normalised so `/agents/{id}` and `/agents/:id` compare
// equal.
func readSpecPaths(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	paths := make(map[string]bool, 2048)
	for _, line := range strings.Split(string(raw), "\n") {
		match := specPathLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		documented := match[1]
		// The spec is inconsistent about the /api prefix (some entries carry it, most do not), and it
		// writes parameters as {id} where the router reports :id. Both spellings are registered so a
		// route is only reported missing when neither form exists - otherwise this check would report
		// its own normalisation as drift, which is exactly what the first measurement did.
		for _, candidate := range []string{documented, "/api" + documented} {
			paths[normaliseAPIPath(candidate)] = true
			paths[normaliseAPIPath(braceParamsToWildcard(candidate))] = true
		}
	}
	if len(paths) < 100 {
		t.Fatalf("parsed only %d documented paths; the extractor broke", len(paths))
	}
	return paths
}

// braceParamsToWildcard rewrites OpenAPI `{id}` parameters to `*`, matching normaliseAPIPath's handling
// of gin's `:id`.
func braceParamsToWildcard(path string) string {
	return regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(path, "*")
}
