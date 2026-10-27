package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func buildCtx(t *testing.T, buildID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/builds/"+buildID, nil)
	c.Params = gin.Params{{Key: "id", Value: buildID}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestBuildJobVisibilityIsTenantScoped pins the cross-tenant read fix: the status
// and download endpoints looked jobs up by id alone, so an id that leaked into a
// log or a screenshot let another tenant fetch the binary - which embeds the
// listener URL and the malleable profile.
func TestBuildJobVisibilityIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	// The caller needs a user row for currentTenantID to resolve to 1.
	seedAIUser(t, s, "admin1", 1)

	job := s.startBuildJobWithProfile("windows", "exe", "https://c2.example", 7, "agent.exe", "prof", "hash", 2)
	s.completeBuildJob(job, filepath.Join(t.TempDir(), "agent.exe"), nil)

	// A tenant-1 operator must not see (or download) a tenant-2 build.
	c, w := buildCtx(t, job.ID)
	s.handleBuildStatus(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status of a foreign build = %d (%s)", w.Code, w.Body.String())
	}
	if body := w.Body.String(); containsAny(body, "c2.example", "prof") {
		t.Fatalf("the status response leaked foreign build details: %s", body)
	}

	c, w = buildCtx(t, job.ID)
	s.handleBuildDownload(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("download of a foreign build = %d", w.Code)
	}

	// The owning tenant still sees it.
	s.db.Model(&db.User{}).Where("username = ?", "admin1").Update("tenant_id", 2)
	c, w = buildCtx(t, job.ID)
	s.handleBuildStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("the owning tenant was refused: %d (%s)", w.Code, w.Body.String())
	}
}

// TestBuildJobVisibleToUnscopedAndLegacy covers the two permissive cases so they
// cannot rot: an unscoped caller (tid 0, single-tenant install) and a job that
// carries no tenant (created before the field existed).
func TestBuildJobVisibleToUnscopedAndLegacy(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)

	c, _ := buildCtx(t, "x")
	if !s.buildJobVisibleTo(c, &BuildJob{ID: "legacy"}) {
		t.Fatal("a job without a tenant became invisible")
	}
	if s.buildJobVisibleTo(c, &BuildJob{ID: "foreign", TenantID: 2}) {
		t.Fatal("a foreign job was visible")
	}
	if s.buildJobVisibleTo(c, nil) {
		t.Fatal("a nil job was visible")
	}
}

// TestBuildDownloadRefusesQueuedJob stays green with the visibility check: a job
// that has not finished is still refused for its owner.
func TestBuildDownloadRefusesQueuedJob(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)

	job := s.startBuildJobWithProfile("windows", "exe", "https://c2.example", 1, "a.exe", "", "", 1)
	if err := os.WriteFile(filepath.Join(t.TempDir(), "unused"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, w := buildCtx(t, job.ID)
	s.handleBuildDownload(c)
	if w.Code == http.StatusOK {
		t.Fatalf("a queued build was downloadable: %d", w.Code)
	}
	_ = time.Now()
}
