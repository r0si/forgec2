package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/api"
	"github.com/gin-gonic/gin"
)

// TestAPIDocsServeTheEmbeddedSpec pins the fix for a release-only failure: the docs endpoint
// resolved "api/openapi.yaml" relative to the working directory (or the executable's
// directory), and the packaged archives ship the binary, config and start script but NOT the
// api/ directory - so GET /api/docs/openapi.yaml answered 500 in every release build while
// working fine in a checkout.
func TestAPIDocsServeTheEmbeddedSpec(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/docs/openapi.yaml", nil)
	s.handleAPIDocsYAML(c)

	if w.Code != http.StatusOK {
		t.Fatalf("openapi.yaml = %d, want 200 (it must not depend on the working directory)", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-yaml" {
		t.Fatalf("content type = %q", ct)
	}
	body := w.Body.Bytes()
	if !bytes.Contains(body, []byte("openapi:")) {
		t.Fatalf("the served body does not look like an OpenAPI document: %.120s", body)
	}
	// Same bytes as the embedded source, so a stale copy cannot creep in.
	if !bytes.Equal(body, api.OpenAPISpec) {
		t.Fatal("the served document differs from api.OpenAPISpec")
	}

	// The UI page is independent of the spec file and must keep working.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/docs/", nil)
	s.handleAPIDocs(c)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("swagger-ui")) {
		t.Fatalf("docs page = %d, body %.120s", w.Code, w.Body.Bytes())
	}
}

// TestOpenAPISpecIsTheRepoFile keeps the embed honest: if the embedded copy is ever replaced
// by a stale snapshot, the spec used by the docs endpoint drifts from the one the type
// generator reads.
func TestOpenAPISpecIsTheRepoFile(t *testing.T) {
	if len(api.OpenAPISpec) < 10_000 {
		t.Fatalf("embedded spec is %d bytes; that is not the real document", len(api.OpenAPISpec))
	}
	if !bytes.Contains(api.OpenAPISpec, []byte("openapi:")) ||
		!bytes.Contains(api.OpenAPISpec, []byte("paths:")) {
		t.Fatal("embedded spec lacks the top-level openapi/paths keys")
	}
}
