package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestProcessTreeEnvelope(t *testing.T) {
	queued := time.Date(2026, 10, 21, 12, 34, 56, 0, time.UTC)
	finished := queued.Add(90 * time.Second)
	env := processTreeEnvelope(db.Task{
		ID:        7,
		Type:      "process_tree",
		Result:    "1 0 systemd",
		CreatedAt: queued,
		UpdatedAt: finished,
	})
	if env["live"] != false {
		t.Fatalf("live=%v, want false", env["live"])
	}
	if env["kind"] != "process_tree" || env["source"] != "process_tree" {
		t.Fatalf("envelope=%#v", env)
	}
	if env["task_id"] != uint(7) || env["collected_at"] != "2026-10-21T12:36:26Z" {
		t.Fatalf("metadata envelope=%#v", env)
	}
	legacy := processTreeEnvelope(db.Task{
		Type:      "process_tree",
		Result:    "1 0 systemd",
		CreatedAt: queued,
	})
	if legacy["collected_at"] != "2026-10-21T12:34:56Z" {
		t.Fatalf("created_at fallback envelope=%#v", legacy)
	}
	ps := processTreeEnvelope(db.Task{Type: "ps", Result: "svchost.exe"})
	if ps["kind"] != "last_ps_snapshot" || ps["source"] != "ps" {
		t.Fatalf("ps fallback envelope=%#v", ps)
	}
}

func TestHandleGetProcessTreePrefersTreeSnapshot(t *testing.T) {
	s := newAgentTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "box"}).Error; err != nil {
		t.Fatalf("seed implant: %v", err)
	}
	if err := s.db.Create(&db.Task{AgentID: "a1", Type: "ps", Status: "completed", Result: "svchost.exe"}).Error; err != nil {
		t.Fatalf("seed ps: %v", err)
	}
	if err := s.db.Create(&db.Task{AgentID: "a1", Type: "process_tree", Status: "completed", Result: "1\t0\tsystem\tsystemd"}).Error; err != nil {
		t.Fatalf("seed tree: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents/a1/process-tree", nil)

	s.handleGetProcessTree(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Processes   string `json:"processes"`
		Source      string `json:"source"`
		Kind        string `json:"kind"`
		Live        bool   `json:"live"`
		TaskID      uint   `json:"task_id"`
		CollectedAt string `json:"collected_at"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Live {
		t.Fatal("handler claimed a live process tree")
	}
	if resp.Kind != "process_tree" || resp.Source != "process_tree" {
		t.Fatalf("envelope: %+v", resp)
	}
	if resp.TaskID == 0 || resp.CollectedAt == "" {
		t.Fatalf("missing snapshot metadata: %+v", resp)
	}
	if resp.Processes != "1\t0\tsystem\tsystemd" {
		t.Fatalf("processes=%q", resp.Processes)
	}
}

func TestHandleGetProcessTreeCrossTenant404(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "other-tenant-proc", 2)
	if err := s.db.Create(&db.Task{
		AgentID:  "other-tenant-proc",
		TenantID: 2,
		Type:     "process_tree",
		Status:   "completed",
		Result:   "PID\tPPID\tUSER\tNAME\n1\t0\tsecret\tlsass.exe",
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	c, w := tenantScopedAdminContext(s, t, "g1-admin", 1)
	c.Params = gin.Params{{Key: "id", Value: "other-tenant-proc"}}
	s.handleGetProcessTree(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "lsass.exe") {
		t.Fatalf("cross-tenant process output leaked: %s", w.Body.String())
	}
}

func TestHandleGetProcessTreeFallsBackToPS(t *testing.T) {
	s := newAgentTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "box"}).Error; err != nil {
		t.Fatalf("seed implant: %v", err)
	}
	if err := s.db.Create(&db.Task{AgentID: "a1", Type: "ps", Status: "completed", Result: "svchost.exe"}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents/a1/process-tree", nil)

	s.handleGetProcessTree(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Kind != "last_ps_snapshot" || resp.Source != "ps" {
		t.Fatalf("envelope: %+v", resp)
	}
}

func TestHandleGetProcessTreeRequiresCompletedSnapshot(t *testing.T) {
	s := newAgentTestServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents/missing/process-tree", nil)

	s.handleGetProcessTree(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d; body=%s", w.Code, w.Body.String())
	}
}
