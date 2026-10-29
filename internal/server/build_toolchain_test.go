package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBuildToolchainPreflight covers the endpoint an operator calls before waiting for a compile.
//
// The assertions are about SHAPE and INTERNAL CONSISTENCY rather than about this machine: `go` is present on a build box and may be
// absent in a container, and a test that demanded either would be testing the CI image. What must hold everywhere: the three probes are
// reported, `problems` explains every failure, and `ok` is exactly "no problems".
func TestBuildToolchainPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/generate/toolchain", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildToolchain(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			OK     bool                   `json:"ok"`
			Go     map[string]interface{} `json:"go"`
			Cache  map[string]interface{} `json:"gocache"`
			Garble map[string]interface{} `json:"garble"`
			Agents map[string]interface{} `json:"agents_dir"`
			Probs  []string               `json:"problems"`
			Note   string                 `json:"note"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// 1. Every probe is present, because a missing probe reads as "fine" to a caller that checks one field.
	if resp.Data.Go == nil || resp.Data.Cache == nil || resp.Data.Garble == nil || resp.Data.Agents == nil {
		t.Fatalf("a probe is missing: %+v", resp.Data)
	}
	if _, ok := resp.Data.Go["available"]; !ok {
		t.Fatalf("the go probe carries no availability: %+v", resp.Data.Go)
	}
	if _, ok := resp.Data.Cache["writable"]; !ok {
		t.Fatalf("the cache probe carries no writability: %+v", resp.Data.Cache)
	}

	// 2. ok is exactly "no problems", in both directions: a report that says ok while listing a problem is the contradiction this
	// endpoint exists to avoid.
	if resp.Data.OK != (len(resp.Data.Probs) == 0) {
		t.Fatalf("ok=%v with problems=%v", resp.Data.OK, resp.Data.Probs)
	}
	for _, problem := range resp.Data.Probs {
		if len(problem) < 10 {
			t.Fatalf("a problem is too terse to act on: %q", problem)
		}
	}

	// 3. The note states the check runs as the server's own user - the whole reason a shell check is not a substitute.
	// Lowercased explicitly: the note capitalises SERVER, and a case-sensitive check here would fail on its own wording.
	if resp.Data.Note == "" || !strings.Contains(strings.ToLower(resp.Data.Note), "server") {
		t.Fatalf("the note does not explain whose environment was checked: %q", resp.Data.Note)
	}

	// 4. The probe is cached: /ready polls every few seconds and `go version` is an exec, so the second call must reuse the snapshot.
	first, firstProbed := s.toolchainProblems()
	second, secondProbed := s.toolchainProblems()
	if firstProbed != secondProbed || len(first) != len(second) {
		t.Fatalf("the cached probe changed between calls: %v then %v", first, second)
	}
	toolchainReportMu.Lock()
	cachedAt := toolchainReportAt
	toolchainReportMu.Unlock()
	if cachedAt.IsZero() {
		t.Fatal("the probe result was not cached, so /ready would exec on every poll")
	}
	// The check is informational: a process-environment answer must not become a /ready warning that fires on a transient state.
	if _, probed := s.toolchainProblems(); !probed {
		t.Fatal("a probed snapshot reported itself as not probed")
	}

	// 5. When go IS available here, its version is reported: a bare path without a version cannot be compared against go.mod.
	if available, _ := resp.Data.Go["available"].(bool); available {
		if _, ok := resp.Data.Go["version"]; !ok {
			if _, failed := resp.Data.Go["error"]; !failed {
				t.Fatalf("go is available but neither its version nor a reason is reported: %+v", resp.Data.Go)
			}
		}
	}
}
