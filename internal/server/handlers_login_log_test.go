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

// TestLoginEventsReportIPTimeAndCountry pins the login log contract: only
// authentication actions are returned, each row carries the recorded client
// IP, the login time and the country cached for that IP, and the result filter
// separates successful from failed attempts.
func TestLoginEventsReportIPTimeAndCountry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// A public IP with a cached country, as the resolver would have written it
	// on the first visit; the test must not reach a provider.
	if err := s.db.Create(&db.LoginGeo{IP: "203.0.113.9", Country: "德国", City: "法兰克福", ResolvedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.LoginGeo{IP: "198.51.100.4", Country: "美国", ResolvedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	// Relative to now: the 24h rollup below filters by wall-clock time, so a
	// fixed date makes this test expire the day after it was written.
	signedInAt := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	rows := []db.AuditLog{
		{User: "alice", Action: "login", Resource: "auth", IP: "203.0.113.9", Success: true, Details: "Login successful", CreatedAt: signedInAt},
		{User: "mallory", Action: "login_failed", Resource: "auth", IP: "198.51.100.4", Success: false, Error: "bad password", Details: "Wrong password", CreatedAt: signedInAt.Add(time.Minute)},
		{User: "alice", Action: "logout", Resource: "auth", IP: "203.0.113.9", Success: true, Details: "Login successful", CreatedAt: signedInAt.Add(2 * time.Minute)},
		// Not an auth action: must never appear in this view.
		{User: "alice", Action: "task_create", Resource: "task", IP: "203.0.113.9", Success: true, CreatedAt: signedInAt.Add(3 * time.Minute)},
		// Loopback: reported as "local", not looked up.
		{User: "admin", Action: "login", Resource: "auth", IP: "127.0.0.1", Success: true, CreatedAt: signedInAt.Add(4 * time.Minute)},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed audit row %d: %v", i, err)
		}
	}

	call := func(query string) (*httptest.ResponseRecorder, loginEventsResponse) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/audit/logins"+query, nil)
		c.Set("user", "root")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleGetLoginEvents(c)
		var body loginEventsResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (%s)", err, w.Body.String())
			}
		}
		return w, body
	}

	w, body := call("?page=1&pageSize=50")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	if len(body.Data.Events) != 4 {
		t.Fatalf("events = %d, want the 4 auth rows only: %+v", len(body.Data.Events), body.Data.Events)
	}
	// Newest first.
	if body.Data.Events[0].Action != "login" || body.Data.Events[0].IP != "127.0.0.1" {
		t.Fatalf("first event = %+v, want the newest loopback login", body.Data.Events[0])
	}
	if body.Data.Events[0].Country != "local" {
		t.Fatalf("loopback country = %q, want local", body.Data.Events[0].Country)
	}

	// The newest public-IP row reports the cached country and the recorded time.
	for _, e := range body.Data.Events {
		if e.User == "alice" && e.Action == "login" {
			if e.Country != "德国" {
				t.Fatalf("alice country = %q, want the cached 德国", e.Country)
			}
			if e.CreatedAt == "" {
				t.Fatal("created_at is empty: the login time must be reported")
			}
			ts, err := time.Parse(time.RFC3339, e.CreatedAt)
			if err != nil || !ts.Equal(signedInAt) {
				t.Fatalf("created_at = %q, want %s", e.CreatedAt, signedInAt.Format(time.RFC3339))
			}
			if e.IP != "203.0.113.9" {
				t.Fatalf("alice ip = %q", e.IP)
			}
		}
	}

	// result=failed keeps only the rejected attempt.
	_, failed := call("?result=failed")
	if len(failed.Data.Events) != 1 || failed.Data.Events[0].User != "mallory" || failed.Data.Events[0].Success {
		t.Fatalf("failed filter = %+v", failed.Data.Events)
	}
	if failed.Data.Events[0].Country != "美国" {
		t.Fatalf("failed row country = %q, want 美国", failed.Data.Events[0].Country)
	}

	// result=ok drops it.
	_, ok := call("?result=ok")
	for _, e := range ok.Data.Events {
		if e.User == "mallory" {
			t.Fatalf("result=ok returned a failed attempt: %+v", e)
		}
	}

	// user filter matches by substring.
	_, byUser := call("?user=mal")
	if len(byUser.Data.Events) != 1 || byUser.Data.Events[0].User != "mallory" {
		t.Fatalf("user filter = %+v", byUser.Data.Events)
	}

	// Pagination reports the unfiltered total so the table can size itself.
	_, paged := call("?page=2&pageSize=2")
	if paged.Data.Total != 4 || len(paged.Data.Events) != 2 {
		t.Fatalf("page 2 = %d events of total %d, want 2 of 4", len(paged.Data.Events), paged.Data.Total)
	}

	// The 24h rollup answers "is someone hammering us, and from where?" without
	// reading every row: totals, distinct IPs, and failures grouped by country.
	// Rows older than the window must not count.
	old := db.AuditLog{
		User: "legacy", Action: "login_failed", Resource: "auth", IP: "203.0.113.9",
		Success: false, CreatedAt: time.Now().Add(-30 * time.Hour),
	}
	if err := s.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	w, withSummary := call("?page=1&pageSize=50")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	sum := withSummary.Data.Summary
	if sum.WindowHours != 24 {
		t.Fatalf("window_hours = %d, want 24", sum.WindowHours)
	}
	// Seeds: 2 successes (alice login, admin loopback) + alice logout,
	// 1 failure (mallory). The 30h-old failure is outside the window.
	if sum.Failed != 1 {
		t.Fatalf("failed = %d, want 1 (the 30h-old attempt must not count): %+v", sum.Failed, sum)
	}
	if sum.OK != 3 {
		t.Fatalf("ok = %d, want 3", sum.OK)
	}
	// The failed attempt came from the 美国 IP, so that region leads the list.
	if len(sum.ByRegion) == 0 || sum.ByRegion[0].Country != "美国" || sum.ByRegion[0].Failed != 1 {
		t.Fatalf("by_region = %+v, want 美国 first with 1 failure", sum.ByRegion)
	}
	var sawLocal, sawDE bool
	for _, r := range sum.ByRegion {
		switch r.Country {
		case "local":
			sawLocal = true
		case "德国":
			sawDE = true
			// The rollup is per city, not per country: the cached city must
			// travel with the bucket so the panel can show "德国 · 法兰克福".
			if r.City != "法兰克福" {
				t.Fatalf("德国 region city = %q, want 法兰克福", r.City)
			}
		}
	}
	if !sawLocal || !sawDE {
		t.Fatalf("by_region misses local/德国: %+v", sum.ByRegion)
	}
}

