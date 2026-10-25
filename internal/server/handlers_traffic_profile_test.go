package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

func newTrafficProfileTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return &Server{
		db:                testutil.SetupTestDB(t),
		trafficLog:        newTrafficRing(),
		autoAdaptLast:     make(map[string]time.Time),
		agentPendingTasks: make(map[string]int),
		metrics:           &MetricsCollector{TasksTotal: prometheus.NewCounter(prometheus.CounterOpts{})},
	}
}

func newTrafficRequest(method, path, agentID, body string) (*httptest.ResponseRecorder, *gin.Context) {
	w, c := newJSONRequest(method, path, body)
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	return w, c
}

func seedTrafficAgent(t *testing.T, s *Server, id string, interval, jitter int) db.Implant {
	t.Helper()
	agent := db.Implant{ID: id, Hostname: "host-" + id, CurrentInterval: interval, CurrentJitter: jitter}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
	return agent
}

// seedTrafficLog adds n beacons spaced `step` seconds apart for the agent.
func seedTrafficLog(s *Server, agentID string, n int, step time.Duration) {
	base := time.Now().Add(-time.Duration(n) * step)
	for i := 0; i < n; i++ {
		s.trafficLog.add(TrafficEntry{
			Time:    base.Add(time.Duration(i) * step),
			AgentID: agentID,
			Size:    1000 + i*10,
		})
	}
}

