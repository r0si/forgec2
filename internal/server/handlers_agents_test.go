package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func newAgentTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Server.OfflineThreshold = 60
	return &Server{db: testutil.SetupTestDB(t), cfg: cfg, wsClients: make(map[*websocket.Conn]*wsClientConn)}
}

func TestHandleListAgents(t *testing.T) {
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
				{ID: "a1", Hostname: "DC01", IP: "10.0.0.1", OS: "Windows", LastSeen: time.Now()},
				{ID: "a2", Hostname: "WEB01", IP: "10.0.0.2", OS: "Linux", LastSeen: time.Now()},
			},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newAgentTestServer(t)
			for _, a := range tc.seed {
				if err := s.db.Create(&a).Error; err != nil {
					t.Fatalf("seed agent: %v", err)
				}
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents", nil)

			s.handleListAgents(c)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Agents []struct {
					ID       string `json:"id"`
					Hostname string `json:"hostname"`
					IP       string `json:"ip"`
					Status   string `json:"status"`
					OS       string `json:"os"`
				} `json:"agents"`
			}
			unmarshalData(t, w.Body.Bytes(), &resp)
			if len(resp.Agents) != tc.wantCount {
				t.Fatalf("expected %d agents, got %d", tc.wantCount, len(resp.Agents))
			}
		})
	}
}

func TestImplantHostKey(t *testing.T) {
	if got := implantHostKey(db.Implant{Hostname: " DC01 ", IP: "10.0.0.1"}); got != "h:dc01" {
		t.Fatalf("hostname key: %q", got)
	}
	if got := implantHostKey(db.Implant{IP: "10.0.0.2"}); got != "ip:10.0.0.2" {
		t.Fatalf("ip key: %q", got)
	}
	if got := implantHostKey(db.Implant{PublicIP: "1.2.3.4", ID: "x"}); got != "ip:1.2.3.4" {
		t.Fatalf("public ip key: %q", got)
	}
	if got := implantHostKey(db.Implant{ID: "sess-1"}); got != "id:sess-1" {
		t.Fatalf("id key: %q", got)
	}
}

