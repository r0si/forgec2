package server

import (
	"os"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/testutil"
	"regexp"
	"sort"
	"strings"
)

// TestDiagnosticsDocPathsExist keeps docs/DIAGNOSTICS.md honest against the router.
//
// The page's whole value is that a reader can copy a path from it. A renamed endpoint would leave a dead path in a reference
// document, and the reader would conclude the feature is broken rather than the page. Every `/api/...` token it mentions must be
// registered, with `{id}` and `:id` treated as the same thing.
func TestDiagnosticsDocPathsExist(t *testing.T) {
	raw, err := os.ReadFile("../../docs/DIAGNOSTICS.md")
	if err != nil {
		t.Fatalf("the diagnostics page is missing: %v", err)
	}
	doc := string(raw)

	// Collect the paths that exist, in both spellings.
	registered := map[string]bool{}
	for _, route := range testingServerRoutes(t) {
		registered[route] = true
		registered[strings.ReplaceAll(route, ":id", "{id}")] = true
		registered[strings.ReplaceAll(route, ":taskId", "{taskId}")] = true
	}
	if len(registered) < 50 {
		t.Fatalf("only %d routes resolved; the guard is not looking at the router", len(registered))
	}

	token := regexp.MustCompile("`(/(?:api/)?[A-Za-z0-9{}:_/.-]+)`")
	missing := make([]string, 0, 4)
	for _, match := range token.FindAllStringSubmatch(doc, -1) {
		path := match[1]
		// Only /api paths are this guard's business: /ready and the docs page's own prose are not routes under test.
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		// A path written with a trailing parameter placeholder is matched by its registered form.
		if registered[path] || registered[strings.TrimSuffix(path, "/")] {
			continue
		}
		// The document uses `{id}` where the router uses `:id`.
		normalised := strings.NewReplacer("{id}", ":id", "{taskId}", ":taskId").Replace(path)
		if registered[normalised] {
			continue
		}
		missing = append(missing, path)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("docs/DIAGNOSTICS.md names paths that are not registered: %s", strings.Join(missing, ", "))
	}

	// The page's two load-bearing claims, pinned so a future edit cannot quietly drop them.
	for _, needle := range []string{"side_effects: true", "confirm: true", "only get worse"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the diagnostics page no longer states %q", needle)
		}
	}
	// The two conventions are the page's other load-bearing content: a scope that every report takes, and exports that name
	// what they contain. A page that drops either is describing a system nobody runs.
	for _, needle := range []string{"engagement=none", taskExportScopeHeader, auditExportScopeHeader, "X-Audit-Export-Filtered"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the diagnostics page no longer mentions %q", needle)
		}
	}

	// The rerun section explains behaviour that is otherwise invisible: the same task submitted twice returns the row it already
	// created, which is what makes a double-clicked bulk button safe.
	for _, needle := range []string{"idempotency key", "already queued", "rerun:<id>"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the diagnostics page no longer explains %q", needle)
		}
	}

	// And it must keep the instructions to exclude the side-effecting entries from a bulk call.
	if !strings.Contains(doc, "run all diagnostics") {
		t.Fatal("the diagnostics page no longer warns about running every diagnostic")
	}
}

// testingServerRoutes returns the registered route patterns of a fully built server.
//
// Built the same way the OpenAPI coverage test builds it - a minimal tenant server has no router, which is how this guard
// panicked on its first run rather than reporting a missing path.
func testingServerRoutes(t *testing.T) []string {
	t.Helper()
	database := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "test-secret-for-diagnostics-doc-guard!"
	cfg.Server.OperatorAllowedCIDRs = []string{"10.0.0.0/8"}
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)
	s := New(cfg, database)
	s.SetupRoutes()

	routes := make([]string, 0, 512)
	for _, route := range s.router.Routes() {
		routes = append(routes, route.Path)
	}
	return routes
}
