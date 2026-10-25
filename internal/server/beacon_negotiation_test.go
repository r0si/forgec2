package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

// registerAndSession registers a test agent and establishes the session so
// the encrypted beacon path can be exercised.
func registerAndSession(t *testing.T, s *Server, r *gin.Engine, agent *tcpTestAgent) {
	t.Helper()
	clearDedup := func() {
		s.beaconDedupMu.Lock()
		s.beaconDedupCache = make(map[string]time.Time)
		s.beaconDedupMu.Unlock()
	}
	clearDedup()
	w := postJSON(r, "/beacon", agent.registerFrame())
	if w.Code != http.StatusOK {
		t.Fatalf("register: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var regResp struct {
		ECDHPub string `json:"ecdh_pub"`
		Mac     string `json:"mac"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("register response parse: %v", err)
	}
	if err := agent.establishFromServerKey(regResp.ECDHPub); err != nil {
		t.Fatalf("establish session: %v", err)
	}
}

func TestBeaconProtocolNegotiationRefusesOldAgentWithReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	s, r := initBeaconTestServer(t, database)

	agent := v3TestAgent(t, s, "55555555-6666-4333-8444-000000000000")
	registerAndSession(t, s, r, agent)

	// The agent announces a protocol version the server no longer serves.
	s.beaconDedupMu.Lock()
	s.beaconDedupCache = make(map[string]time.Time)
	s.beaconDedupMu.Unlock()
	inner, _ := json.Marshal(map[string]interface{}{"uuid": agent.uuid, "pv": 1})
	w := postJSON(r, "/beacon", agent.encryptedFrame(inner))

	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("an agent on an unserved protocol version must be refused with 426, got %d; body=%s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rebuild the agent") {
		t.Errorf("the refusal must tell the operator what to do, got %s", w.Body.String())
	}
}

func TestBeaconProtocolNegotiationRefusesNewAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	s, r := initBeaconTestServer(t, database)

	agent := v3TestAgent(t, s, "66666666-7777-4333-8444-000000000000")
	registerAndSession(t, s, r, agent)

	s.beaconDedupMu.Lock()
	s.beaconDedupCache = make(map[string]time.Time)
	s.beaconDedupMu.Unlock()
	inner, _ := json.Marshal(map[string]interface{}{"uuid": agent.uuid, "pv": protocol.CurrentProtocolVersion + 1})
	w := postJSON(r, "/beacon", agent.encryptedFrame(inner))

	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("an agent newer than the server must be refused with 426, got %d; body=%s",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "upgrade the server") {
		t.Errorf("the refusal must point at the server, got %s", w.Body.String())
	}
}

func TestBeaconProtocolNegotiationCountsRejections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	s, r := initBeaconTestServer(t, database)
	s.metrics = NewMetricsCollector(s)

	agent := v3TestAgent(t, s, "77777777-8888-4333-8444-000000000000")
	registerAndSession(t, s, r, agent)

	s.beaconDedupMu.Lock()
	s.beaconDedupCache = make(map[string]time.Time)
	s.beaconDedupMu.Unlock()
	inner, _ := json.Marshal(map[string]interface{}{"uuid": agent.uuid, "pv": 1})
	postJSON(r, "/beacon", agent.encryptedFrame(inner))

	counter, err := s.metrics.BeaconProtocolRejectedTotal.GetMetricWithLabelValues("too_old")
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if got := promtest.ToFloat64(counter); got == 0 {
		t.Error("a refused beacon must be counted")
	}
}

func TestAgentListFlagsOutdatedProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	s, _ := initBeaconTestServer(t, database)

	if err := database.Create(&db.Implant{
		ID:              "88888888-9999-4333-8444-000000000000",
		Hostname:        "old-host",
		ProtocolVersion: 1,
	}).Error; err != nil {
		t.Fatalf("create implant: %v", err)
	}
	if err := database.Create(&db.Implant{
		ID:              "99999999-aaaa-4333-8444-000000000000",
		Hostname:        "current-host",
		ProtocolVersion: protocol.CurrentProtocolVersion,
	}).Error; err != nil {
		t.Fatalf("create implant: %v", err)
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
			ID               string `json:"id"`
			ProtocolVersion  uint   `json:"protocol_version"`
			ProtocolOutdated bool   `json:"protocol_outdated"`
			ProtocolCurrent  uint   `json:"protocol_current"`
		} `json:"agents"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if len(resp.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(resp.Agents))
	}
	for _, a := range resp.Agents {
		if a.ProtocolCurrent != protocol.CurrentProtocolVersion {
			t.Errorf("agent %s: protocol_current = %d, want %d", a.ID, a.ProtocolCurrent, protocol.CurrentProtocolVersion)
		}
		switch a.ID {
		case "88888888-9999-4333-8444-000000000000":
			if !a.ProtocolOutdated {
				t.Error("an agent on an outdated protocol version must be flagged")
			}
		case "99999999-aaaa-4333-8444-000000000000":
			if a.ProtocolOutdated {
				t.Error("an agent on the current protocol version must not be flagged")
			}
		}
	}
}
