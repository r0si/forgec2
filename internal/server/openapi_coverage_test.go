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
	"gopkg.in/yaml.v3"
)

// OpenAPI coverage: the published contract must describe what the server actually serves, and it must
// be usable - a path present but without an operationId or responses tells a codegen tool nothing.
//
// Two checks, both from the real router:
//   - every /api route the router registers appears in api/openapi.yaml (the gap that let three
//     endpoints stay code-only, invisible to check:openapi-types because that gate compares generated
//     types to the spec - it cannot see a route the spec never mentions);
//   - every documented operation has an operationId and at least one response, which is the minimum
//     for a client generator or a reader to do anything with it.
//
// The allowlist is shrink-only: when a route gains a spec entry, delete its line.
func TestDocumentedAPIRoutesAreInTheSpec(t *testing.T) {
	database := testutil.SetupTestDB(t)
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "test-secret-for-openapi-coverage-32!!"
	cfg.Server.OperatorAllowedCIDRs = []string{"10.0.0.0/8"}
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)

	s := New(cfg, database)
	s.SetupRoutes()

	spec := readSpec(t)

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
		"/api/login":         "documented as /login in the spec",
		"/api/logout":        "documented as /logout in the spec",
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
		if !spec.has(path) {
			missing = append(missing, path)
		}
	}

	// Guard against a vacuous pass: if SetupRoutes registered nothing (or the spec parser broke), every
	// assertion below would hold for the wrong reason.
	if len(seen) < 50 {
		t.Fatalf("only %d /api paths reached the comparison; the router or the spec parser broke", len(seen))
	}
	for _, path := range missing {
		t.Errorf("%s is registered but not in api/openapi.yaml (add it there, or record why it is undocumented)", path)
	}
	t.Logf("checked %d distinct /api paths against %d documented paths", len(seen)+len(undocumented), spec.pathCount)

	// The operations themselves must be describable.
	var incomplete []string
	for _, path := range spec.sortedPaths() {
		for _, op := range spec.operations[path] {
			if op.ID == "" {
				incomplete = append(incomplete, path+" "+op.Method+" has no operationId")
			}
			if op.Responses == 0 {
				incomplete = append(incomplete, path+" "+op.Method+" has no responses")
			}
		}
	}
	for _, problem := range incomplete {
		t.Errorf("%s; a documented operation without it cannot be used by a client generator", problem)
	}
	if len(incomplete) == 0 {
		t.Logf("all %d documented operations carry an operationId and at least one response", spec.operationCount())
	}
}

// specOperation is one documented method of one path.
type specOperation struct {
	Method    string
	ID        string
	Responses int
}

// specDocument is the parsed spec, normalised for comparison with gin's route strings.
type specDocument struct {
	operations map[string][]specOperation
	// pathCount is the number of paths in the document. operations is keyed by BOTH the plain and the
	// /api-prefixed spelling, so its size is not the documented path count - reporting it as one would
	// be a misleading number.
	pathCount int
}

func (d specDocument) has(path string) bool {
	return len(d.operations[path]) > 0
}

func (d specDocument) count() int { return len(d.operations) }

func (d specDocument) operationCount() int {
	total := 0
	for _, ops := range d.operations {
		total += len(ops)
	}
	return total
}

func (d specDocument) sortedPaths() []string {
	paths := make([]string, 0, len(d.operations))
	for path := range d.operations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// readSpec parses api/openapi.yaml with a real YAML parser.
//
// The first version scanned for `^  /path:` lines, which forced hand-rolled normalisation and produced
// 25 false "missing route" reports: the spec is inconsistent about the /api prefix and writes
// parameters as {id} where gin reports :id. Parsing the document lets both spellings be registered
// deliberately instead of by regex accident, and lets the operations themselves be inspected.
func readSpec(t *testing.T) specDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]interface{} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	if len(doc.Paths) < 100 {
		t.Fatalf("parsed only %d paths; the spec parser broke", len(doc.Paths))
	}

	spec := specDocument{
		operations: make(map[string][]specOperation, len(doc.Paths)*2),
		pathCount:  len(doc.Paths),
	}
	for path, methods := range doc.Paths {
		for method, body := range methods {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete", "head", "options":
			default:
				continue
			}
			op := specOperation{Method: strings.ToUpper(method)}
			if fields, ok := body.(map[string]interface{}); ok {
				if id, ok := fields["operationId"].(string); ok {
					op.ID = id
				}
				if responses, ok := fields["responses"].(map[string]interface{}); ok {
					op.Responses = len(responses)
				}
			}
			for _, candidate := range []string{path, "/api" + path} {
				normalised := normaliseAPIPath(braceParamsToWildcard(candidate))
				spec.operations[normalised] = append(spec.operations[normalised], op)
			}
		}
	}
	return spec
}

var braceParams = regexp.MustCompile(`\{[^}]*\}`)

// braceParamsToWildcard rewrites OpenAPI `{id}` parameters to `*`, matching normaliseAPIPath's handling
// of gin's `:id`.
func braceParamsToWildcard(path string) string {
	return braceParams.ReplaceAllString(path, "*")
}
