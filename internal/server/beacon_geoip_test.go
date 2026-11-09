package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// postBeaconFrom is v2Post with a chosen client address, so the handler's
// GeoIP branch (which skips loopback and empty addresses) is exercised. The
// middleware records what gin resolved as the client IP, because a change in
// trusted-proxy defaults would silently disable the lookup.
func postBeaconFrom(t *testing.T, s *Server, body, remoteAddr string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var seen string
	r := gin.New()
	r.POST("/beacon", func(c *gin.Context) {
		seen = c.ClientIP()
		s.handleBeacon(c)
	})
	s.router = r
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/beacon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	r.ServeHTTP(w, req)
	return w, seen
}

// TestBeaconRegistrationResolvesAgentCountry is the agent-side end-to-end
// counterpart of the login-log fix: after a real v3 registration, the server
// must resolve the agent's public IP through the shared provider chain and
// write country/city to the implant row.
//
// It drives the actual handler (not lookupGeoIP directly), so the wiring —
// async goroutine, semaphore, DB update — is covered, and it uses a client
// address that is public so the loopback guard does not skip the lookup.
func TestBeaconRegistrationResolvesAgentCountry(t *testing.T) {
	resetGeoIPState()
	clearProviderBackoff(t)
	t.Cleanup(resetGeoIPState)

	s, database := v2TestServer(t)
	cfg := s.cfg
	s.configMu.Lock()
	cfg.Server.GeoIPEnabled = true
	s.configMu.Unlock()
	s.ctx = context.Background()
	s.geoIPSem = make(chan struct{}, 4)
	s.httpClient = newCannedClient(&cannedProviderTransport{bodys: map[string]string{
		// The demo-host shape: ip-api is unreachable, zxinc answers.
		"ip-api.com":   `{"status":"fail","message":"reserved range"}`,
		"ip.zxinc.org": `{"code":0,"data":{"country":"中国–北京–北京"}}`,
	}})

	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	agent := v3TestAgent(t, s, uuid)
	// Prove the transport is wired the way the handler will use it.
	if c, city, _, _ := s.lookupGeoIP(context.Background(), "203.0.113.45"); c != "中国" || city != "北京" {
		t.Fatalf("direct lookup = %q/%q, want 中国/北京", c, city)
	}
	w, seenIP := postBeaconFrom(t, s, agent.registerFrame(), "203.0.113.44:41234")
	if w.Code != http.StatusOK {
		t.Fatalf("registration status = %d, body=%s", w.Code, w.Body.String())
	}
	if seenIP != "203.0.113.44" {
		t.Fatalf("handler saw client IP %q, want the public RemoteAddr", seenIP)
	}

	deadline := time.Now().Add(5 * time.Second)
	var implant db.Implant
	for {
		if err := database.Where("id = ?", uuid).First(&implant).Error; err != nil {
			t.Fatalf("implant row missing: %v", err)
		}
		if implant.Country != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent country never resolved: %+v", implant)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if implant.Country != "中国" || implant.City != "北京" {
		t.Fatalf("agent location = %q/%q, want 中国/北京", implant.Country, implant.City)
	}
	// zxinc publishes no coordinates; the handler must not write 0/0.
	if implant.Latitude != 0 || implant.Longitude != 0 {
		t.Fatalf("coordinates = %v/%v, want 0/0", implant.Latitude, implant.Longitude)
	}
}

// TestBeaconGeoIPSkipsLoopback keeps the local-dev guard: an agent registering
// from 127.0.0.1 must not trigger an outbound lookup.
func TestBeaconGeoIPSkipsLoopback(t *testing.T) {
	resetGeoIPState()
	clearProviderBackoff(t)
	t.Cleanup(resetGeoIPState)

	s, database := v2TestServer(t)
	s.configMu.Lock()
	s.cfg.Server.GeoIPEnabled = true
	s.configMu.Unlock()
	s.ctx = context.Background()
	s.geoIPSem = make(chan struct{}, 4)
	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip.zxinc.org": `{"code":0,"data":{"country":"中国"}}`,
	}}
	s.httpClient = newCannedClient(transport)

	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-111111111111"
	agent := v3TestAgent(t, s, uuid)
	if w, _ := postBeaconFrom(t, s, agent.registerFrame(), "127.0.0.1:50000"); w.Code != http.StatusOK {
		t.Fatalf("registration status = %d, body=%s", w.Code, w.Body.String())
	}
	time.Sleep(300 * time.Millisecond)
	if calls := transport.calls(); len(calls) != 0 {
		t.Fatalf("a loopback registration reached the provider chain: %v", calls)
	}
	var implant db.Implant
	if err := database.Where("id = ?", uuid).First(&implant).Error; err != nil {
		t.Fatal(err)
	}
	if implant.Country != "" {
		t.Fatalf("loopback agent got country %q", implant.Country)
	}
}
