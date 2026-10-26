package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// listQueryCtx builds a context with query parameters for implantListQuery.
func listQueryCtx(query map[string]string) *gin.Context {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequestWithQuery(query)
	return c
}

func newRequestWithQuery(query map[string]string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "/agents", nil)
	q := req.URL.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	return req
}

// TestImplantListQueryFilters pins every filter the agents page can apply and,
// above all, that none of them can widen the query past the caller's tenant.
func TestImplantListQueryFilters(t *testing.T) {
	s := mustTenantServer(t)
	// Pin the buckets so the test does not depend on the default thresholds:
	// online is "seen within 60s", stale is "60s..300s", offline is older.
	s.cfg.Server.OfflineThreshold = 60 // stale = 60s * StaleThresholdMultiplier
	now := time.Now()
	rows := []db.Implant{
		{ID: "online-t1", TenantID: 1, Hostname: "DC01", Username: "admin", IP: "10.0.0.5", OS: "windows", Status: "online", LastSeen: now},
		{ID: "stale-t1", TenantID: 1, Hostname: "web01", Username: "www", IP: "10.0.0.6", OS: "linux", Status: "online", LastSeen: now.Add(-2 * time.Minute)},
		{ID: "offline-t1", TenantID: 1, Hostname: "old01", Username: "svc", IP: "10.0.0.7", OS: "linux", Status: "offline", LastSeen: now.Add(-96 * time.Hour)},
		{ID: "online-t2", TenantID: 2, Hostname: "DC01", Username: "admin", IP: "10.9.9.9", OS: "windows", Status: "online", LastSeen: now},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	// A chained agent for the linked filter.
	chained := db.Implant{ID: "chained-t1", TenantID: 1, Hostname: "child", OS: "windows", Status: "online", ParentID: "online-t1", LastSeen: now}
	if err := s.db.Create(&chained).Error; err != nil {
		t.Fatal(err)
	}
	// A user in tenant 1 so tenantScope resolves to 1.
	seedAIUser(t, s, "admin1", 1)

	run := func(query map[string]string) []string {
		c := listQueryCtx(query)
		c.Set("user", "admin1")
		c.Set("user_role", db.RoleAdmin)
		var out []db.Implant
		if err := s.implantListQuery(c).Find(&out).Error; err != nil {
			t.Fatalf("query %v: %v", query, err)
		}
		ids := make([]string, 0, len(out))
		for _, a := range out {
			ids = append(ids, a.ID)
		}
		return ids
	}

	contains := func(ids []string, want string) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	// No filter: everything in the caller's tenant, nothing from tenant 2.
	all := run(nil)
	if contains(all, "online-t2") {
		t.Fatalf("unfiltered list leaked a tenant-2 agent: %v", all)
	}
	if len(all) != 4 {
		t.Fatalf("unfiltered = %v, want the 4 tenant-1 agents", all)
	}

	// Status buckets.
	if ids := run(map[string]string{"status": "online"}); !contains(ids, "online-t1") || contains(ids, "offline-t1") {
		t.Fatalf("online filter = %v", ids)
	}
	if ids := run(map[string]string{"status": "stale"}); !contains(ids, "stale-t1") || contains(ids, "online-t1") {
		t.Fatalf("stale filter = %v", ids)
	}
	if ids := run(map[string]string{"status": "offline"}); !contains(ids, "offline-t1") || contains(ids, "online-t1") {
		t.Fatalf("offline filter = %v", ids)
	}

	// Search matches hostname, username or IP — but only within the tenant.
	if ids := run(map[string]string{"search": "DC01"}); len(ids) != 1 || ids[0] != "online-t1" {
		t.Fatalf("search by hostname = %v", ids)
	}
	if ids := run(map[string]string{"search": "www"}); len(ids) != 1 || ids[0] != "stale-t1" {
		t.Fatalf("search by username = %v", ids)
	}
	if ids := run(map[string]string{"search": "10.0.0"}); len(ids) != 3 {
		t.Fatalf("search by ip = %v", ids)
	}

	// A LIKE wildcard in the search is escaped rather than matching everything.
	if ids := run(map[string]string{"search": "%"}); len(ids) != 0 {
		t.Fatalf("a literal %% matched %v, want nothing", ids)
	}

	// OS filter is case-insensitive.
	if ids := run(map[string]string{"os": "LINUX"}); len(ids) != 2 || contains(ids, "online-t1") {
		t.Fatalf("os filter = %v", ids)
	}

	// Linked filters split direct from chained.
	if ids := run(map[string]string{"linked": "chained"}); len(ids) != 1 || ids[0] != "chained-t1" {
		t.Fatalf("chained filter = %v", ids)
	}
	if ids := run(map[string]string{"linked": "direct"}); contains(ids, "chained-t1") || !contains(ids, "online-t1") {
		t.Fatalf("direct filter = %v", ids)
	}
}
