package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestHandleKillAgent(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "kill-me", 1)

	c, w := missingAPICtx(http.MethodPost, "/api/agents/x/kill", "",
		gin.Params{{Key: "id", Value: "kill-me"}})
	s.handleKillAgent(c)
	if w.Code != http.StatusOK {
		t.Fatalf("kill = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		TaskID  uint   `json:"task_id"`
		Message string `json:"message"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.TaskID == 0 {
		t.Fatalf("kill response = %+v", out)
	}
	var task db.Task
	if err := s.db.Where("id = ?", out.TaskID).First(&task).Error; err != nil {
		t.Fatalf("task not persisted: %v", err)
	}
	if task.Type != "kill" || task.AgentID != "kill-me" {
		t.Fatalf("task = %+v", task)
	}

	// An unknown agent is refused by the task issuer, not silently ignored.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/kill", "",
		gin.Params{{Key: "id", Value: "ghost"}})
	s.handleKillAgent(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent kill = %d %s", w.Code, w.Body.String())
	}
}

func TestHandleListUnlinkedAgents(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	// Two agents with no P2P parent, one linked, one from another tenant.
	if err := s.db.Create(&db.Implant{ID: "unlinked-a", TenantID: 1, Hostname: "zulu", ParentID: ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "unlinked-b", TenantID: 1, Hostname: "alpha"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "linked-a", TenantID: 1, Hostname: "linked", ParentID: "unlinked-b"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.Implant{ID: "unlinked-foreign", TenantID: 2, Hostname: "foreign"}).Error; err != nil {
		t.Fatal(err)
	}

	c, w := missingAPICtx(http.MethodGet, "/api/agents/unlinked", "", nil)
	s.handleListUnlinkedAgents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("unlinked = %d %s", w.Code, w.Body.String())
	}
	var agents []struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}
	unmarshalData(t, w.Body.Bytes(), &agents)
	if len(agents) != 2 {
		t.Fatalf("unlinked = %+v, want the 2 parentless tenant-1 agents", agents)
	}
	// Ordered by hostname and free of linked/foreign rows.
	if agents[0].Hostname != "alpha" || agents[1].Hostname != "zulu" {
		t.Fatalf("order = %+v", agents)
	}
	for _, a := range agents {
		if a.ID == "linked-a" || a.ID == "unlinked-foreign" {
			t.Fatalf("wrong agent in the list: %+v", a)
		}
	}
}

func TestHandleGetAgentConfig(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	s.cfg.Implant.DefaultInterval = 30
	s.cfg.Implant.DefaultJitter = 10
	s.cfg.Implant.DefaultUA = "default-agent"

	// An agent with its own values wins over the defaults.
	if err := s.db.Create(&db.Implant{ID: "cfg-agent", TenantID: 1, Hostname: "cfg",
		CurrentInterval: 90, CurrentJitter: 25}).Error; err != nil {
		t.Fatal(err)
	}
	c, w := missingAPICtx(http.MethodGet, "/api/agents/x/config", "",
		gin.Params{{Key: "id", Value: "cfg-agent"}})
	s.handleGetAgentConfig(c)
	if w.Code != http.StatusOK {
		t.Fatalf("config = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		AgentID   string `json:"agent_id"`
		Effective struct {
			Sleep     int    `json:"sleep"`
			Jitter    int    `json:"jitter"`
			UserAgent string `json:"user_agent"`
		} `json:"effective"`
		HasPending    bool `json:"has_pending"`
		PendingTaskID uint `json:"pending_task_id"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Effective.Sleep != 90 || out.Effective.Jitter != 25 {
		t.Fatalf("effective = %+v", out.Effective)
	}
	if out.Effective.UserAgent != "default-agent" {
		t.Fatalf("user agent = %q", out.Effective.UserAgent)
	}
	if out.HasPending {
		t.Fatal("pending reported without a config_push task")
	}

	// A queued config_push task is surfaced with its id.
	queued := db.Task{AgentID: "cfg-agent", Type: "config_push", Status: "pending"}
	if err := s.db.Create(&queued).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodGet, "/api/agents/x/config", "",
		gin.Params{{Key: "id", Value: "cfg-agent"}})
	s.handleGetAgentConfig(c)
	unmarshalData(t, w.Body.Bytes(), &out)
	if !out.HasPending || out.PendingTaskID != queued.ID {
		t.Fatalf("pending = %+v (want task %d)", out, queued.ID)
	}

	// An agent without its own values falls back to the config defaults.
	if err := s.db.Create(&db.Implant{ID: "cfg-default", TenantID: 1, Hostname: "def"}).Error; err != nil {
		t.Fatal(err)
	}
	c, w = missingAPICtx(http.MethodGet, "/api/agents/x/config", "",
		gin.Params{{Key: "id", Value: "cfg-default"}})
	s.handleGetAgentConfig(c)
	unmarshalData(t, w.Body.Bytes(), &out)
	if out.Effective.Sleep != 30 || out.Effective.Jitter != 10 {
		t.Fatalf("defaults = %+v", out.Effective)
	}

	// Unknown agent and cross-tenant agent both 404.
	c, w = missingAPICtx(http.MethodGet, "/api/agents/x/config", "",
		gin.Params{{Key: "id", Value: "ghost"}})
	s.handleGetAgentConfig(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent config = %d", w.Code)
	}
}

func TestHandleAgentsPageRenders(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "page-agent", 1)

	c, w := missingAPICtx(http.MethodGet, "/agents", "", nil)
	s.handleAgents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("agents page = %d %s", w.Code, w.Body.String())
	}
	body := strings.TrimSpace(w.Body.String())
	if body == "" {
		t.Fatal("agents page returned an empty body")
	}
	if !strings.Contains(body, "{") && !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("agents page is neither JSON nor HTML: %.80s", body)
	}
}
