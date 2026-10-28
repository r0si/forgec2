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

// TestEngagementExpiryRollsUpPerEngagement covers the per-engagement summary: the deadline that stops
// work usually belongs to an exercise, so "how much time is left on it and how many hosts does it cover"
// is the question an operator actually asks - the per-host list alone does not answer it.
func TestEngagementExpiryRollsUpPerEngagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	soon := db.Engagement{TenantID: 1, Name: "short exercise", StartsAt: now.Add(-day), EndsAt: now.Add(2 * day)}
	later := db.Engagement{TenantID: 1, Name: "long exercise", StartsAt: now, EndsAt: now.Add(10 * day)}
	foreign := db.Engagement{TenantID: 2, Name: "other customer", StartsAt: now, EndsAt: now.Add(5 * day)}
	for _, engagement := range []*db.Engagement{&soon, &later, &foreign} {
		if err := s.db.Create(engagement).Error; err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
	}

	expiredDeadline := now.Add(-time.Hour)
	liveDeadline := now.Add(48 * time.Hour)
	seed := func(id string, tenant uint, deadline *time.Time, engagementID *uint) {
		if err := s.db.Create(&db.Implant{
			ID: id, TenantID: tenant, Hostname: id, Status: "online", LastSeen: now,
			ExpiresAt: deadline, EngagementID: engagementID,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("soon-a", 1, &liveDeadline, &soon.ID)
	seed("soon-b", 1, &expiredDeadline, &soon.ID)
	seed("later-a", 1, &liveDeadline, &later.ID)
	seed("unlinked", 1, &liveDeadline, nil)
	seed("foreign-a", 2, &liveDeadline, &foreign.ID)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/expiry", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleEngagementExpiry(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Total       int              `json:"total"`
			Engagements []map[string]any `json:"engagements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// Only this tenant's two engagements, ordered by when they end.
	if len(resp.Data.Engagements) != 2 {
		t.Fatalf("engagements = %v, want 2 (tenant isolation)", resp.Data.Engagements)
	}
	first, second := resp.Data.Engagements[0], resp.Data.Engagements[1]
	if first["name"] != "short exercise" || second["name"] != "long exercise" {
		t.Fatalf("order = %v / %v, want soonest-ending first", first["name"], second["name"])
	}

	// Counts are per engagement, and the expired agent is counted inside its own engagement.
	if first["agents"] != float64(2) || first["agents_expired"] != float64(1) {
		t.Fatalf("short exercise = agents %v / expired %v, want 2 / 1", first["agents"], first["agents_expired"])
	}
	if second["agents"] != float64(1) || second["agents_expired"] != float64(0) {
		t.Fatalf("long exercise = agents %v / expired %v, want 1 / 0", second["agents"], second["agents_expired"])
	}

	// Remaining time is derived, and a started engagement is active.
	remaining, ok := first["ends_in_seconds"].(float64)
	if !ok || remaining <= 0 || remaining > float64(3*24*60*60) {
		t.Fatalf("ends_in_seconds = %v", first["ends_in_seconds"])
	}
	if first["state"] != "active" {
		t.Fatalf("state = %v, want active", first["state"])
	}

	// The unlinked agent is still part of the fleet total, just not of any roll-up: four agents belong
	// to tenant 1 (two in the short exercise, one in the long one, one unlinked).
	if resp.Data.Total != 4 {
		t.Fatalf("fleet total = %d, want 4", resp.Data.Total)
	}
}
