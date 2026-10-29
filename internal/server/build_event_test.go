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

// TestBuildOutcomeEvent covers the payload a rule writes conditions against.
//
// The event is built by a pure function on purpose: the bus in a test harness has no running reader, so asserting delivery would be
// asserting the harness. What a rule actually depends on - the type, the fields a condition may reference, and the HINT CODE - is
// verifiable directly, and the emit stays a single call.
func TestBuildOutcomeEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	job := s.startBuildJob("windows", "exe", "https://c2.example", 0, "agent.exe")
	if job == nil {
		t.Fatal("no build job was created")
	}
	s.completeBuildJob(job, "", errTestCompileFailure{})

	failed := buildOutcomeEvent(job, errTestCompileFailure{}, "windows", "exe", "agent.exe")
	if failed.Type != EventBuildFailed {
		t.Fatalf("type = %q, want %q", failed.Type, EventBuildFailed)
	}
	if failed.Data["build_id"] != job.ID || failed.Data["platform"] != "windows" || failed.Data["format"] != "exe" {
		t.Fatalf("event data = %v", failed.Data)
	}
	// The cause, not the message: a rule should route on this code, because the message changes with every toolchain release.
	if failed.Data["hint_code"] != HintCompileError {
		t.Fatalf("hint_code = %v, want %q", failed.Data["hint_code"], HintCompileError)
	}
	if errText, _ := failed.Data["error"].(string); errText == "" {
		t.Fatal("the event carries no diagnosis")
	}
	// Tenant attribution: a rule must not fire across tenants.
	if failed.TenantID != job.TenantID {
		t.Fatalf("tenant = %d, want %d", failed.TenantID, job.TenantID)
	}

	// A successful build is a different type and carries no error field, so a rule can watch for recovery.
	okJob := s.startBuildJob("linux", "elf", "https://c2.example", 0, "agent.elf")
	s.completeBuildJob(okJob, "/tmp/agent.elf", nil)
	succeeded := buildOutcomeEvent(okJob, nil, "linux", "elf", "agent.elf")
	if succeeded.Type != EventBuildSucceeded {
		t.Fatalf("type = %q, want %q", succeeded.Type, EventBuildSucceeded)
	}
	if _, hasError := succeeded.Data["error"]; hasError {
		t.Fatalf("a successful build carried an error field: %v", succeeded.Data)
	}
	if _, hasHint := succeeded.Data["hint_code"]; hasHint {
		t.Fatalf("a successful build carried a hint: %v", succeeded.Data)
	}

	// And the operator-facing status agrees with the event about the code, so the two views cannot drift.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/generate/builds/"+job.ID, nil)
	c.Params = gin.Params{{Key: "id", Value: job.ID}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildStatus(c)
	var resp struct {
		Data struct {
			Hint struct {
				Code string `json:"code"`
			} `json:"error_hint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Hint.Code != failed.Data["hint_code"] {
		t.Fatalf("status view says %q, event says %v", resp.Data.Hint.Code, failed.Data["hint_code"])
	}
}
