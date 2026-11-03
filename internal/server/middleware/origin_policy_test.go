package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOriginAllowedPolicy(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		host    string
		allowed []string
		want    bool
	}{
		{"no origin", "", "203.0.113.10:8000", nil, true},
		{"public same origin", "http://203.0.113.10:8000", "203.0.113.10:8000", nil, true},
		{"public wrong port", "http://203.0.113.10:9000", "203.0.113.10:8000", nil, false},
		{"local dev cross port", "http://localhost:5173", "127.0.0.1:8000", nil, true},
		{"local page to public host", "http://localhost:5173", "203.0.113.10:8000", nil, false},
		{"configured hostname", "https://console.example:8443", "api.internal:8000", []string{"Console.Example"}, true},
		{"configured hostname padded", "https://console.example:8443", "api.internal:8000", []string{"  console.example  "}, true},
		{"configured full origin", "https://console.example", "api.internal:8000", []string{"https://console.example"}, true},
		{"full origin rejects scheme downgrade", "http://console.example", "api.internal:8000", []string{"https://console.example"}, false},
		{"full origin rejects other port", "https://console.example:8443", "api.internal:8000", []string{"https://console.example"}, false},
		{"invalid port", "http://localhost:99999", "127.0.0.1:8000", nil, false},
		{"empty port", "http://localhost:", "127.0.0.1:8000", nil, false},
		{"missing hostname", "http://:8000", ":8000", nil, false},
		{"bare port config is not a hostname wildcard", "https://console.example:9000", "api.internal", []string{"console.example:8443"}, false},
		{"hostname suffix", "https://console.example.evil.test", "api.internal:8000", []string{"console.example"}, false},
		{"origin path", "https://console.example/path", "api.internal:8000", []string{"console.example"}, false},
		{"origin credentials", "https://user@console.example", "api.internal:8000", []string{"console.example"}, false},
		{"malformed", "http://[::1", "[::1]:8000", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OriginAllowed(tt.origin, tt.host, tt.allowed); got != tt.want {
				t.Fatalf("OriginAllowed(%q, %q, %v) = %t, want %t", tt.origin, tt.host, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestCORSSameOriginPublicConsole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS(nil))
	r.OPTIONS("/api/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "http://203.0.113.10:8000/api/test", nil)
	req.Host = "203.0.113.10:8000"
	req.Header.Set("Origin", "http://203.0.113.10:8000")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://203.0.113.10:8000" {
		t.Fatalf("allow origin = %q", got)
	}
}

func TestCORSWithOriginsReadsHotReloadedPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	allowed := []string{"first.example"}
	r := gin.New()
	r.Use(CORSWithOrigins(func() []string { return allowed }))
	r.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func(origin string) string {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://api.internal/api/test", nil)
		req.Host = "api.internal"
		req.Header.Set("Origin", origin)
		r.ServeHTTP(w, req)
		return w.Header().Get("Access-Control-Allow-Origin")
	}

	if got := request("https://first.example"); got != "https://first.example" {
		t.Fatalf("initial policy did not allow first origin: %q", got)
	}
	allowed = []string{"second.example"}
	if got := request("https://first.example"); got != "" {
		t.Fatalf("stale first origin remained allowed after reload: %q", got)
	}
	if got := request("https://second.example"); got != "https://second.example" {
		t.Fatalf("reloaded policy did not allow second origin: %q", got)
	}
}

func TestCORSVariesAllOriginResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS([]string{"https://console.example"}))
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, origin := range []string{"", "https://console.example", "https://untrusted.example"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://api.internal/test", nil)
		req.Header.Set("Origin", origin)
		r.ServeHTTP(w, req)
		if got := w.Header().Get("Vary"); got != "Origin" {
			t.Fatalf("origin %q: Vary = %q, want Origin", origin, got)
		}
		if origin != "https://console.example" && w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("origin %q unexpectedly received CORS approval", origin)
		}
	}
}
