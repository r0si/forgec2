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

// TestEngagementCloseStampsAgents covers the opt-in side effect of closing an exercise.
//
// Dispatch already refuses tasks once an engagement is closed, so stamping the deadline onto the member hosts is defence in
// depth - but it is the difference between "this host is done because its exercise is" and "this host is done", which is
// what an operator reading the agent list needs. It is opt-in because a close is a bookkeeping decision and must not
// silently write forty agent rows.
func TestEngagementCloseStampsAgents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	build := func(name string, memberIDs []string) (db.Engagement, []string) {
		engagement := db.Engagement{TenantID: 1, Name: name, StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(time.Hour)}
		if err := s.db.Create(&engagement).Error; err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
		for _, id := range memberIDs {
			if err := s.db.Create(&db.Implant{
				ID: id, TenantID: 1, Hostname: id + "-host", Status: "online", LastSeen: now,
				EngagementID: &engagement.ID,
			}).Error; err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
		return engagement, memberIDs
	}

	closeIt := func(id uint, body map[string]interface{}) (int, map[string]interface{}) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/engagements/x/close", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: itoaUint(id)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleCloseEngagement(c)
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

	// 1. Without the flag nothing is written: a close is bookkeeping.
	plain, plainMembers := build("plain", []string{"p-1", "p-2"})
	code, data := closeIt(plain.ID, map[string]interface{}{})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["stamped_agents"] != float64(0) {
		t.Fatalf("a plain close stamped %v agents", data["stamped_agents"])
	}
	for _, id := range plainMembers {
		if expiryOf(id) != nil {
			t.Fatalf("%s got a deadline from a plain close", id)
		}
	}

	// 2. With the flag, every member host carries the closing instant.
	stamped, stampedMembers := build("stamped", []string{"s-1", "s-2", "s-3"})
	// A host from another engagement must not be touched.
	other, otherMembers := build("other", []string{"o-1"})
	code, data = closeIt(stamped.ID, map[string]interface{}{"stamp_agents": true})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if data["stamped_agents"] != float64(3) {
		t.Fatalf("stamped_agents = %v, want the three member hosts", data["stamped_agents"])
	}
	for _, id := range stampedMembers {
		if expiryOf(id) == nil {
			t.Fatalf("%s did not get the close stamped", id)
		}
	}
	for _, id := range otherMembers {
		if expiryOf(id) != nil {
			t.Fatalf("%s was stamped by another engagement's close", id)
		}
	}
	// The other engagement is still open, so its host is unaffected either way.
	_ = other

	// 3. A second close is refused, stamping or not.
	if code, _ := closeIt(stamped.ID, map[string]interface{}{"stamp_agents": true}); code != http.StatusConflict {
		t.Fatalf("a second close = %d, want 409", code)
	}

	// 4. The stamping is audited separately from the close, so "who ended the hosts" has an answer.
	var closes, stamps int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_close").Count(&closes).Error; err != nil {
		t.Fatalf("count close: %v", err)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_close_stamp").Count(&stamps).Error; err != nil {
		t.Fatalf("count stamp: %v", err)
	}
	if closes != 2 || stamps != 1 {
		t.Fatalf("audit rows: %d close, %d stamp; want 2 close and 1 stamp", closes, stamps)
	}
}
