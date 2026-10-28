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

// TestAgentsFilterByEngagement covers GET /api/agents?engagement=<id>: "who is in this exercise".
//
// The roll-up endpoint (round 23) says how many hosts an engagement covers; this answers WHICH ones,
// and `engagement=none` answers the inverse question - which implants no deadline will ever stop.
func TestAgentsFilterByEngagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "exercise", StartsAt: now, EndsAt: now.Add(24 * time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}

	seed := func(id string, tenant uint, engagementID *uint) {
		if err := s.db.Create(&db.Implant{
			ID: id, TenantID: tenant, Hostname: id, Status: "online", LastSeen: now,
			EngagementID: engagementID,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("in-scope-a", 1, &engagement.ID)
	seed("in-scope-b", 1, &engagement.ID)
	seed("unscoped", 1, nil)
	seed("other-tenant", 2, &engagement.ID)

	call := func(query string, tenant uint) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/agents"+query, nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.writeAgentListJSON(c)
		return w
	}

	names := func(w *httptest.ResponseRecorder) []string {
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
		}
		var resp struct {
			Data struct {
				Agents []struct {
					ID string `json:"id"`
				} `json:"agents"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		out := make([]string, 0, len(resp.Data.Agents))
		for _, agent := range resp.Data.Agents {
			out = append(out, agent.ID)
		}
		return out
	}

	// 1. The engagement filter returns exactly its members, and only from the caller's tenant.
	members := names(call("?engagement="+itoaUintTest(engagement.ID), 1))
	if len(members) != 2 {
		t.Fatalf("members = %v, want the two linked agents", members)
	}
	for _, name := range members {
		if name == "unscoped" || name == "other-tenant" {
			t.Fatalf("members = %v, want neither the unscoped agent nor another tenant's", members)
		}
	}

	// 2. "none" is the inverse question: which implants no engagement deadline will ever stop.
	unscoped := names(call("?engagement=none", 1))
	if len(unscoped) != 1 || unscoped[0] != "unscoped" {
		t.Fatalf("engagement=none = %v, want exactly the unscoped agent", unscoped)
	}

	// 3. No filter still lists the whole tenant, so the parameter is additive.
	if all := names(call("", 1)); len(all) != 3 {
		t.Fatalf("unfiltered = %v, want all three tenant agents", all)
	}

	// 4. A non-numeric value is a 400, not a silently ignored filter.
	if got := call("?engagement=not-a-number", 1); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid engagement = %d, want 400", got.Code)
	}

	// 5. Another tenant's filter sees nothing: scoping is not bypassed by naming an engagement id.
	if other := names(call("?engagement="+itoaUintTest(engagement.ID), 2)); len(other) != 1 || other[0] != "other-tenant" {
		t.Fatalf("tenant 2 members = %v, want only its own agent (tenant scoping still applies)", other)
	}
}
