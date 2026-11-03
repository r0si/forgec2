package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
