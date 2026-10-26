package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestListenerDetailActiveCount pins the "active" figure on the listener detail
// payload: only agents seen within ListenerActiveThreshold count, and an agent
// from another tenant never does. A failed count now answers 500 instead of a
// reassuring zero (the page renders this number prominently).
func TestListenerDetailActiveCount(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)

	listener := db.Listener{Name: "l1", TenantID: 1, Scheme: "http", Type: "http", Host: "127.0.0.1", Port: 9100, Enabled: true, Status: "running"}
	if err := s.db.Create(&listener).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	agents := []db.Implant{
		{ID: "recent", TenantID: 1, Hostname: "recent", Status: "online", ListenerID: listener.ID, LastSeen: now},
		{ID: "old", TenantID: 1, Hostname: "old", Status: "offline", ListenerID: listener.ID, LastSeen: now.Add(-2 * ListenerActiveThreshold)},
		{ID: "foreign", TenantID: 2, Hostname: "foreign", Status: "online", ListenerID: listener.ID, LastSeen: now},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/listeners/detail", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Params = gin.Params{{Key: "id", Value: jsonNumber(listener.ID)}}

	s.handleListenerDetail(c)

	if w.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", w.Code, w.Body.String())
	}
	// The endpoint serves the page payload shape (PascalCase keys, documented
	// as a renderPageOrJSON exception in envelope_baseline.txt).
	var out struct {
		ActiveAgents int `json:"ActiveAgents"`
		Agents       []struct {
			ID string `json:"id"`
		} `json:"Agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if out.ActiveAgents != 1 {
		t.Fatalf("ActiveAgents = %d, want 1 (only the recently-seen tenant-1 agent)", out.ActiveAgents)
	}
	if len(out.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (recent + old, never the foreign one)", len(out.Agents))
	}
	for _, a := range out.Agents {
		if a.ID == "foreign" {
			t.Fatal("the detail payload listed another tenant's agent")
		}
	}
}
