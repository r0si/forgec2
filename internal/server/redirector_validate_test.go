package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestRedirectorGeneratorsRejectInjectedDirectives pins the validation the three config
// generators now do. They write every field into nginx/apache/haproxy syntax verbatim, and the
// fields come from a JSON body, so `domain: "x;\n    server attacker.example;"` used to produce a
// config the operator would paste onto their own redirector with an extra directive inside it.
// Nothing here is reachable without an operator, but a tool that emits injectable output is still
// broken - and the same fields can arrive from a saved template or an AI tool call.
func TestRedirectorGeneratorsRejectInjectedDirectives(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	call := func(handler gin.HandlerFunc, body map[string]interface{}) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/infra/generate", bytes.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(3))
		c.Set(middleware.TenantIDContextKey, uint(1))
		handler(c)
		return w
	}

	cases := map[string]gin.HandlerFunc{
		"nginx":   s.handleGenerateNginx,
		"apache":  s.handleGenerateApache,
		"haproxy": s.handleGenerateHAProxy,
	}
	bad := []map[string]interface{}{
		{"domain": "c2.example.test;\n    server attacker.example;"},
		{"domain": "c2.example.test\n"},
		{"domain": ""},
		{"domain": "ok.example.test", "backend_url": "http://127.0.0.1:8000; include /etc/passwd"},
		{"domain": "ok.example.test", "blocked_ips": []string{"1.2.3.4;\n allow all"}},
		{"domain": "ok.example.test", "extc2_paths": []string{"/a; deny all"}},
		{"domain": "ok.example.test", "user_agent": "Mozilla\r\nX-Injected: 1"},
	}
	for name, handler := range cases {
		for _, body := range bad {
			w := call(handler, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s accepted an injectable config %#v: %d %.200s", name, body, w.Code, w.Body.String())
			}
		}
	}

	// A sane config still generates.
	w := call(s.handleGenerateNginx, map[string]interface{}{
		"domain":      "c2.example.test",
		"backend_url": "http://127.0.0.1:8000",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("valid config refused: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "c2.example.test") {
		t.Fatalf("the generated config lost the domain: %s", w.Body.String())
	}
}
