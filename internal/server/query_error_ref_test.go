package server

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestQueryErrorCarriesAReference makes an operator report usable.
//
// handleQueryError backs 74 call sites and used to log the error while answering the literal "Query
// failed" - so "I got Query failed" identified neither the endpoint nor the log line. Each failure now
// carries a 6-character reference in X-Error-Ref and the same reference is in the log, together with
// the route.
func TestQueryErrorCarriesAReference(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	_ = s

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	// FullPath() is empty unless a route was matched; the fallback covers both.
	handleQueryError(c, errors.New("SQL logic error: no such column: tenant_id"), "Failed to list tasks")

	ref := w.Header().Get("X-Error-Ref")
	if len(ref) != 6 {
		t.Fatalf("X-Error-Ref = %q, want 6 hex characters", ref)
	}
	if strings.ContainsAny(ref, "ghijklmnopqrstuvwxyz") {
		t.Fatalf("reference is not hex: %q", ref)
	}

	// The body stays a literal, so nothing internal leaks and the envelope gate is unaffected.
	if !strings.Contains(w.Body.String(), "Query failed") {
		t.Fatalf("body = %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "tenant_id") {
		t.Fatalf("the raw error leaked into the body: %q", w.Body.String())
	}

	// The log carries the SAME reference plus the route, which is what makes it findable.
	out := logs.String()
	if !strings.Contains(out, ref) {
		t.Fatalf("the log line does not carry the reference %q: %q", ref, out)
	}
	if !strings.Contains(out, "/api/tasks") {
		t.Fatalf("the log line does not identify the route: %q", out)
	}
	if !strings.Contains(out, "no such column") {
		t.Fatalf("the log line lost the cause: %q", out)
	}

	// Two failures get different references, so a report is unambiguous.
	first := ref
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	handleQueryError(c2, errors.New("busy"), "Failed to list tasks")
	if w2.Header().Get("X-Error-Ref") == first {
		t.Fatal("two failures shared a reference")
	}
}