type loginEventsResponse struct {
	Data struct {
		Events  []loginEvent `json:"events"`
		Total   int64        `json:"total"`
		Summary struct {
			WindowHours int   `json:"window_hours"`
			Failed      int64 `json:"failed"`
			OK          int64 `json:"ok"`
			DistinctIPs int   `json:"distinct_ips"`
			NewIP       int64 `json:"new_ip"`
			ByRegion    []struct {
				Country string `json:"country"`
				City    string `json:"city"`
				Failed  int64  `json:"failed"`
				OK      int64  `json:"ok"`
			} `json:"by_region"`
		} `json:"summary"`
	} `json:"data"`
}

// TestLoginSummaryCountsNewIPSignIns proves the anomalous-login signal: the
// 24h rollup counts first sign-ins from an address the account has never
// used, and only from the caller's own tenant.
func TestLoginSummaryCountsNewIPSignIns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	now := time.Now()
	for _, geo := range []db.LoginGeo{
		{IP: "203.0.113.20", Country: "德国", City: "法兰克福", ResolvedAt: now},
		{IP: "198.51.100.7", Country: "美国", ResolvedAt: now},
		{IP: "198.51.100.8", Country: "中国", ResolvedAt: now},
	} {
		if err := s.db.Create(&geo).Error; err != nil {
			t.Fatalf("seed geo %s: %v", geo.IP, err)
		}
	}
	rows := []db.AuditLog{
		{User: "alice", Action: "login_new_ip", Resource: "auth", IP: "203.0.113.20", TenantID: 2, Success: true, Details: "First login from 203.0.113.20", CreatedAt: now.Add(-2 * time.Hour)},
		{User: "alice", Action: "login", Resource: "auth", IP: "203.0.113.20", TenantID: 2, Success: true, CreatedAt: now.Add(-time.Hour)},
		// Outside the 24h window: the rollup must not count it.
		{User: "old", Action: "login_new_ip", Resource: "auth", IP: "198.51.100.7", TenantID: 2, Success: true, CreatedAt: now.Add(-40 * time.Hour)},
		// Another tenant: never visible.
		{User: "tenant1", Action: "login_new_ip", Resource: "auth", IP: "198.51.100.8", TenantID: 1, Success: true, CreatedAt: now.Add(-time.Hour)},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "tenant2-admin", 2)
	s.handleGetLoginEvents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var body loginEventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if got := body.Data.Summary.NewIP; got != 1 {
		t.Fatalf("summary new_ip = %d, want 1 (the recent, own-tenant sign-in)", got)
	}
}
