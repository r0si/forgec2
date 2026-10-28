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

// TestSetAgentExpiry covers POST /api/agents/:id/expiry: the deadline made enforceable in round 17 was
// settable only at build time, which meant an extended engagement had to be tracked in someone's head -
// exactly what the deadline exists to prevent.
func TestSetAgentExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now()
	if err := s.db.Create(&db.Implant{
		ID: "agent-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "agent-other", TenantID: 2, Hostname: "other", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}

	call := func(agentID string, body map[string]interface{}, tenant uint) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/expiry", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: agentID}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		s.handleSetAgentExpiry(c)
		return w
	}

	deadline := now.Add(72 * time.Hour).UTC().Truncate(time.Second)

	// 1. Setting a future deadline reports the previous value (none) and that the agent is still live.
	w := call("agent-1", map[string]interface{}{"expires_at": deadline.Format(time.RFC3339)}, 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ExpiresAt string `json:"expires_at"`
			Previous  string `json:"previous"`
			Expired   bool   `json:"expired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ExpiresAt != deadline.Format(time.RFC3339) || resp.Data.Previous != "none" || resp.Data.Expired {
		t.Fatalf("response = %+v", resp.Data)
	}
	var stored db.Implant
	if err := s.db.Where("id = ?", "agent-1").First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.ExpiresAt == nil || !stored.ExpiresAt.UTC().Equal(deadline) {
		t.Fatalf("stored deadline = %v, want %v", stored.ExpiresAt, deadline)
	}

	// 2. Extending it reports the previous value, so the audit line is readable.
	extended := now.Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	w = call("agent-1", map[string]interface{}{"expires_at": extended.Format(time.RFC3339)}, 1)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Previous != deadline.Format(time.RFC3339) {
		t.Fatalf("previous = %q, want %q", resp.Data.Previous, deadline.Format(time.RFC3339))
	}

	// 3. Setting a deadline in the past is how an engagement ends for one host immediately; the
	// response says so rather than pretending it is scheduled.
	past := now.Add(-time.Hour).UTC().Truncate(time.Second)
	w = call("agent-1", map[string]interface{}{"expires_at": past.Format(time.RFC3339)}, 1)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Data.Expired {
		t.Fatalf("a past deadline was not reported as expired: %+v", resp.Data)
	}
	// ...and task creation now refuses it, which is the point of setting it.
	if err := s.validateTaskCreationWithAgent(nil, "agent-1", "shell", "whoami", "", "", 0, nil); err == nil {
		t.Fatal("an agent with a past deadline accepted a task")
	}

	// 4. Clearing removes the deadline and the agent accepts work again.
	w = call("agent-1", map[string]interface{}{"clear": true}, 1)
	if w.Code != http.StatusOK {
		t.Fatalf("clear status = %d (%s)", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ExpiresAt != "none" || resp.Data.Expired {
		t.Fatalf("clear response = %+v", resp.Data)
	}
	if err := s.validateTaskCreationWithAgent(nil, "agent-1", "shell", "whoami", "", "", 0, nil); err != nil {
		t.Fatalf("a cleared deadline still refused work: %v", err)
	}

	// 5. Bad input is a 400, not a silent no-op.
	for _, body := range []map[string]interface{}{
		{},                                    // neither field
		{"expires_at": "not-a-timestamp"},     // unparseable
		{"expires_at": "2026-11-01 18:00:00"}, // RFC3339 requires the T
	} {
		if got := call("agent-1", body, 1); got.Code != http.StatusBadRequest {
			t.Fatalf("body %v = %d, want 400", body, got.Code)
		}
	}

	// 6. A deadline change is audited with both values.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "agent_expiry_set").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	// Four decisions, not three: the past-deadline step is its own change (it is how an engagement
	// ends for one host immediately), so it must be in the trail too. My first expectation forgot it -
	// the code was right.
	if audited != 4 {
		t.Fatalf("agent_expiry_set rows = %d, want 4 (set, extend, expire-now, clear)", audited)
	}

	// 7. Another tenant's agent is invisible, so the deadline cannot be changed across tenants.
	if got := call("agent-other", map[string]interface{}{"clear": true}, 1); got.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant change = %d, want 404", got.Code)
	}
}
