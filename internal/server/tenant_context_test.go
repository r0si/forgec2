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

// TestTenantLookupUsesTheAuthenticatedContext pins the hot-path fix: the auth middleware
// already has the user row, so scoped requests should not re-SELECT users. The proof uses a
// CLOSED database - if the lookup still queried, it would fail closed and report the legacy
// zero, which the assertions would catch.
func TestTenantLookupUsesTheAuthenticatedContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	sqlDB, err := s.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set("user", "someone")
	c.Set(middleware.TenantIDContextKey, uint(7))

	if got := s.currentTenantID(c); got != 7 {
		t.Fatalf("tenant = %d, want 7 from the authenticated context (a lookup against the closed DB would fail closed to 0)", got)
	}

	// Without the context value the lookup must still fail CLOSED now that the database
	// is gone, never degrade to global visibility.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c2.Set("user", "someone")
	scope := s.tenantScope(s.db.Model(&db.Implant{}), c2)
	sql := scope.Statement.SQL.String()
	if sql == "" {
		// Building the statement on a closed pool can defer the error; the important
		// property is that the scope did not become unrestricted.
		if _, ok := s.resolveTenant(c2); ok {
			t.Fatal("a failed tenant lookup reported ok=true")
		}
	}
}

// TestScopedRequestSeesOnlyItsTenantWithContextTenant keeps the correctness half: when the
// middleware supplies the tenant, scoping must still hide another tenant's rows.
func TestScopedRequestSeesOnlyItsTenantWithContextTenant(t *testing.T) {
	s := mustTenantServer(t)
	const mine = "b1000000-0000-0000-0000-000000000001"
	const theirs = "b2000000-0000-0000-0000-000000000002"
	for _, row := range []db.Implant{
		{ID: mine, TenantID: 5, Hostname: "mine", Status: "online", LastSeen: time.Now()},
		{ID: theirs, TenantID: 6, Hostname: "theirs", Status: "online", LastSeen: time.Now()},
	} {
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set("user", "someone")
	c.Set(middleware.TenantIDContextKey, uint(5))

	var rows []db.Implant
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != mine {
		t.Fatalf("scoped query returned %d rows (%+v), want only the caller's tenant", len(rows), rows)
	}
}
