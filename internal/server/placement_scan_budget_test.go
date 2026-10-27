package server

import (
	"encoding/base64"
	"testing"

	"github.com/forgec2/forgec2/internal/malleable"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// placementsCtx builds a GET request whose query carries exactly the values given, in
// order, with the placement scan configured to look at query values with one step.
func placementsCtx(t *testing.T, s *Server, values []string) *gin.Context {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	q := url.Values{}
	for i, v := range values {
		q.Set("p"+itoaPlacement(i), v)
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/beacon?"+q.Encode(), nil)
	// Configure a single query placement with a trivial chain so tryDecode only runs
	// base64 on whatever it is handed.
	s.cfg.Malleable.Placements = `[{"target":"query:p0","chain":"base64"}]`
	return c
}

func itoaPlacement(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestExtractPlacedBodyBoundsTheScan pins the fix for an unbounded amount of work on the
// unauthenticated beacon endpoint: the handler used to decode EVERY query value and EVERY
// cookie with the profile's transform chain, so a request carrying thousands of parameters
// bought thousands of base64/XOR passes before any beacon processing happened.
func TestExtractPlacedBodyBoundsTheScan(t *testing.T) {
	s := mustTenantServer(t)
	payload := base64.StdEncoding.EncodeToString([]byte(`{"k":"v"}`))

	// The normal case: one placed value, found immediately.
	c := placementsCtx(t, s, []string{payload})
	if got := s.extractPlacedBody(c); got == nil {
		t.Fatal("a single placed value was not found")
	}

	// A hostile request: the valid value hidden behind many junk parameters. The scan is
	// bounded, so it must NOT do the work for all of them - and it reports no body rather
	// than spending unbounded time.
	junk := strings.Repeat("A", 16)
	many := make([]string, 0, 4000)
	for i := 0; i < 4000; i++ {
		many = append(many, junk)
	}
	many = append(many, payload)
	c = placementsCtx(t, s, many)
	// Whatever the outcome, the call must return promptly; the meaningful assertion is
	// that the budget counter stopped the scan before the last parameter.
	if got := s.extractPlacedBody(c); got != nil {
		t.Fatalf("the scan walked past its budget and decoded the tail of a %d-parameter request", len(many))
	}
}

// TestPlacementScanBudgetConstants keeps the bounds small enough to matter and large
// enough for a real beacon (which carries one value of at most a few KiB).
func TestPlacementScanBudgetConstants(t *testing.T) {
	if maxPlacementCandidates < 8 || maxPlacementCandidates > 256 {
		t.Fatalf("candidate cap = %d, out of a sane range", maxPlacementCandidates)
	}
	if maxPlacementValueBytes < 4<<10 || maxPlacementValueBytes > 64<<10 {
		t.Fatalf("per-value cap = %d, out of a sane range", maxPlacementValueBytes)
	}
	if maxPlacementDecodeBudget < maxPlacementValueBytes || maxPlacementDecodeBudget > 2<<20 {
		t.Fatalf("total budget = %d, out of a sane range", maxPlacementDecodeBudget)
	}
	if !malleableHasBase64Step("base64") {
		t.Fatal("the test chain is not the one the transform understands")
	}
}

// malleableHasBase64Step documents that the test's chain string is meaningful: the
// transform block accepts "base64", so tryDecode really exercises a decode.
func malleableHasBase64Step(chain string) bool {
	tb := &malleable.TransformBlock{Transforms: []malleable.Transform{{Type: chain}}}
	// Encode, not decode: decoding arbitrary text is expected to fail, so the check must
	// exercise the direction the test actually uses.
	out, err := tb.Apply([]byte("x"), true)
	return err == nil && len(out) > 0 && string(out) == "eA=="
}

// TestExtractPlacedBodySkipsPathologicalQuery pins the second half of the bound: the
// per-value budget cannot help if url.ParseQuery has to allocate a parameter per byte
// first, so an over-long query string is skipped unparsed. A real beacon sends a short
// query, and the caller falls back to the request body either way.
func TestExtractPlacedBodySkipsPathologicalQuery(t *testing.T) {
	s := mustTenantServer(t)
	payload := base64.StdEncoding.EncodeToString([]byte(`{"k":"v"}`))

	// Build a query string far over the cap, with the valid value at the very start: if
	// the gate works, it is not even looked at.
	q := "p0=" + payload + "&" + strings.Repeat("junk=AAAAAAAAAAAAAAAA&", maxPlacementQueryBytes/8)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/beacon?"+q, nil)
	s.cfg.Malleable.Placements = `[{"target":"query:p0","chain":"base64"}]`

	if got := s.extractPlacedBody(c); got != nil {
		t.Fatalf("an over-long query string was parsed and decoded (%d bytes of query)", len(q))
	}

	// The same value in a normal-sized query is still found.
	small := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(small)
	c2.Request = httptest.NewRequest(http.MethodGet, "/beacon?p0="+url.QueryEscape(payload), nil)
	if got := s.extractPlacedBody(c2); got == nil {
		t.Fatal("a normal query string stopped resolving the placed value")
	}
}
