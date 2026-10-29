package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAgentFleetCSVExport covers ?format=csv on the fleet list.
//
// "Who was in scope" is the most-requested artifact of a review, and the per-agent export packages one host - a different
// document from the inventory. The list is paged, so the file must say when it was cut.
func TestAgentFleetCSVExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	for _, agent := range []db.Implant{
		{ID: "a-1", TenantID: 1, Hostname: "web-01", IP: "10.0.0.5", OS: "windows", Username: "svc",
			Status: "online", LastSeen: now, EngagementID: &engagement.ID},
		{ID: "a-2", TenantID: 1, Hostname: "db-02", IP: "10.0.0.6", OS: "linux", Status: "offline", LastSeen: now},
	} {
		agent := agent
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/agents?format=csv&page_size=100", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleListAgents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want a header and the two agents: %v", len(rows), rows)
	}
	if got := strings.Join(rows[0], ","); got != "ID,Hostname,IP,OS,Username,Status,Protocol,Last Seen,Engagement" {
		t.Fatalf("header = %q", got)
	}
	// The engagement column is what makes the file answer "who was in scope" rather than "who exists". Keyed by hostname
	// because the list has its own default ordering, and a positional assertion would fail the day that order changes.
	wantEngagement := strconv.FormatUint(uint64(engagement.ID), 10)
	engagementByHost := map[string]string{}
	for _, row := range rows[1:] {
		engagementByHost[row[1]] = row[8]
	}
	if engagementByHost["web-01"] != wantEngagement {
		t.Fatalf("engagement for web-01 = %q, want %q", engagementByHost["web-01"], wantEngagement)
	}
	if engagementByHost["db-02"] != "" {
		t.Fatalf("engagement for the unlinked agent = %q, want empty", engagementByHost["db-02"])
	}
	// Everything fits on one page, so nothing was cut.
	if got := w.Header().Get("X-Export-Truncated"); got != "" {
		t.Fatalf("X-Export-Truncated = %q, want it absent when the whole fleet fits", got)
	}
	if scope := w.Header().Get(exportScopeHeader); scope != "all" {
		t.Fatalf("scope header = %q, want all for an unscoped fleet", scope)
	}
}
