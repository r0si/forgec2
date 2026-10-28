package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestEngagementExpiryStatus covers GET /api/engagements/expiry: the deadlines made enforceable last
// round, made visible. A deadline nobody can see is still a surprise - the fleet simply stops accepting
// work one day and the first sign is a 403.
func TestEngagementExpiryStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now()
	past := now.Add(-2 * time.Hour)
	inAnHour := now.Add(time.Hour)
	inThreeDays := now.Add(72 * time.Hour)
	inAMonth := now.Add(30 * 24 * time.Hour)

	seed := func(id string, tenant uint, expires *time.Time) {
		if err := s.db.Create(&db.Implant{
			ID: id, TenantID: tenant, Hostname: id, Status: "online", LastSeen: now, ExpiresAt: expires,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("already-expired", 1, &past)
	seed("expires-today", 1, &inAnHour)
	seed("expires-this-week", 1, &inThreeDays)
	seed("expires-next-month", 1, &inAMonth)
	seed("no-deadline", 1, nil)
	seed("other-tenant", 2, &past)

	call := func(tenant uint) map[string]interface{} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/expiry", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleEngagementExpiry(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		return resp.Data
	}

	data := call(1)
	// Tenant 1 has five agents: one expired, one within 24h, one within the week, one next month, one
	// unbounded. The other tenant's agent must not appear at all.
	if got := data["total"]; got != float64(5) {
		t.Fatalf("total = %v, want 5 (tenant isolation)", got)
	}
	if got := data["unbounded"]; got != float64(1) {
		t.Fatalf("unbounded = %v, want 1", got)
	}
	if got := data["expired"]; got != float64(1) {
		t.Fatalf("expired = %v, want 1", got)
	}
	if got := data["within_24h"]; got != float64(1) {
		t.Fatalf("within_24h = %v, want 1", got)
	}
	if got := data["within_7d"]; got != float64(1) {
		t.Fatalf("within_7d = %v, want 1", got)
	}
	if got := data["bounded"]; got != float64(4) {
		t.Fatalf("bounded = %v, want 4 (everything with a deadline)", got)
	}

	// The earliest deadline is the expired one - the status line's single glance.
	earliest, ok := data["earliest"].(map[string]interface{})
	if !ok {
		t.Fatalf("earliest missing: %v", data["earliest"])
	}
	if earliest["agent_id"] != "already-expired" || earliest["expired"] != true {
		t.Fatalf("earliest = %v", earliest)
	}

	// The list is ordered by deadline so a UI can render "what is next".
	agents, ok := data["agents"].([]interface{})
	if !ok || len(agents) != 4 {
		t.Fatalf("agents = %v, want 4 bounded entries", data["agents"])
	}
	first, _ := agents[0].(map[string]interface{})
	last, _ := agents[len(agents)-1].(map[string]interface{})
	if first["agent_id"] != "already-expired" || last["agent_id"] != "expires-next-month" {
		t.Fatalf("list is not ordered by deadline: %v", agents)
	}

	// Tenant 2 sees only its own expired agent, and nothing from tenant 1.
	other := call(2)
	if got := other["total"]; got != float64(1) {
		t.Fatalf("tenant 2 total = %v, want 1", got)
	}
	if got := other["expired"]; got != float64(1) {
		t.Fatalf("tenant 2 expired = %v, want 1", got)
	}

	// A fleet with no deadlines reports zeros, not nulls.
	empty := call(3)
	if got := empty["total"]; got != float64(0) {
		t.Fatalf("empty tenant total = %v", got)
	}
	if got, ok := empty["agents"].([]interface{}); !ok || len(got) != 0 {
		t.Fatalf("empty tenant agents = %v, want []", empty["agents"])
	}
	if _, present := empty["earliest"]; present {
		t.Fatalf("an empty fleet reported an earliest deadline: %v", empty["earliest"])
	}
}
