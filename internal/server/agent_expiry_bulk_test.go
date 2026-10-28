package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBulkSetAgentExpiry covers the fleet-wide deadline.
//
// Ending an exercise means time-boxing every host, and doing that one agent at a time is forty requests and one omission away
// from a beacon that outlives the engagement. The endpoint's strictness is the point: an id the caller cannot see is COUNTED
// AND NAMED, because "I set 39 of 40" must not look like success.
func TestBulkSetAgentExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	for _, id := range []string{"a-1", "a-2"} {
		if err := s.db.Create(&db.Implant{
			ID: id, TenantID: 1, Hostname: id + "-host", Status: "online", LastSeen: now, EngagementID: &engagement.ID,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Another tenant's agent, and one with no engagement: neither may be touched by the first two requests.
	if err := s.db.Create(&db.Implant{
		ID: "other", TenantID: 2, Hostname: "other-host", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "loose", TenantID: 1, Hostname: "loose-host", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed loose: %v", err)
	}

	call := func(body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/bulk/expiry", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleBulkSetAgentExpiry(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}
	expiryOf := func(id string) *time.Time {
		var agent db.Implant
		if err := s.db.Select("id, expires_at").First(&agent, "id = ?", id).Error; err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return agent.ExpiresAt
	}

	deadline := now.Add(48 * time.Hour).UTC().Format(time.RFC3339)

	// 1. An engagement applies to its own hosts only.
	code, data := call(map[string]interface{}{"engagement_id": engagement.ID, "expires_at": deadline})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["updated"] != float64(2) {
		t.Fatalf("updated = %v, want the two engagement hosts", data["updated"])
	}
	for _, id := range []string{"a-1", "a-2"} {
		if expiryOf(id) == nil {
			t.Fatalf("%s did not get a deadline", id)
		}
	}
	// The loose host and the other tenant's host are untouched.
	if expiryOf("loose") != nil {
		t.Fatal("an agent outside the engagement got a deadline")
	}
	if expiryOf("other") != nil {
		t.Fatal("another tenant's agent was modified")
	}

	// 2. An explicit list reports what it could not see, rather than silently dropping it.
	code, data = call(map[string]interface{}{
		"agent_ids": []string{"loose", "other", "missing"}, "expires_at": deadline,
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["updated"] != float64(1) {
		t.Fatalf("updated = %v, want only the one visible agent", data["updated"])
	}
	skipped, _ := data["skipped"].([]interface{})
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want the other tenant's agent and the missing one", data["skipped"])
	}
	if expiryOf("other") != nil {
		t.Fatal("another tenant's agent was modified through the explicit list")
	}

	// 3. Clearing removes the deadline.
	code, data = call(map[string]interface{}{"agent_ids": []string{"a-1", "a-2"}, "clear": true})
	if code != http.StatusOK {
		t.Fatalf("clear status = %d", code)
	}
	if data["expires_at"] != "cleared" {
		t.Fatalf("clear reported %v", data["expires_at"])
	}
	for _, id := range []string{"a-1", "a-2"} {
		if expiryOf(id) != nil {
			t.Fatalf("%s kept its deadline after a clear", id)
		}
	}

	// 4. Bad input is refused rather than defaulted.
	for _, body := range []map[string]interface{}{
		{"agent_ids": []string{"a-1"}},                               // no deadline
		{"agent_ids": []string{"a-1"}, "expires_at": "next tuesday"}, // malformed deadline
		{"expires_at": deadline},                                     // no selection
		{},                                                           // nothing at all
	} {
		if code, _ := call(body); code != http.StatusBadRequest {
			t.Fatalf("%v = %d, want 400", body, code)
		}
	}

	// 5. Every application is audited with its counts.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "agent_expiry_bulk").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 3 {
		t.Fatalf("agent_expiry_bulk rows = %d, want 3 (engagement, list, clear)", audited)
	}
}