func TestHandleListAgentsGroupByHost(t *testing.T) {
	s := newAgentTestServer(t)
	now := time.Now()
	seed := []db.Implant{
		{ID: "s1", Hostname: "BOX", Username: "a", IP: "10.0.0.1", LastSeen: now.Add(-2 * time.Minute)},
		{ID: "s2", Hostname: "box", Username: "b", IP: "10.0.0.1", LastSeen: now.Add(-1 * time.Minute)},
		{ID: "s3", Hostname: "OTHER", Username: "c", IP: "10.0.0.9", LastSeen: now},
	}
	for _, a := range seed {
		if err := s.db.Create(&a).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents?group=host&page=1&page_size=1&sort_key=last_seen&sort_dir=desc", nil)
	s.handleListAgents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Agents []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"agents"`
		Total int    `json:"total"`
		Group string `json:"group"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Group != "host" {
		t.Fatalf("expected group=host, got %q", resp.Group)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 hosts, got %d", resp.Total)
	}
	if len(resp.Agents) != 1 {
		t.Fatalf("page_size=1 should return one host; got %d agents %v", len(resp.Agents), resp.Agents)
	}
	if resp.Agents[0].ID != "s3" {
		t.Fatalf("newest host should be OTHER, got %+v", resp.Agents)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request, _ = http.NewRequest(http.MethodGet, "/api/agents?group=host&page=2&page_size=1", nil)
	s.handleListAgents(c2)
	var page2 struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
		Total int `json:"total"`
	}
	unmarshalData(t, w2.Body.Bytes(), &page2)
	if page2.Total != 2 || len(page2.Agents) != 2 {
		t.Fatalf("page 2 should return both BOX sessions, got total=%d n=%d ids=%v", page2.Total, len(page2.Agents), page2.Agents)
	}
	got := map[string]bool{}
	for _, a := range page2.Agents {
		got[a.ID] = true
	}
	if !got["s1"] || !got["s2"] {
		t.Fatalf("expected s1+s2 on host page 2, got %v", page2.Agents)
	}
}

func TestAgentListLinkedFilter(t *testing.T) {
	s := newAgentTestServer(t)
	seed := []db.Implant{
		{ID: "direct1", Hostname: "SOLO", IP: "10.0.0.1", LastSeen: time.Now()},
		{ID: "direct2", Hostname: "STANDALONE", IP: "10.0.0.2", LastSeen: time.Now()},
		{ID: "child1", Hostname: "CHILD", IP: "10.0.0.3", ParentID: "parent1", LastSeen: time.Now()},
	}
	for _, a := range seed {
		if err := s.db.Create(&a).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	list := func(q string) []string {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents?"+q, nil)
		s.handleListAgents(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Agents []struct {
				ID string `json:"id"`
			} `json:"agents"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		ids := make([]string, 0, len(resp.Agents))
		for _, a := range resp.Agents {
			ids = append(ids, a.ID)
		}
		return ids
	}

	got := list("linked=direct&page=1&page_size=10")
	if len(got) != 2 || !slicesContains(got, "direct1") || !slicesContains(got, "direct2") || slicesContains(got, "child1") {
		t.Fatalf("linked=direct should return unparented agents only, got %v", got)
	}

	got = list("linked=chained&page=1&page_size=10")
	if len(got) != 1 || got[0] != "child1" {
		t.Fatalf("linked=chained should return parented agents only, got %v", got)
	}
}

func slicesContains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestAgentListEnvelopeShared(t *testing.T) {
	s := newAgentTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "H", IP: "10.0.0.1", LastSeen: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	hit := func(fn func(*gin.Context)) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req, _ := http.NewRequest(http.MethodGet, "/api/agents?page=1&page_size=10", nil)
		c.Request = req
		c.Set("user_role", "operator")
		fn(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body=%s", w.Code, w.Body.String())
		}
		var m map[string]any
		unmarshalData(t, w.Body.Bytes(), &m)
		return m
	}

	a := hit(s.handleListAgents)
	b := hit(s.apiListAgents)
	for _, m := range []map[string]any{a, b} {
		if _, ok := m["agents"].([]any); !ok {
			t.Fatalf("missing agents[]: %v", m)
		}
		if _, ok := m["data"].([]any); !ok {
			t.Fatalf("missing data[]: %v", m)
		}
	}
}

func TestHandleAgentDetail(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		s := newAgentTestServer(t)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/agents/nonexistent", nil)
		c.Params = gin.Params{{Key: "id", Value: "nonexistent"}}

		s.handleAgentDetail(c)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("found", func(t *testing.T) {
		s := newAgentTestServer(t)
		agent := db.Implant{
			ID:       "agent-found",
			Hostname: "WORKSTATION01",
			IP:       "10.0.0.5",
			OS:       "Windows",
			Username: "admin",
			LastSeen: time.Now(),
		}
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/agents/agent-found", nil)
		c.Params = gin.Params{{Key: "id", Value: "agent-found"}}

		s.handleAgentDetail(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		unmarshalData(t, w.Body.Bytes(), &resp)
		if _, ok := resp["Agent"]; !ok {
			t.Fatalf("expected 'Agent' key in response; keys=%v", keys(resp))
		}
	})

	t.Run("include_unlinked=false skips unlinked agents payload", func(t *testing.T) {
		s := newAgentTestServer(t)
		agent := db.Implant{ID: "agent-unlinked-skip", Hostname: "SKIP01", LastSeen: time.Now()}
		other := db.Implant{ID: "agent-unlinked-other", Hostname: "OTHER01", LastSeen: time.Now()}
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
		if err := s.db.Create(&other).Error; err != nil {
			t.Fatalf("seed other agent: %v", err)
		}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/agents/agent-unlinked-skip?format=json&include_unlinked=false", nil)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		s.handleAgentDetail(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		unmarshalData(t, w.Body.Bytes(), &resp)
		if _, exists := resp["UnlinkedAgents"]; exists {
			t.Fatalf("expected UnlinkedAgents to be omitted with include_unlinked=false; keys=%v", keys(resp))
		}
	})

	t.Run("include_unlinked default keeps unlinked agents payload", func(t *testing.T) {
		s := newAgentTestServer(t)
		agent := db.Implant{ID: "agent-unlinked-keep", Hostname: "KEEP01", LastSeen: time.Now()}
		other := db.Implant{ID: "agent-unlinked-other2", Hostname: "OTHER02", LastSeen: time.Now()}
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
		if err := s.db.Create(&other).Error; err != nil {
			t.Fatalf("seed other agent: %v", err)
		}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/agents/agent-unlinked-keep?format=json", nil)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		s.handleAgentDetail(c)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		unmarshalData(t, w.Body.Bytes(), &resp)
		unlinked, ok := resp["UnlinkedAgents"].([]any)
		if !ok || len(unlinked) != 1 {
			t.Fatalf("expected 1 unlinked agent with default include; got %v", resp["UnlinkedAgents"])
		}
	})

	t.Run("uses all task history for stats", func(t *testing.T) {
		s := newAgentTestServer(t)
		agent := db.Implant{ID: "agent-stats", Hostname: "STATS01", LastSeen: time.Now()}
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
		for i := 0; i < AgentDetailTaskLimit+1; i++ {
			if err := s.db.Create(&db.Task{AgentID: agent.ID, Type: "shell", Status: "completed"}).Error; err != nil {
				t.Fatalf("seed completed task: %v", err)
			}
		}
		if err := s.db.Create(&db.Task{AgentID: agent.ID, Type: "shell", Status: "failed"}).Error; err != nil {
			t.Fatalf("seed failed task: %v", err)
		}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/agents/agent-stats?format=json", nil)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		s.handleAgentDetail(c)

		var resp struct {
			TotalTasks     int `json:"TotalTasks"`
			CompletedTasks int `json:"CompletedTasks"`
			FailedTasks    int `json:"FailedTasks"`
			SuccessRate    int `json:"SuccessRate"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.TotalTasks != AgentDetailTaskLimit+2 || resp.CompletedTasks != AgentDetailTaskLimit+1 || resp.FailedTasks != 1 || resp.SuccessRate != 98 {
			t.Fatalf("unexpected aggregate stats: %+v", resp)
		}
	})
}

func TestHandleListAgentScreenshots(t *testing.T) {
	s := newAgentTestServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	agent := db.Implant{ID: "agent-screenshots", Hostname: "SCREEN01", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	dir := filepath.Join(s.cfg.Server.DataDir, "screenshots", agent.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create screenshots directory: %v", err)
	}
	for _, filename := range []string{"screenshot_100.png", "screenshot_300.png", "screenshot_200.png", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte("test"), 0o600); err != nil {
			t.Fatalf("write screenshot fixture: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents/agent-screenshots/screenshots?page=1&page_size=2", nil)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	s.handleListAgentScreenshots(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Screenshots []string `json:"screenshots"`
		Total       int      `json:"total"`
		Page        int      `json:"page"`
		PageSize    int      `json:"page_size"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Total != 3 || resp.Page != 1 || resp.PageSize != 2 {
		t.Fatalf("unexpected response metadata: %+v", resp)
	}
	if got, want := resp.Screenshots, []string{"screenshot_300.png", "screenshot_200.png"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("screenshots = %v, want %v", got, want)
	}
}

func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
