package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func healthCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestHealthReportsAuditChain pins the new field: the health payload the console
// already shows now carries the chain verdict, so an operator sees tampering
// without opening a separate page or remembering a CLI.
func TestHealthReportsAuditChain(t *testing.T) {
	s := mustTenantServer(t)
	seedAuditChain(t, s, 2)

	c, w := healthCtx()
	s.apiHealth(c)
	if w.Code != http.StatusOK {
		t.Fatalf("health = %d", w.Code)
	}
	var out struct {
		AuditChain struct {
			OK      bool   `json:"ok"`
			Checked int    `json:"checked"`
			Reason  string `json:"reason"`
		} `json:"audit_chain"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if !out.AuditChain.OK {
		t.Fatalf("an intact chain was reported broken: %+v", out.AuditChain)
	}
	if out.AuditChain.Checked < 2 {
		t.Fatalf("checked = %d", out.AuditChain.Checked)
	}
}

// TestHealthAuditVerdictIsCached pins the cache: the console polls this endpoint,
// so a verification must not run on every hit - and the staleness it introduces is
// bounded by the TTL.
func TestHealthAuditVerdictIsCached(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 2)

	// Prime the cache.
	c, _ := healthCtx()
	s.apiHealth(c)

	// Tamper after the cache was filled.
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[0].ID).Update("details", "tampered").Error; err != nil {
		t.Fatal(err)
	}

	var out struct {
		AuditChain struct {
			OK bool `json:"ok"`
		} `json:"audit_chain"`
	}
	c, w := healthCtx()
	s.apiHealth(c)
	unmarshalData(t, w.Body.Bytes(), &out)
	if !out.AuditChain.OK {
		t.Fatal("the verdict was recomputed inside the TTL window; the cache is not working")
	}

	// Expire the cache: the same payload now reports the failure.
	s.auditVerdictMu.Lock()
	s.auditVerdictAt = time.Now().Add(-2 * auditHealthCacheTTL)
	s.auditVerdictMu.Unlock()

	c, w = healthCtx()
	s.apiHealth(c)
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.AuditChain.OK {
		t.Fatal("the failure was not reported after the cache expired")
	}
}

// TestReadyStillVerifiesImmediately is the contrast that keeps the two paths
// honest: /ready must NOT serve a cached verdict, because it is what an alert
// fires on.
func TestReadyStillVerifiesImmediately(t *testing.T) {
	s := mustTenantServer(t)
	rows := seedAuditChain(t, s, 2)

	c, _ := readyCtx()
	s.handleReadyCheck(c) // fills the health cache path? no - /ready never caches

	// Fill the health cache, then tamper.
	c, _ = healthCtx()
	s.apiHealth(c)
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", rows[0].ID).Update("success", false).Error; err != nil {
		t.Fatal(err)
	}

	c, w := readyCtx()
	s.handleReadyCheck(c)
	if !containsAny(w.Body.String(), "integrity failure at row") {
		t.Fatalf("/ready did not notice the tampering immediately: %s", w.Body.String())
	}
}

// TestHealthDegradedPaths covers the branches the console relies on when startup
// is partial: no database at all, and a database handle that cannot be pinged.
func TestHealthDegradedPaths(t *testing.T) {
	// A server without a database answers "degraded" instead of panicking.
	bare := &Server{}
	c, w := healthCtx()
	bare.apiHealth(c)
	if w.Code != http.StatusOK {
		t.Fatalf("bare server health = %d", w.Code)
	}
	var out struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Status != "degraded" || out.Database != "unavailable" {
		t.Fatalf("bare server health = %+v", out)
	}
	// A bare server must not report an audit chain it cannot check.
	if containsAny(w.Body.String(), "audit_chain") {
		t.Fatalf("a server without a database reported an audit chain: %s", w.Body.String())
	}

	// A closed pool pings with an error: the endpoint reports "unreachable"
	// (or degrades) rather than 500ing.
	s := mustTenantServer(t)
	sqlDB, err := s.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close pool: %v", err)
	}
	c, w = healthCtx()
	s.apiHealth(c)
	if w.Code != http.StatusOK {
		t.Fatalf("closed pool health = %d (%s)", w.Code, w.Body.String())
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Status == "" {
		t.Fatalf("no status was reported: %s", w.Body.String())
	}
}
