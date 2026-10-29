package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBuildStatusCarriesTheFailureHint covers what a console actually reads.
//
// The status JSON is the contract: `error` is the human diagnosis and `error_hint` is the machine-readable next step. A client that keyed
// an action on the message text would break on the next compiler version, which is the whole reason the code exists.
func TestBuildStatusCarriesTheFailureHint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	job := s.startBuildJob("windows", "exe", "https://c2.example", 0, "agent.exe")
	if job == nil {
		t.Fatal("no build job was created")
	}
	// Fail it the way the runner does, through completeBuildJob, so the test covers the wiring rather than a hand-built job.
	s.completeBuildJob(job, "", errTestCompileFailure{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/generate/builds/"+job.ID, nil)
	c.Params = gin.Params{{Key: "id", Value: job.ID}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Hint   struct {
				Code string `json:"code"`
				Text string `json:"text"`
			} `json:"error_hint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Data.Status != "failed" {
		t.Fatalf("status = %q", resp.Data.Status)
	}
	// The diagnosis is the compiler's, not a fallback sentence.
	if resp.Data.Error == "" || resp.Data.Error == "Build failed" {
		t.Fatalf("the error text collapsed again: %q", resp.Data.Error)
	}
	// And the next step is a code, not something to be regexed out of English.
	if resp.Data.Hint.Code != HintCompileError {
		t.Fatalf("hint code = %q, want %q", resp.Data.Hint.Code, HintCompileError)
	}
	if resp.Data.Hint.Text == "" {
		t.Fatal("the hint carries no sentence")
	}
}

// errTestCompileFailure is a stand-in for the toolchain's own output.
type errTestCompileFailure struct{}

func (errTestCompileFailure) Error() string {
	return "go build failed: exit status 1\n./agent.go:42:2: undefined: ResolveNtSyscalls"
}
