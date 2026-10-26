package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func screenshotCtx(agentID, filename string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/screenshots/x/y", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Params = gin.Params{{Key: "agent_id", Value: agentID}, {Key: "filename", Value: filename}}
	return c, w
}

// TestServeScreenshotPathTraversal pins the file-serving guards: the extension
// allowlist, the path containment check and the tenant gate all have to hold for
// anything to be served.
func TestServeScreenshotPathTraversal(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	seedAIUser(t, s, "admin1", 1)

	// A real agent with a legitimate screenshot, plus a secret outside the
	// screenshot root that no request may reach.
	if err := s.db.Create(&db.Implant{ID: "shot-agent", TenantID: 1, Hostname: "shot", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	shotDir := filepath.Join(dataDir, "screenshots", "shot-agent")
	if err := os.MkdirAll(shotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shotDir, "cap.png"), []byte("PNGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dataDir, "config.yaml")
	if err := os.WriteFile(secret, []byte("jwt_secret: SUPER-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A legitimate screenshot is served.
	c, w := screenshotCtx("shot-agent", "cap.png")
	s.handleServeScreenshot(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "PNGDATA") {
		t.Fatalf("legit screenshot = %d %.120s", w.Code, w.Body.String())
	}

	// Traversal attempts: relative segments, absolute paths, backslashes and an
	// encoded separator must never escape the agent directory.
	for _, name := range []string{
		"../../config.yaml",
		"..\\..\\config.yaml",
		"../../../etc/passwd",
		"/etc/passwd",
		"sub/../../config.yaml",
		"..%2f..%2fconfig.yaml",
	} {
		c, w = screenshotCtx("shot-agent", name)
		s.handleServeScreenshot(c)
		if strings.Contains(w.Body.String(), "SUPER-SECRET") {
			t.Fatalf("%q served a file outside the screenshot root", name)
		}
		if w.Code == http.StatusOK {
			t.Fatalf("%q was served (status 200)", name)
		}
	}

	// A non-image extension is refused even when the path is contained.
	if err := os.WriteFile(filepath.Join(shotDir, "notes.txt"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, w = screenshotCtx("shot-agent", "notes.txt")
	s.handleServeScreenshot(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-image = %d %s", w.Code, w.Body.String())
	}

	// Another tenant's agent is invisible: 404 before any file handling.
	if err := s.db.Create(&db.Implant{ID: "foreign-shot", TenantID: 2, Hostname: "foreign", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	c, w = screenshotCtx("foreign-shot", "cap.png")
	s.handleServeScreenshot(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign agent screenshot = %d %s", w.Code, w.Body.String())
	}
}

// TestListAgentScreenshotsPagination covers the listing endpoint: the tenant
// gate, an empty directory, and that pagination slices without panicking.
func TestListAgentScreenshotsPagination(t *testing.T) {
	s := mustTenantServer(t)
	dataDir := t.TempDir()
	s.cfg.Server.DataDir = dataDir
	seedAIUser(t, s, "admin1", 1)
	if err := s.db.Create(&db.Implant{ID: "list-agent", TenantID: 1, Hostname: "list", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}

	list := func(params gin.Params, query string) (*httptest.ResponseRecorder, []string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/agents/list/screenshots?"+query, nil)
		c.Set("user", "admin1")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Params = params
		s.handleListAgentScreenshots(c)
		return w, nil
	}

	// No screenshots yet: an empty list, not an error.
	w, _ := list(gin.Params{{Key: "id", Value: "list-agent"}}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("empty list = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("empty list body = %.200s", w.Body.String())
	}

	// One screenshot, requested with a page size that would slice past the end.
	shotDir := filepath.Join(dataDir, "screenshots", "list-agent")
	if err := os.MkdirAll(shotDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shotDir, "one.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _ = list(gin.Params{{Key: "id", Value: "list-agent"}}, "page=3&page_size=50")
	if w.Code != http.StatusOK {
		t.Fatalf("page past end = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatalf("total = %.200s", w.Body.String())
	}

	// A foreign agent is invisible.
	if err := s.db.Create(&db.Implant{ID: "foreign-list", TenantID: 2, Hostname: "f", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	w, _ = list(gin.Params{{Key: "id", Value: "foreign-list"}}, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign list = %d %s", w.Code, w.Body.String())
	}
}
