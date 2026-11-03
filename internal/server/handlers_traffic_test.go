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
		c.Set(trafficTenantIDContextKey, uint(7))
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
	if got := logs[0].TenantID; got != 7 {
		t.Fatalf("tenant id = %d, want authenticated tenant 7", got)
	}
	if got := logs[0].Status; got != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", got, http.StatusAccepted)
	}
}

func TestTrafficByteAccumulatorTenantIsolation(t *testing.T) {
	acc := newTrafficByteAccumulator()
	now := time.Now()
	acc.add(now, 1, 10, 20)
	acc.add(now, 2, 30, 40)
	acc.add(now, 0, 5, 5)
	start, end := now.Add(-time.Minute), now.Add(time.Minute)

	if in, out := acc.sumRange(start, end, 1); in != 10 || out != 20 {
		t.Fatalf("tenant 1 bytes = %d/%d, want 10/20", in, out)
	}
	if in, out := acc.sumRange(start, end, 2); in != 30 || out != 40 {
		t.Fatalf("tenant 2 bytes = %d/%d, want 30/40", in, out)
	}
	if in, out := acc.sumRange(start, end, 0); in != 45 || out != 65 {
		t.Fatalf("global bytes = %d/%d, want 45/65", in, out)
	}
}

func TestDashboardListenerTrafficIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	s.trafficBytes = newTrafficByteAccumulator()
	now := time.Now()
	s.trafficBytes.add(now, 1, 100, 200)
	s.trafficBytes.add(now, 2, 900, 800)

	for _, dashboardRange := range []string{"1h", "24h"} {
		t.Run(dashboardRange, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/listener-traffic?range="+dashboardRange, nil)
			c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
			s.handleDashboardListenerTraffic(c)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			var envelope struct {
				Data TrafficData `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode dashboard traffic: %v", err)
			}
			if got := envelope.Data.BytesIn[len(envelope.Data.BytesIn)-1]; got != 100 {
				t.Fatalf("tenant bytes in = %d, want 100", got)
			}
			if got := envelope.Data.BytesOut[len(envelope.Data.BytesOut)-1]; got != 200 {
				t.Fatalf("tenant bytes out = %d, want 200", got)
			}
		})
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
