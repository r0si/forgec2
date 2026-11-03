package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestDocumentedEndpointPathsExist keeps every operator-facing document pointing at URLs the
// router actually serves.
//
// docs/AUDIT_EXPORT.md is "that third party's instructions" (its own words) and told a reader
// to GET /api/audit/export. No route registers that path: the endpoint lives on the auth
// group, which has no prefix, so the real path is /audit/export. A client that followed the
// document hit the server's NoRoute handler — the beacon ingestion surface — and got
// "400 invalid beacon payload" for an operator request. The sibling guard for
// docs/DIAGNOSTICS.md already existed but covered only that one file, and the capabilities
// document was checked against api/openapi.yaml rather than against the router, so a
// fictional path agreed with its own fiction.
//
// The sweep covers tokens an operator or a script author would actually type as an API
// request: a path named after an HTTP verb, and a backticked literal that starts with
// /api/. Everything else a document writes with a leading slash is a SPA page route
// (/agents/42/shell is a URL you open in a browser), a static prefix (/assets/), a host
// path (/etc/hosts) or prose shorthand (/api/v1/...), and this guard cannot judge those.
//
// Known gaps are stated as allowlist entries with a reason; anything else that does not
// resolve to a registered route fails the test.
func TestDocumentedEndpointPathsExist(t *testing.T) {
	registered := map[string]bool{}
	for _, entry := range testingServerMethodRoutes(t) {
		_, path, ok := strings.Cut(entry, " ")
		if !ok {
			continue
		}
		// A doc may name a parameter differently than the router does, and a worked example
		// fills it in with a literal ("/api/engagements/7/bundle").
		registered[normaliseExample(path)] = true
	}
	if len(registered) < 50 {
		t.Fatalf("only %d routes resolved; the guard is not looking at the router", len(registered))
	}

	// Paths a document may legitimately name without the router registering them.
	allow := map[string]string{
		// The beacon URI is a protocol URI, not an operator route: with a malleable profile
		// active it is whatever the profile says, and ARCHITECTURE.md describes it as such.
		"/beacon": "implant beacon URI shape, not a registered operator route",
		// A group prefix named in prose ("/api/v1 接入 operator guard"), not a call target.
		"/api/v1": "route-group prefix reference, not an endpoint",
		// docs/MATURITY_BLUEPRINT.md describes the design as it stood when written; the
		// /update-signing/* routes were deliberately removed (see routes_settings.go), and
		// a blueprint is a record of a plan, not a live instruction.
		"/api/update-signing/sign": "historical design note; the route was removed on purpose",
	}

	// A document teaches by counter-example too: "Do **not** invent `/api/loot`",
	// "→ /users (not /api/users)". A path named in a sentence that tells the reader NOT to
	// use it is the opposite of a call target, and flagging it would train everyone to
	// ignore the guard. Scoped to the words immediately before the token so a genuine
	// instruction ("list is not paginated: GET /api/agents") is still checked.
	negated := regexp.MustCompile(`(?i)\b(?:not|never|invent|avoid)\s+\S{0,3}$`)

	targets := []string{}
	for _, dir := range []string{"../../docs", "../.."} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := e.Name()
			// CHANGELOG records what shipped at the time; rewriting its history would be
			// falsifying it, and nobody follows a URL out of a changelog entry.
			if name == "CHANGELOG.md" {
				continue
			}
			targets = append(targets, filepath.Join(dir, name))
		}
	}
	if len(targets) < 5 {
		t.Fatalf("only %d documents swept; the guard is not reading the docs tree", len(targets))
	}

	// A path token an author would type: named after a verb, or a backticked /api/ literal.
	// `}` is part of the class so a template ("/tasks/{taskId}/output") is captured whole
	// rather than truncated at its first closing brace.
	verbPath := regexp.MustCompile(`(?i)\b(GET|POST|PUT|DELETE|PATCH)\s+(/[A-Za-z0-9{}/_.:,{}-]+)`)
	bareAPI := regexp.MustCompile("`(/api/[A-Za-z0-9{}/_.:,{}-]+)`")

	type finding struct {
		file, path string
	}
	var dead []finding
	seen := map[string]bool{}
	for _, file := range targets {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		doc := string(raw)

		candidates := map[string]bool{}
		for _, m := range verbPath.FindAllStringSubmatchIndex(doc, -1) {
			path := doc[m[4]:m[5]]
			if isNegated(doc, m[4], negated) {
				continue
			}
			candidates[path] = true
		}
		for _, m := range bareAPI.FindAllStringSubmatchIndex(doc, -1) {
			path := doc[m[2]:m[3]]
			if isNegated(doc, m[2], negated) {
				continue
			}
			candidates[path] = true
		}

		for path := range candidates {
			// Prose shorthand for a family ("/api/v1/...", "/agents/:id/...") names no single
			// endpoint and cannot be resolved. Checked BEFORE trimming, or trimming the dots
			// turns "/api/..." into "/api/" and the ellipsis is invisible to this test — which
			// is exactly how the first version of this guard reported two false positives.
			if strings.Contains(path, "...") {
				continue
			}
			// The class admits "," so a brace expansion is captured whole; that also lets a
			// half-width punctuation mark end a run-on ("/api/x, then..."). Trim it.
			path = strings.TrimRight(path, ",.;")
			if path == "/api" {
				continue
			}
			// A brace holding a comma is shell expansion ("/generate/{linux,exe}" meaning
			// "either of two commands"), not a route template ("/tasks/{taskId}/output").
			if i := strings.IndexByte(path, '{'); i >= 0 && strings.Contains(path[i:], ",") {
				continue
			}
			key := filepath.Base(file) + " " + path
			if seen[key] {
				continue
			}
			seen[key] = true
			canonical := normaliseExample(strings.TrimSuffix(path, "/"))
			if registered[canonical] || allow[path] != "" {
				continue
			}
			dead = append(dead, finding{filepath.Base(file), path})
		}
	}

	if len(dead) > 0 {
		sort.Slice(dead, func(i, j int) bool {
			if dead[i].file == dead[j].file {
				return dead[i].path < dead[j].path
			}
			return dead[i].file < dead[j].file
		})
		var b strings.Builder
		for _, d := range dead {
			b.WriteString("\n  " + d.file + ": " + d.path)
		}
		t.Fatalf("%d documented endpoints are not registered routes:%s", len(dead), b.String())
	}
}

