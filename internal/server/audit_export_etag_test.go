package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAuditExportETag covers revalidation of the export: an investigator holding a copy should learn
// "nothing new" with a 304 instead of downloading the whole selection again.
func TestAuditExportETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	seed := func(user, action string, tenant uint) {
		row := db.AuditLog{
			User: user, Action: action, TenantID: tenant, Success: true,
			CreatedAt: time.Now(), PrevHash: "p", EntryHash: "e",
		}
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed("alice", "login", 1)
	seed("bob", "task_create", 1)

	call := func(ifNoneMatch string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export", nil)
		if ifNoneMatch != "" {
			c.Request.Header.Set("If-None-Match", ifNoneMatch)
		}
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleExportAuditChain(c)
		return w
	}

	// 1. The first request returns the rows and a tag.
	first := call("", 1)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the export")
	}
	if cc := first.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("Cache-Control = %q, want revalidation", cc)
	}

	// 2. Re-fetching with the tag is a 304 with no body.
	second := call(etag, 1)
	t.Logf("first etag=%q second etag=%q", etag, second.Header().Get("ETag"))
	if second.Code != http.StatusNotModified {
		t.Fatalf("repeat fetch = %d, want 304", second.Code)
	}
	if body := second.Body.String(); body != "" {
		t.Fatalf("a 304 carried a body: %q", body)
	}

	// 3. Appending a row invalidates the tag, and the new export contains it.
	seed("carol", "login", 1)
	third := call(etag, 1)
	if third.Code != http.StatusOK {
		t.Fatalf("after an append = %d, want 200 (the tag must change)", third.Code)
	}
	if third.Header().Get("ETag") == etag {
		t.Fatal("the ETag did not change after a new audit row")
	}

	// 4. A wildcard If-None-Match matches, as the spec says.
	if got := call("*", 1); got.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: * = %d, want 304", got.Code)
	}

	// 5. Tenant 2 must not reuse tenant 1's tag.
	t2 := call("", 2)
	if t2.Header().Get("ETag") == third.Header().Get("ETag") {
		t.Fatalf("two tenants produced the same ETag: %q", t2.Header().Get("ETag"))
	}
	// And tenant 1's tag must not satisfy tenant 2's request.
	if got := call(third.Header().Get("ETag"), 2); got.Code != http.StatusOK {
		t.Fatalf("a foreign tenant's tag returned %d, want 200", got.Code)
	}
}

// TestAuditExportNotModifiedUnit checks the comparison itself, so a failure points at the helper
// rather than at the handler.
func TestAuditExportNotModifiedUnit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tag := `W/"v1-1-max=2-since=0-limit=50000"`
	cases := map[string]bool{
		tag:                                true,
		`"v1-1-max=2-since=0-limit=50000"`: true,
		"*":                                true,
		`W/"other"`:                        false,
		"":                                 false,
	}
	for header, want := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			c.Request.Header.Set("If-None-Match", header)
		}
		if got := auditExportNotModified(c, tag); got != want {
			t.Errorf("If-None-Match %q -> %v, want %v", header, got, want)
		}
	}
}
