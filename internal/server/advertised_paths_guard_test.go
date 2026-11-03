package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// paramSpan rewrites a route pattern into a shape-comparable form: parameter NAMES differ
// between the router (`:id`) and a client document (`{taskId}`), but the URL is the same.
var paramSpan = regexp.MustCompile(`:[A-Za-z0-9_]+|\{[A-Za-z0-9_]+\}`)

func canonicalRoutePath(p string) string {
	return paramSpan.ReplaceAllString(p, "{}")
}

// TestAdvertisedEndpointPathsExist stops the server from publishing API paths it does not
// serve.
//
// The capabilities document exists so a client can build a request without reading the
// source, and five of its entries pointed at URLs no router registers:
// /api/tasks/export, /api/audit/export, /api/tasks/:taskId/output,
// /api/tasks/rerun-by-signature and /api/stats/failures. The endpoints are registered on the
// auth group, which has no prefix, so the real paths are /tasks/export and friends — the
// project's own convention, stated in frontend/src/lib/api-paths.ts: "Dual-use page routes
// (SPA + JSON via Accept) stay bare — do not invent /api/*".
//
// A wrong path here is worse than a missing one, and the failure is not a 404. The server's
// NoRoute handler is the beacon ingestion surface, so an operator following this document
// got "400 invalid beacon payload" — a C2 error for an operator request — or, on the bare
// spelling without an Accept header, the SPA's index.html. Nothing in the suite could see
// this because every handler test builds a gin context directly and never dispatches the
// advertised URL through the router, and the sibling guard only checked that the path appears
// in api/openapi.yaml: a fictional path documented as fiction passes that test.
//
// The walk is keyed on the JSON shape rather than an enumerated list of sections, so a
// capability block added later is covered without editing this test.
func TestAdvertisedEndpointPathsExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/exports/capabilities", nil)
	c.Set("user", "op")
	c.Set("user_role", "admin")
	c.Set("user_id", uint(1))
	s.handleExportCapabilities(c)
	if w.Code != http.StatusOK {
		t.Fatalf("capabilities handler returned %d: %s", w.Code, w.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}

	registered := map[string]bool{}
	byPath := map[string]bool{}
	for _, entry := range testingServerMethodRoutes(t) {
		method, path, ok := strings.Cut(entry, " ")
		if !ok {
			continue
		}
		// Report both spellings of a parameter so a document that names it differently
		// still matches: the router says ":id", the document may say "{agentId}".
		canonical := canonicalRoutePath(path)
		registered[strings.ToUpper(method)+" "+canonical] = true
		byPath[canonical] = true
	}
	if len(byPath) < 50 {
		t.Fatalf("only %d routes resolved; the guard is not looking at the router", len(byPath))
	}

	// The two hints the handlers build, outside the capabilities document, are the same class
	// of claim and are checked in the same pass.
	//  1. handleFailureSignatures answers with a "rerun" hint a client is told to POST.
	//  2. The bundle manifest names the endpoints a receiver can re-fetch a member from.
	//
	// Collected here as literals because reading them out of those handlers needs a second
	// fixture per handler; the point is that a path a human is told to type is verified by
	// this guard, and adding one to those files trips it.
	advertised := collectAdvertisedPaths(payload)
	extra := []struct {
		path   string
		method string
	}{
		{"/tasks/rerun-by-signature", "POST"},
	}
	for _, e := range extra {
		if !registered[e.method+" "+canonicalRoutePath(e.path)] {
			t.Errorf("handler-side claim %s %s is not registered", e.method, e.path)
		}
	}

	// Deduplication keys on BOTH spellings. Keying on the path alone, or comparing a
	// missing map entry to an empty method, silently skips every entry that states no
	// verb — which is most of them, because the exportCapabilities struct carries only
	// "path". That is exactly how the first version of this guard passed while the
	// document still advertised a path no router serves.
	type claim struct {
		canonical string
		method    string
	}
	checked := map[claim]bool{}
	var missing []string
	for path, method := range advertised {
		entry := claim{canonical: canonicalRoutePath(path), method: method}
		if checked[entry] {
			continue
		}
		checked[entry] = true
		if method == "" {
			if byPath[entry.canonical] {
				continue
			}
			missing = append(missing, "(any method) "+path)
			continue
		}
		if registered[method+" "+entry.canonical] {
			continue
		}
		if byPath[entry.canonical] {
			missing = append(missing, method+" "+path+"  [path exists, but not for "+method+"]")
			continue
		}
		missing = append(missing, method+" "+path)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("the capabilities document advertises paths the router does not serve:\n  %s\n"+
			"an operator following these gets the beacon NoRoute handler (400 invalid beacon payload)",
			strings.Join(missing, "\n  "))
	}
}

// collectAdvertisedPaths walks the decoded document and returns every path the server tells a
// client to call, mapped to the method stated beside it (empty when the block states none).
//
// Two keys are deliberately NOT treated as call targets:
//   - "verify_path": it is a sibling of "path" in the same object, so walking "path" already
//     covers the block; "verify_path" is matched too when it appears at the top level of an
//     entry object, which is how the bundle verify endpoint is published.
//   - "members": those are archive member file names, not URLs.
func collectAdvertisedPaths(node any) map[string]string {
	out := map[string]string{}
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if p, ok := v["path"].(string); ok && strings.HasPrefix(p, "/") {
				method, _ := v["method"].(string)
				out[p] = strings.ToUpper(method)
			}
			if p, ok := v["verify_path"].(string); ok && strings.HasPrefix(p, "/") {
				// Verified through the same registry: a bundle receiver is told to POST it.
				out[p] = "POST"
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(node)
	return out
}