// isNegated reports whether the words just before a path token tell the reader NOT to use it.
// The window is short on purpose: a full-file search would let one "do not invent /api/loot"
// excuse every bad path in the document.
func isNegated(doc string, at int, pattern *regexp.Regexp) bool {
	start := at - 40
	if start < 0 {
		start = 0
	}
	return pattern.MatchString(doc[start:at])
}

// normaliseExample folds route parameters and the literal values a worked example puts in
// their place into one comparable form: ":id", "{taskId}" and "7" all become "{}" so
// /api/engagements/7/bundle matches the registered /api/engagements/:id/bundle.
//
// Only whole segments are folded, so a real path that contains a number ("/v1/tasks") keeps
// its shape and a wrong one stays wrong.
func normaliseExample(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, ":") || (strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")) {
			segments[i] = "{}"
			continue
		}
		if _, err := strconv.Atoi(segment); err == nil {
			segments[i] = "{}"
		}
	}
	return strings.Join(segments, "/")
}

// TestDualUseAdvertisedPathsStateTheAcceptRequirement proves the machine-readable document
// teaches the one constraint a client cannot infer.
//
// /audit/export, /tasks/export and /tasks/:taskId/output are also console page routes, and
// the embedded-frontend middleware runs BEFORE routing (SetStaticFS is called before
// SetupRoutes in cmd/server/main.go), so a GET without `Accept: application/json` is answered
// with the console's index.html and status 200. Verified against the running deployment:
// 200 text/html, 2364 bytes. A script that omits the header believes it downloaded an export
// and then cannot parse it, which is worse than an error.
func TestDualUseAdvertisedPathsStateTheAcceptRequirement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/exports/capabilities", nil)
	c.Set("user", "op")
	c.Set("user_role", "admin")
	c.Set("user_id", uint(1))
	s.handleExportCapabilities(c)

	var resp struct {
		Data struct {
			TaskExport exportCapabilities `json:"task_export"`
			Audit      exportCapabilities `json:"audit_export"`
			Output     exportCapabilities `json:"task_output_download"`
			Notes      []string           `json:"notes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	dualUse := []exportCapabilities{resp.Data.TaskExport, resp.Data.Audit, resp.Data.Output}
	for _, entry := range dualUse {
		if entry.Path == "" {
			t.Fatal("an export entry carries no path")
		}
		if !strings.HasPrefix(entry.Path, "/api/") && !mentionsAccept(entry) {
			t.Errorf("%s is a dual-use path but the document never states the Accept requirement", entry.Path)
		}
	}
	if !strings.Contains(w.Body.String(), "Accept: application/json") {
		t.Fatal("the capabilities document never mentions the Accept header, so a client cannot discover the requirement")
	}
}

func mentionsAccept(entry exportCapabilities) bool {
	for _, note := range entry.Notes {
		if strings.Contains(note, "Accept") {
			return true
		}
	}
	return false
}
