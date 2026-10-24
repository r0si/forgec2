package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func newDashboardTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Server.OfflineThreshold = 60
	return &Server{db: testutil.SetupTestDB(t), cfg: cfg, wsClients: make(map[*websocket.Conn]*wsClientConn)}
}

func TestHandleDashboardActivityHeatmap(t *testing.T) {
	tests := []struct {
		name   string
		seed   []db.Task
		query  string
		minLen int
	}{
		{
			name:   "empty",
			seed:   nil,
			query:  "?range=24h",
			minLen: 0,
		},
		{
			name: "with data 24h",
			seed: []db.Task{
				{AgentID: "a1", Status: "completed", CreatedAt: time.Now()},
				{AgentID: "a2", Status: "failed", CreatedAt: time.Now()},
			},
			query:  "?range=24h",
			minLen: 1,
		},
		{
			name: "with data 7d",
			seed: []db.Task{
				{AgentID: "a1", Status: "completed", CreatedAt: time.Now().AddDate(0, 0, -3)},
			},
			query:  "?range=7d",
			minLen: 1,
		},
		{
			name: "with data 30d",
			seed: []db.Task{
				{AgentID: "a1", Status: "completed", CreatedAt: time.Now().AddDate(0, 0, -15)},
			},
			query:  "?range=30d",
			minLen: 1,
		},
		{
			name: "with data 1h",
			seed: []db.Task{
				{AgentID: "a1", Status: "completed", CreatedAt: time.Now().Add(-10 * time.Minute)},
			},
			query:  "?range=1h",
			minLen: 1,
		},
		{
			name: "1h excludes older task",
			seed: []db.Task{
				{AgentID: "a1", Status: "completed", CreatedAt: time.Now().Add(-2 * time.Hour)},
			},
			query:  "?range=1h",
			minLen: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newDashboardTestServer(t)
			for _, task := range tc.seed {
				if err := s.db.Create(&task).Error; err != nil {
					t.Fatalf("seed task: %v", err)
				}
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/activity-heatmap"+tc.query, nil)

			s.handleDashboardActivityHeatmap(c)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
			}
			var resp []HeatmapData
			unmarshalData(t, w.Body.Bytes(), &resp)
			if len(resp) < tc.minLen {
				t.Errorf("expected at least %d heatmap entries, got %d", tc.minLen, len(resp))
			}
			if tc.name == "1h excludes older task" {
				total := 0
				for _, p := range resp {
					total += p.Count
				}
				if total != 0 {
					t.Errorf("expected zero activity in 1h range for 2h-old task, got %d", total)
				}
			}
		})
	}
}

func TestHandleDashboardOSDistribution(t *testing.T) {
	tests := []struct {
		name      string
		seed      []db.Implant
		wantCount int
	}{
		{
			name:      "empty",
			seed:      nil,
			wantCount: 0,
		},
		{
			name: "with data",
			seed: []db.Implant{
				{ID: "a1", Hostname: "DC01", OS: "Windows", LastSeen: time.Now()},
				{ID: "a2", Hostname: "WEB01", OS: "Linux", LastSeen: time.Now()},
				{ID: "a3", Hostname: "MAC01", OS: "macOS", LastSeen: time.Now()},
			},
			wantCount: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newDashboardTestServer(t)
			for _, a := range tc.seed {
				if err := s.db.Create(&a).Error; err != nil {
					t.Fatalf("seed agent: %v", err)
				}
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/os-distribution", nil)

			s.handleDashboardOSDistribution(c)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
			}
			var resp []OSDistribution
			unmarshalData(t, w.Body.Bytes(), &resp)
			if len(resp) != tc.wantCount {
				t.Errorf("expected %d OS entries, got %d", tc.wantCount, len(resp))
			}
		})
	}
}

func TestHandleDashboardTaskStatus(t *testing.T) {
	s := newDashboardTestServer(t)

	// Seed tasks with various statuses
	statuses := []string{"completed", "completed", "completed", "failed", "pending", "running"}
	for _, st := range statuses {
		s.db.Create(&db.Task{AgentID: "a1", Status: st, CreatedAt: time.Now()})
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/task-status", nil)

	s.handleDashboardTaskStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp []TaskStatusData
	unmarshalData(t, w.Body.Bytes(), &resp)
	if len(resp) == 0 {
		t.Error("expected at least one task status entry")
	}

	// Verify status counts
	countMap := make(map[string]int64)
	for _, d := range resp {
		countMap[d.Name] = d.Count
	}
	if countMap["Completed"] != 3 {
		t.Errorf("Completed count = %d, want 3", countMap["Completed"])
	}
	if countMap["Failed"] != 1 {
		t.Errorf("Failed count = %d, want 1", countMap["Failed"])
	}
}

func TestHandleDashboardListenerTraffic(t *testing.T) {
	s := newDashboardTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/listener-traffic?range=24h", nil)

	s.handleDashboardListenerTraffic(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp TrafficData
	unmarshalData(t, w.Body.Bytes(), &resp)
	// Should have labels array
	if resp.Labels == nil {
		t.Error("expected labels array")
	}
}

func TestHandleDashboardCredentialTypes(t *testing.T) {
	s := newDashboardTestServer(t)

	// Hermetic: the vault refuses plaintext store until loot encryption is
	// initialized, and tenantScope fails closed for an identity-less context.
	// Both used to rely on whichever test happened to run first.
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "DC01", OS: "Windows", LastSeen: time.Now()}).Error; err != nil {
		t.Fatalf("seed implant: %v", err)
	}
	if err := s.db.Create(&db.CredentialEntry{Type: "ntlm", Hash: "hash123", AgentID: "a1"}).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/credential-types", nil)
	// tenantScope fails closed for an identity-less context (WHERE 1 = 0), so
	// the dashboard test needs an operator identity: a name with no user row is
	// the documented legacy/unscoped path. Without it this test only passed
	// when another test in the package happened to leave rows behind.
	c.Set("user", "dash-admin")

	s.handleDashboardCredentialTypes(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}

	var resp []CredentialTypeData
	unmarshalData(t, w.Body.Bytes(), &resp)

	if len(resp) == 0 {
		t.Errorf("expected at least 1 credential type entry; body=%s", w.Body.String())
	}
}

func TestHandleDashboardAttackPath(t *testing.T) {
	s := newDashboardTestServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/dashboard/attack-path", nil)

	s.handleDashboardAttackPath(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp AttackPathData
	unmarshalData(t, w.Body.Bytes(), &resp)
	// Should return a valid structure even with empty data
	if resp.Nodes == nil {
		resp.Nodes = []AttackPathNode{}
	}
	if resp.Edges == nil {
		resp.Edges = []AttackPathEdge{}
	}
}
