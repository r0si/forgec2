package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestIsSafeLocalRedirect pins the guard on the unauthenticated /lang/set bounce.
//
// The old check was "starts with / and not with //", which a backslash walks around:
// browsers normalise "\" to "/" in a Location, so "/\evil.com" is requested as
// "//evil.com" and lands on another host - an open redirect served from the console origin,
// which is exactly the shape phishing wants.
func TestIsSafeLocalRedirect(t *testing.T) {
	accept := []string{
		"/",
		"/agents",
		"/agents?x=1",
		"/agents#top",
		"/settings/notifications",
	}
	for _, ref := range accept {
		if !isSafeLocalRedirect(ref) {
			t.Errorf("isSafeLocalRedirect(%q) rejected a local path", ref)
		}
	}

	reject := []string{
		"",
		"//evil.com",
		"//evil.com/path",
		"/\\evil.com",          // the backslash bypass
		"/\\/evil.com",         // and its doubled form
		"http://evil.com",      // absolute
		"https://evil.com/",    // absolute
		"/path\r\nSet-Cookie:", // header splitting
		"/path\nX-Injected: 1",
		"/path\twith-tab",
		"javascript:alert(1)", // not a path at all
	}
	for _, ref := range reject {
		if isSafeLocalRedirect(ref) {
			t.Errorf("isSafeLocalRedirect(%q) accepted a non-local target", ref)
		}
	}
}

// TestHandleSetLanguageRefusesTheBackslashBypass drives the real handler through a router,
// because gin only settles the 302 on a writer the engine flushes.
func TestHandleSetLanguageRefusesTheBackslashBypass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	r := gin.New()
	r.POST("/lang/set", s.handleSetLanguage)

	post := func(referer string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/lang/set?lang=zh", nil)
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		r.ServeHTTP(w, req)
		return w
	}

	// The backslash bypass: previously accepted, and a browser would have followed it off-site.
	if w := post("/\\evil.com"); w.Code == http.StatusFound {
		t.Fatalf("a backslash Referer produced a redirect to %q", w.Header().Get("Location"))
	}

	// A local path is still followed, so the guard did not break the feature.
	w := post("/agents")
	if w.Code != http.StatusFound {
		t.Fatalf("a local Referer was not followed: %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/agents" {
		t.Fatalf("Location = %q, want /agents", loc)
	}
}
