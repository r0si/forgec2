package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBuildHealthSnapshot covers the readiness answer "have builds been working".
//
// Round 188 added "can this server build"; this is the other half. A deployment where every build fails is broken in the way an operator
// discovers only when they need a payload, and /ready answered ok through all of it. The assertions pin the threshold in both directions:
// three failures and no success warns, two do not, and a mixed window reports the ratio without warning.
func TestBuildHealthSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	s.resetBuildHealthCache()

	seed := func(status string, at time.Time) {
		if err := s.db.Create(&db.BuildLog{Platform: "windows", Format: "exe", Status: status, CreatedAt: at}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// 1. No builds at all is a sentence, not an empty check.
	if health := s.buildHealth(); !health.Scanned || !strings.Contains(health.Text, "no builds") || health.Warn {
		t.Fatalf("empty history = %+v", health)
	}

	// 2. Two failures are not evidence: a typo is not a broken toolchain.
	s.resetBuildHealthCache()
	seed("failed", time.Now().Add(-time.Hour))
	seed("failed", time.Now().Add(-2*time.Hour))
	if health := s.buildHealth(); health.Warn || !strings.Contains(health.Text, "2 of 2") {
		t.Fatalf("two failures = %+v", health)
	}

	// 3. Three failures and no success is unambiguous, and says so.
	s.resetBuildHealthCache()
	seed("failed", time.Now().Add(-3*time.Hour))
	health := s.buildHealth()
	if !health.Warn {
		t.Fatalf("three failures and no success did not warn: %+v", health)
	}
	if !strings.Contains(health.Text, "every one of the last 3") {
		t.Fatalf("text = %q", health.Text)
	}

	// 4. One success in the window makes it a ratio rather than a warning: somebody can build, so the toolchain works.
	s.resetBuildHealthCache()
	seed("success", time.Now().Add(-30*time.Minute))
	if health := s.buildHealth(); health.Warn || !strings.Contains(health.Text, "3 of 4") {
		t.Fatalf("mixed window = %+v", health)
	}

	// 5. Old failures fall out of the window.
	s.resetBuildHealthCache()
	if err := s.db.Where("1 = 1").Delete(&db.BuildLog{}).Error; err != nil {
		t.Fatalf("clear: %v", err)
	}
	seed("failed", time.Now().Add(-48*time.Hour))
	if health := s.buildHealth(); health.Warn || !strings.Contains(health.Text, "no builds") {
		t.Fatalf("out-of-window failure = %+v", health)
	}

	// 6. And the warning reaches /ready only through the unambiguous case.
	s.resetBuildHealthCache()
	if err := s.db.Where("1 = 1").Delete(&db.BuildLog{}).Error; err != nil {
		t.Fatalf("clear: %v", err)
	}
	readReady := func() (map[string]interface{}, []string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/ready", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleReadyCheck(c)
		var resp struct {
			Checks   map[string]interface{} `json:"checks"`
			Warnings []string               `json:"warnings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode ready: %v", err)
		}
		return resp.Checks, resp.Warnings
	}
	checks, warnings := readReady()
	if _, present := checks["build_history"]; !present {
		t.Fatalf("checks = %v, want build_history", checks)
	}
	for _, warning := range warnings {
		if strings.Contains(warning, "payload builds are failing consistently") {
			t.Fatalf("a clean history produced the failure warning: %v", warnings)
		}
	}

	s.resetBuildHealthCache()
	for index := 0; index < 3; index++ {
		seed("failed", time.Now().Add(-time.Duration(index+1)*time.Minute))
	}
	_, warnings = readReady()
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "payload builds are failing consistently") {
			found = true
		}
	}
	if !found {
		t.Fatalf("three failed builds produced no ready warning: %v", warnings)
	}
}
