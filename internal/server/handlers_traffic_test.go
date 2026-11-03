package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestTrafficMiddlewareCapturesRootBeaconRouteAndAuthenticatedAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{
		trafficLog:   newTrafficRing(),
		trafficBytes: newTrafficByteAccumulator(),
	}
	r := gin.New()
	r.Use(s.trafficMiddleware())
	r.POST("/collect", func(c *gin.Context) {
		c.Set(trafficAgentIDContextKey, "authenticated-agent")
		c.Data(http.StatusAccepted, "application/json", []byte(`{"ok":true}`))
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/collect", nil)
	// A forged header must not win over the identity set by the authenticated
	// beacon handler.
	req.Header.Set("X-Agent-ID", "forged-agent")
	r.ServeHTTP(w, req)

	logs := s.trafficLog.recent(10)
	if len(logs) != 1 {
		t.Fatalf("traffic rows = %d, want 1", len(logs))
	}
	if got := logs[0].Path; got != "/collect" {
		t.Fatalf("path = %q, want root profile route", got)
	}
	if got := logs[0].AgentID; got != "authenticated-agent" {
		t.Fatalf("agent id = %q, want authenticated identity", got)
	}
	if got := logs[0].Status; got != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", got, http.StatusAccepted)
	}
}

func TestTrafficMiddlewareFallsBackToLegacyAgentHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{
		trafficLog:   newTrafficRing(),
		trafficBytes: newTrafficByteAccumulator(),
	}
	r := gin.New()
	r.Use(s.trafficMiddleware())
	r.POST("/api/v1/beacon", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beacon", nil)
	req.Header.Set("X-Agent-ID", "legacy-agent")
	r.ServeHTTP(w, req)

	logs := s.trafficLog.recent(1)
	if len(logs) != 1 || logs[0].AgentID != "legacy-agent" {
		t.Fatalf("legacy header was not captured: %+v", logs)
	}
}

func TestHandleTrafficDataScopesThroughVisibleAgents(t *testing.T) {
	s := mustTenantServer(t)
	s.trafficLog = newTrafficRing()
	for _, agent := range []db.Implant{
		{ID: "own-agent", TenantID: 1, Hostname: "own"},
		{ID: "foreign-agent", TenantID: 2, Hostname: "foreign"},
	} {
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	for _, entry := range []TrafficEntry{
		{Path: "/own", AgentID: "own-agent", RemoteIP: "10.0.0.1"},
		{Path: "/foreign", AgentID: "foreign-agent", RemoteIP: "10.9.9.9"},
		{Path: "/unattributed", RemoteIP: "203.0.113.10"},
	} {
		s.trafficLog.add(entry)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/traffic", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleTrafficData(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data []TrafficEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, w.Body.String())
	}
	if len(envelope.Data) != 1 || envelope.Data[0].AgentID != "own-agent" {
		t.Fatalf("tenant traffic = %+v, want only own agent", envelope.Data)
	}
}

func TestHandleTrafficDataLegacyTenantKeepsGlobalView(t *testing.T) {
	s := mustTenantServer(t)
	s.trafficLog = newTrafficRing()
	s.trafficLog.add(TrafficEntry{Path: "/agent", AgentID: "any-agent"})
	s.trafficLog.add(TrafficEntry{Path: "/probe"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/traffic", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 0, ok: true})
	s.handleTrafficData(c)

	var envelope struct {
		Data []TrafficEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data) != 2 {
		t.Fatalf("global traffic rows = %d, want 2", len(envelope.Data))
	}
}

func TestHandleTrafficDataQueryFailureIsNotAnEmptyList(t *testing.T) {
	s := closedDBServer(t)
	s.trafficLog = newTrafficRing()
	s.trafficLog.add(TrafficEntry{Path: "/agent", AgentID: "agent-1"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/traffic", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleTrafficData(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
}
