package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestEveryServerErrorCarriesAReference extends the reference from handleQueryError to every 5xx.
//
// Before this, only the query helper produced one: any other 500 (a sanitized plugin failure, a
// listener that could not bind, a WebRTC setup error) answered a message with nothing an operator could
// quote and no guaranteed log line tying the response to a route.
func TestEveryServerErrorCarriesAReference(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	newCtx := func(method, target string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, nil)
		return c, w
	}

	// A 5xx gains a reference and produces exactly one log line with the route.
	c, w := newCtx(http.MethodPost, "/api/listeners")
	respondError(c, http.StatusInternalServerError, "failed to start listener")
	ref := w.Header().Get("X-Error-Ref")
	if len(ref) != 6 {
		t.Fatalf("X-Error-Ref = %q", ref)
	}
	out := logs.String()
	if !strings.Contains(out, ref) || !strings.Contains(out, "/api/listeners") {
		t.Fatalf("the 500 was not logged with its reference and route: %q", out)
	}
	if !strings.Contains(out, "failed to start listener") {
		t.Fatalf("the log line lost the message: %q", out)
	}

	// A 4xx is a client mistake: no reference, no error log.
	logs.Reset()
	c2, w2 := newCtx(http.MethodPost, "/api/listeners")
	respondError(c2, http.StatusBadRequest, "name required")
	if w2.Header().Get("X-Error-Ref") != "" {
		t.Fatal("a 400 gained a reference")
	}
	if strings.Contains(logs.String(), "request failed") {
		t.Fatalf("a 400 was logged as a server error: %q", logs.String())
	}

	// An existing reference (set by handleQueryError, which also logs the cause) is reused, not
	// replaced: the operator must see the same id that the cause was logged under.
	c3, w3 := newCtx(http.MethodGet, "/api/tasks")
	c3.Header("X-Error-Ref", "abc123")
	respondError(c3, http.StatusInternalServerError, "Query failed")
	if got := w3.Header().Get("X-Error-Ref"); got != "abc123" {
		t.Fatalf("the existing reference was replaced: %q", got)
	}
	// ...and the helper's own line still names the route so it is findable even without the cause log.
	if !strings.Contains(logs.String(), "abc123") {
		t.Fatalf("the reused reference was not logged: %q", logs.String())
	}
}
