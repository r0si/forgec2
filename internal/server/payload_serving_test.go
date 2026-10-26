package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func payloadCtx(id, filename string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/payloads/x/y", nil)
	c.Params = gin.Params{{Key: "id", Value: id}, {Key: "filename", Value: filename}}
	return c, w
}

// TestServePayloadPathTraversal pins the unauthenticated payload download: the
// path is built with a raw join from two URL segments, so containment is the
// only thing standing between a request and an arbitrary file read. The route
// is public, which makes this the highest-severity traversal surface in the
// server.
func TestServePayloadPathTraversal(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir

	// A real hosted payload.
	payloadDir := filepath.Join(dataDir, "payloads", "abc123")
	if err := os.MkdirAll(payloadDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, "beacon.exe"), []byte("MZ-PAYLOAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A secret one level above the payloads root.
	if err := os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte("jwt_secret: SUPER-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The legitimate download works.
	c, w := payloadCtx("abc123", "beacon.exe")
	s.handleServePayload(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "MZ-PAYLOAD") {
		t.Fatalf("legit payload = %d %.120s", w.Code, w.Body.String())
	}

	// Every traversal shape must be refused, and none may leak the secret.
	cases := [][2]string{
		{"..", "config.yaml"},
		{"abc123", "../../config.yaml"},
		{"abc123", "..\\..\\config.yaml"},
		{"....//", "config.yaml"},
		{".", "data/config.yaml"},
	}
	for _, tc := range cases {
		c, w = payloadCtx(tc[0], tc[1])
		s.handleServePayload(c)
		if strings.Contains(w.Body.String(), "SUPER-SECRET") {
			t.Fatalf("id=%q file=%q leaked a file outside the payloads root", tc[0], tc[1])
		}
		// The only acceptable bodies are the containment refusal ("forbidden"),
		// a 404 with no body, or a 400 with a message. Anything else means a
		// file was served. The status itself is not asserted because gin defers
		// flushing a bare c.Status() until the handler returns, so a recorder
		// reports the default 200 for an empty 404.
		if body := w.Body.String(); body != "forbidden" && body != "Invalid path" && body != "not found" {
			t.Fatalf("id=%q file=%q served a body: %.120q", tc[0], tc[1], body)
		}
	}

	// A missing payload is a 404, not a 500.
	c, w = payloadCtx("nope", "beacon.exe")
	s.handleServePayload(c)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("missing payload = %d %.80q", w.Code, w.Body.String())
	}

	// Empty segments are rejected.
	c, w = payloadCtx("", "")
	s.handleServePayload(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty params = %d", w.Code)
	}
}