func TestHandleTrafficProfileGet(t *testing.T) {
	s := newTrafficProfileTestServer(t)
	seedTrafficAgent(t, s, "agent-1", 5, 0)

	t.Run("unknown agent returns 404", func(t *testing.T) {
		w, c := newTrafficRequest(http.MethodGet, "/api/agents/ghost/traffic-profile", "ghost", "")
		s.handleTrafficProfileGet(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})

	t.Run("auto_adapt reflects persisted value", func(t *testing.T) {
		if err := s.db.Model(&db.Implant{}).Where("id = ?", "agent-1").Update("auto_adapt", true).Error; err != nil {
			t.Fatalf("update: %v", err)
		}
		w, c := newTrafficRequest(http.MethodGet, "/api/agents/agent-1/traffic-profile", "agent-1", "")
		s.handleTrafficProfileGet(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var resp struct {
			Data struct {
				AutoAdapt bool `json:"auto_adapt"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if !resp.Data.AutoAdapt {
			t.Error("expected auto_adapt=true from persisted value")
		}
	})

	t.Run("beacon samples produce baseline stats and suggestion", func(t *testing.T) {
		seedTrafficLog(s, "agent-1", 5, 5*time.Second)
		w, c := newTrafficRequest(http.MethodGet, "/api/agents/agent-1/traffic-profile", "agent-1", "")
		s.handleTrafficProfileGet(c)
		var resp struct {
			Data struct {
				SampleCount      int                    `json:"sample_count"`
				BaselineInterval int                    `json:"baseline_interval"`
				RecentRecords    []TrafficEntry         `json:"recent_records"`
				Suggestion       map[string]interface{} `json:"suggestion"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if resp.Data.SampleCount != 5 {
			t.Errorf("sample_count = %d, want 5", resp.Data.SampleCount)
		}
		if resp.Data.BaselineInterval != 5 {
			t.Errorf("baseline_interval = %d, want 5", resp.Data.BaselineInterval)
		}
		if resp.Data.Suggestion == nil {
			t.Error("expected non-nil suggestion with enough samples")
		}
		if len(resp.Data.RecentRecords) != 5 {
			t.Errorf("recent_records = %d, want 5", len(resp.Data.RecentRecords))
		}
	})
}

func TestHandleTrafficProfileAdapt(t *testing.T) {
	s := newTrafficProfileTestServer(t)

	t.Run("missing agent returns 404", func(t *testing.T) {
		w, c := newTrafficRequest(http.MethodPost, "/api/agents/ghost/traffic-profile/adapt", "ghost", `{}`)
		s.handleTrafficProfileAdapt(c)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("insufficient samples returns 400", func(t *testing.T) {
		seedTrafficAgent(t, s, "agent-1", 5, 0)
		w, c := newTrafficRequest(http.MethodPost, "/api/agents/agent-1/traffic-profile/adapt", "agent-1", `{}`)
		s.handleTrafficProfileAdapt(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("creates a real set_sleep task", func(t *testing.T) {
		seedTrafficLog(s, "agent-1", 5, 5*time.Second)
		w, c := newTrafficRequest(http.MethodPost, "/api/agents/agent-1/traffic-profile/adapt", "agent-1", `{}`)
		s.handleTrafficProfileAdapt(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			TaskID  uint   `json:"task_id"`
			Message string `json:"message"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if resp.TaskID == 0 {
			t.Fatalf("expected created task, got %+v", resp)
		}
		var task db.Task
		if err := s.db.First(&task, resp.TaskID).Error; err != nil {
			t.Fatalf("task not persisted: %v", err)
		}
		if task.Type != "set_sleep" || task.Status != "pending" {
			t.Errorf("unexpected task: type=%s status=%s", task.Type, task.Status)
		}
		// 5s interval, 0% measured jitter -> suggestion 6s, jitter 0
		if task.Command != "6,0" {
			t.Errorf("command = %q, want %q", task.Command, "6,0")
		}
	})

	t.Run("agent already matching suggestion creates no task", func(t *testing.T) {
		if err := s.db.Model(&db.Implant{}).Where("id = ?", "agent-1").Update("current_interval", 6).Error; err != nil {
			t.Fatalf("update: %v", err)
		}
		var before int64
		s.db.Model(&db.Task{}).Count(&before)
		w, c := newTrafficRequest(http.MethodPost, "/api/agents/agent-1/traffic-profile/adapt", "agent-1", `{}`)
		s.handleTrafficProfileAdapt(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var after int64
		s.db.Model(&db.Task{}).Count(&after)
		if after != before {
			t.Errorf("expected no new task when profile already matches, before=%d after=%d", before, after)
		}
	})
}

func TestHandleTrafficProfileAutoAdapt(t *testing.T) {
	s := newTrafficProfileTestServer(t)
	seedTrafficAgent(t, s, "agent-1", 5, 0)

	t.Run("missing agent returns 404", func(t *testing.T) {
		w, c := newTrafficRequest(http.MethodPost, "/api/agents/ghost/traffic-profile/auto-adapt", "ghost", `{"enabled":true}`)
		s.handleTrafficProfileAutoAdapt(c)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("toggle persists to database", func(t *testing.T) {
		w, c := newTrafficRequest(http.MethodPost, "/api/agents/agent-1/traffic-profile/auto-adapt", "agent-1", `{"enabled":true}`)
		s.handleTrafficProfileAutoAdapt(c)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			AutoAdapt bool `json:"auto_adapt"`
		}
		unmarshalData(t, w.Body.Bytes(), &resp)
		if !resp.AutoAdapt {
			t.Errorf("expected enabled, got %+v", resp)
		}
		var agent db.Implant
		if err := s.db.First(&agent, "id = ?", "agent-1").Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if !agent.AutoAdapt {
			t.Error("auto_adapt not persisted in database")
		}

		_, c = newTrafficRequest(http.MethodPost, "/api/agents/agent-1/traffic-profile/auto-adapt", "agent-1", `{"enabled":false}`)
		s.handleTrafficProfileAutoAdapt(c)
		if err := s.db.First(&agent, "id = ?", "agent-1").Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if agent.AutoAdapt {
			t.Error("auto_adapt should be disabled after toggle off")
		}
	})
}

func TestMaybeAutoAdaptBeacon(t *testing.T) {
	s := newTrafficProfileTestServer(t)
	seedTrafficAgent(t, s, "agent-1", 5, 0)

	t.Run("disabled agents never get tasks", func(t *testing.T) {
		s.maybeAutoAdaptBeacon(seedTrafficAgent(t, s, "agent-off", 5, 0))
		var count int64
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-off").Count(&count)
		if count != 0 {
			t.Errorf("expected no task for disabled agent, got %d", count)
		}
	})

	agent := seedTrafficAgent(t, s, "agent-adapt", 5, 0)
	agent.AutoAdapt = true

	t.Run("no samples -> no task", func(t *testing.T) {
		s.maybeAutoAdaptBeacon(agent)
		var count int64
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-adapt").Count(&count)
		if count != 0 {
			t.Errorf("expected no task without samples, got %d", count)
		}
	})

	t.Run("deviation queues a real set_sleep task", func(t *testing.T) {
		seedTrafficLog(s, "agent-adapt", 5, 5*time.Second)
		s.maybeAutoAdaptBeacon(agent)
		var task db.Task
		if err := s.db.Where("agent_id = ? AND type = ?", "agent-adapt", "set_sleep").First(&task).Error; err != nil {
			t.Fatalf("expected set_sleep task, got %v", err)
		}
		if task.Command != "6,0" {
			t.Errorf("command = %q, want %q", task.Command, "6,0")
		}
	})

	t.Run("rate limit prevents immediate re-trigger", func(t *testing.T) {
		var before int64
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-adapt").Count(&before)
		s.maybeAutoAdaptBeacon(agent)
		var after int64
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-adapt").Count(&after)
		if after != before {
			t.Errorf("expected rate limit to block second task, before=%d after=%d", before, after)
		}
	})

	t.Run("pending set_sleep blocks a new one", func(t *testing.T) {
		agent2 := seedTrafficAgent(t, s, "agent-pending", 5, 0)
		agent2.AutoAdapt = true
		seedTrafficLog(s, "agent-pending", 5, 5*time.Second)
		s.maybeAutoAdaptBeacon(agent2)
		var count int64
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-pending").Count(&count)
		if count != 1 {
			t.Fatalf("expected exactly 1 task, got %d", count)
		}
		// Clear the rate-limit timestamp so the second call actually reaches
		// the pending-task guard instead of short-circuiting on rate limit.
		s.autoAdaptMu.Lock()
		delete(s.autoAdaptLast, "agent-pending")
		s.autoAdaptMu.Unlock()
		s.maybeAutoAdaptBeacon(agent2)
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-pending").Count(&count)
		if count != 1 {
			t.Errorf("pending task should block new auto-adapt, got %d tasks", count)
		}
	})

	t.Run("matching profile does not queue a task", func(t *testing.T) {
		agent3 := seedTrafficAgent(t, s, "agent-match", 6, 0)
		agent3.AutoAdapt = true
		seedTrafficLog(s, "agent-match", 5, 5*time.Second)
		s.maybeAutoAdaptBeacon(agent3)
		var count int64
		s.db.Model(&db.Task{}).Where("agent_id = ?", "agent-match").Count(&count)
		if count != 0 {
			t.Errorf("expected no task when profile matches, got %d", count)
		}
	})
}
