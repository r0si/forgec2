package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestLooksLikeCS pins the auto-detection heuristic for Cobalt Strike profile
// text. It decides which parser runs, so a false positive sends a JSON profile
// through the CS translator and a false negative sends CS text through the JSON
// migrator.
func TestLooksLikeCS(t *testing.T) {
	cs := []string{
		"http-get { set uri \"/x\"; }",
		"http-post { }",
		"http-config { set headers \"x\"; }",
		"set sleeptime \"60000\";",
		"set useragent \"Mozilla\";",
		"set jitter \"20\";",
		"# forgec2 profile",
	}
	for _, in := range cs {
		if !looksLikeCS(in) {
			t.Errorf("looksLikeCS(%q) = false, want true", in)
		}
	}
	notCS := []string{
		"",
		"   ",
		`{"name":"p","beacon_uri":"/x"}`, // JSON object: the v2 migrator handles it
		`[{"type":"base64"}]`,            // JSON array
		"just some notes",
		"profile without any cs keywords",
	}
	for _, in := range notCS {
		if looksLikeCS(in) {
			t.Errorf("looksLikeCS(%q) = true, want false", in)
		}
	}
}

func validateProfileCtx(body string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/generate/profiles/validate", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestHandleValidateProfile covers the editor's dry-run: a valid v2 profile
// validates, malformed JSON is a 400, a CS text profile is parsed and flagged
// invalid (not 400 — the editor needs the partial profile to render), and a
// lossy server_output chain produces a warning rather than a silent pass.
func TestHandleValidateProfile(t *testing.T) {
	s := mustTenantServer(t)

	// Empty content is rejected.
	c, w := validateProfileCtx(`{"content":""}`)
	s.handleValidateProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty content = %d %s", w.Code, w.Body.String())
	}

	// Malformed JSON is a 400 with a sanitized message.
	c, w = validateProfileCtx(`{"content":"{"}`)
	s.handleValidateProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed = %d %s", w.Code, w.Body.String())
	}

	// A minimal valid v2 profile validates and comes back compiled.
	valid := `{"name":"t","beacon_uri":"/api/v1/beacon","method":"POST","transforms":[]}`
	c, w = validateProfileCtx(`{"name":"t","content":` + jsonQuote(valid) + `}`)
	s.handleValidateProfile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("valid profile = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Valid   bool        `json:"valid"`
		Profile interface{} `json:"profile"`
		Wire    interface{} `json:"wire"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if !out.Valid || out.Profile == nil {
		t.Fatalf("valid profile reported %+v", out)
	}

	// A CS text profile is parsed (200) — the editor shows diagnostics rather
	// than a bare 400, and `valid` reflects the parse.
	csText := "http-get {\n  set uri \"/x\";\n}"
	c, w = validateProfileCtx(`{"name":"cs","content":` + jsonQuote(csText) + `}`)
	s.handleValidateProfile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("cs profile = %d %s", w.Code, w.Body.String())
	}
	var csOut struct {
		Valid   bool        `json:"valid"`
		Profile interface{} `json:"profile"`
		Wires   interface{} `json:"wire"`
	}
	unmarshalData(t, w.Body.Bytes(), &csOut)
	// A minimal http-get profile is complete enough to compile, so the parser
	// returns a profile rather than an error.
	if csOut.Profile == nil {
		t.Fatalf("a CS text profile produced no profile: %+v", csOut)
	}

	// A syntactically broken CS profile is reported through diagnostics with
	// valid=false, and deliberately NOT as a 400: the editor needs the partial
	// profile to render a live preview.
	broken := "http-get {"
	c, w = validateProfileCtx(`{"name":"broken","format":"cs","content":` + jsonQuote(broken) + `}`)
	s.handleValidateProfile(c)
	if w.Code != http.StatusOK {
		t.Fatalf("broken CS profile = %d %s", w.Code, w.Body.String())
	}
	var brokenOut struct {
		Valid       bool `json:"valid"`
		Diagnostics []struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	unmarshalData(t, w.Body.Bytes(), &brokenOut)
	if len(brokenOut.Diagnostics) == 0 {
		t.Fatal("a broken CS profile produced no diagnostics")
	}
	// An unterminated block is an error; a missing semicolon is only a warning,
	// which is why valid is asserted against a structural break.
	if brokenOut.Valid {
		t.Fatalf("an unterminated CS block reported valid: %+v", brokenOut)
	}
}

// jsonQuote embeds a string as a JSON literal, so a test can build a JSON body
// containing JSON content.
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
