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

	signedInAt := time.Date(2026, 11, 9, 8, 30, 0, 0, time.UTC)
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
}

type loginEventsResponse struct {
	Data struct {
		Events []loginEvent `json:"events"`
		Total  int64        `json:"total"`
	} `json:"data"`
}
