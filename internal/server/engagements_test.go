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

// TestEngagementLifecycle covers the time-boxed container above the tenant: create, list, close.
//
// The integrity rule worth having from the start is the overlap refusal - two simultaneous "exercises"
// against one customer is almost always a mistake, and it should be refused at creation rather than
// discovered when two teams task the same host.
func TestEngagementLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour

	call := func(method, target string, body map[string]interface{}, tenant uint, params gin.Params) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, target, reader)
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = params
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, tenant)
		switch {
		case target == "/api/engagements" && method == http.MethodPost:
			s.handleCreateEngagement(c)
		case len(params) > 0:
			s.handleCloseEngagement(c)
		default:
			s.handleListEngagements(c)
		}
		return w
	}

	create := func(name string, starts, ends time.Time, tenant uint) *httptest.ResponseRecorder {
		return call(http.MethodPost, "/api/engagements", map[string]interface{}{
			"name": name, "notes": "scoped to the DMZ",
			"starts_at": starts.Format(time.RFC3339), "ends_at": ends.Format(time.RFC3339),
		}, tenant, nil)
	}

	// 1. Create: the state is derived, not stored - an engagement that already started is active.
	w := create("Q4 exercise", now.Add(-day), now.Add(7*day), 1)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d (%s)", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			Engagement struct {
				ID     uint   `json:"id"`
				State  string `json:"state"`
				Active bool   `json:"active"`
			} `json:"engagement"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Data.Engagement.State != "active" || !created.Data.Engagement.Active {
		t.Fatalf("state = %q active=%v, want active", created.Data.Engagement.State, created.Data.Engagement.Active)
	}
	id := created.Data.Engagement.ID

	// 2. A second engagement that overlaps the open one is refused with 409.
	if got := create("overlapping", now, now.Add(3*day), 1); got.Code != http.StatusConflict {
		t.Fatalf("overlapping create = %d, want 409 (%s)", got.Code, got.Body.String())
	}

	// 3. A window in the future that does not intersect is allowed; its state is "scheduled".
	if got := create("next quarter", now.Add(30*day), now.Add(37*day), 1); got.Code != http.StatusOK {
		t.Fatalf("future create = %d (%s)", got.Code, got.Body.String())
	}

	// 4. Another tenant's window is independent: the same dates are fine there.
	if got := create("other customer", now, now.Add(3*day), 2); got.Code != http.StatusOK {
		t.Fatalf("other tenant create = %d (%s)", got.Code, got.Body.String())
	}

	// 5. Listing is tenant-scoped and newest window first.
	w = call(http.MethodGet, "/api/engagements", nil, 1, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", w.Code, w.Body.String())
	}
	var listed struct {
		Data struct {
			Count       int              `json:"count"`
			Engagements []map[string]any `json:"engagements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Data.Count != 2 {
		t.Fatalf("count = %d, want 2 (tenant isolation)", listed.Data.Count)
	}
	if listed.Data.Engagements[0]["name"] != "next quarter" {
		t.Fatalf("order = %v, want the newest window first", listed.Data.Engagements)
	}

	// 6. Close: the agreed window is preserved, the state becomes closed, and closing twice conflicts.
	w = call(http.MethodPost, "/api/engagements/x/close", nil, 1, gin.Params{{Key: "id", Value: itoaUintTest(id)}})
	if w.Code != http.StatusOK {
		t.Fatalf("close = %d (%s)", w.Code, w.Body.String())
	}
	var closed struct {
		Data struct {
			Engagement map[string]any `json:"engagement"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &closed); err != nil {
		t.Fatalf("decode close: %v", err)
	}
	if closed.Data.Engagement["state"] != "closed" {
		t.Fatalf("state after close = %v", closed.Data.Engagement["state"])
	}
	if closed.Data.Engagement["ends_at"] == "" {
		t.Fatalf("closing rewrote the agreed window: %v", closed.Data.Engagement)
	}
	if got := call(http.MethodPost, "/api/engagements/x/close", nil, 1, gin.Params{{Key: "id", Value: itoaUintTest(id)}}); got.Code != http.StatusConflict {
		t.Fatalf("double close = %d, want 409", got.Code)
	}

	// 7. A closed engagement no longer blocks the overlapping window: history must not prevent new work.
	if got := create("replacement", now.Add(day), now.Add(4*day), 1); got.Code != http.StatusOK {
		t.Fatalf("create after close = %d, want 200 (%s)", got.Code, got.Body.String())
	}

	// 8. Bad input is refused with a reason.
	for name, body := range map[string]map[string]interface{}{
		"no name":   {"starts_at": now.Format(time.RFC3339), "ends_at": now.Add(day).Format(time.RFC3339)},
		"bad start": {"name": "x", "starts_at": "nope", "ends_at": now.Add(day).Format(time.RFC3339)},
		"backwards": {"name": "x", "starts_at": now.Add(day).Format(time.RFC3339), "ends_at": now.Format(time.RFC3339)},
	} {
		if got := call(http.MethodPost, "/api/engagements", body, 1, nil); got.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", name, got.Code)
		}
	}

	// 9. Both decisions are audited.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action IN ?", []string{"engagement_create", "engagement_close"}).Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited < 5 {
		t.Fatalf("audit rows = %d, want at least 5 (four creates + one close)", audited)
	}
}

// itoaUintTest renders a uint for a URL parameter in tests.
func itoaUintTest(v uint) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
