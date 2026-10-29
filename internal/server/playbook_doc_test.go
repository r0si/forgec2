package server

import (
	"os"
	"strings"
	"testing"
)

// TestPlaybookDocListsTheEndpoints keeps docs/PLAYBOOKS.md true to the routes.
//
// The page described the pure state machine, which was right while that was the whole feature. It then grew storage, transitions, a history
// endpoint and an attention filter, and a page that documents only the state machine leaves an operator looking for endpoints that exist.
// The check is one-directional on purpose: the page may describe things the router does not register (the "what is absent" section), but
// every playbook route the router registers must be mentioned.
func TestPlaybookDocListsTheEndpoints(t *testing.T) {
	raw, err := os.ReadFile("../../docs/PLAYBOOKS.md")
	if err != nil {
		t.Fatalf("the playbook page is missing: %v", err)
	}
	doc := string(raw)

	routes := []string{
		"GET /api/playbooks/runs",
		"GET /api/playbooks/runs/{id}",
		"POST /api/playbooks/runs",
		"POST /api/playbooks/runs/{id}/transitions",
		"GET /api/playbooks/runs/{id}/history",
	}
	for _, route := range routes {
		if !strings.Contains(doc, route) {
			t.Fatalf("the page does not document %q", route)
		}
	}

	// The parts that are easy to leave out, and that a reader most needs: what "blocked" distinguishes, that a refusal is a 409, and that
	// nothing here dispatches.
	for _, needle := range []string{"blocked_steps", "next_step", "attention=1", "409", "Nothing here dispatches anything"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the page does not state %q", needle)
		}
	}

	// And the state-machine half must survive: the pure rules are what the endpoints are built on.
	for _, needle := range []string{"Only `succeeded` unlocks a dependent", "Normalise"} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("the page lost the state-machine rules: %q", needle)
		}
	}
}
