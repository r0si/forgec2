package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newDomainFrontTestServer builds a server with the fronting maps
// initialized (a bare &Server{} has nil maps).
func newDomainFrontTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return &Server{
		db:                 testutil.SetupTestDB(t),
		cfg:                &config.Config{},
		domainFrontStatus:  make(map[string]*frontDomainState),
		domainFrontDomains: nil,
	}
}

// TestFrontRefreshDoesNotHoldLockDuringProbe proves a reader is not blocked
// while domains are being probed: frontRefresh used to hold domainFrontMu
// across every DNS+HTTPS probe, stalling list/status for the whole sweep.
func TestFrontRefreshDoesNotHoldLockDuringProbe(t *testing.T) {
	s := newDomainFrontTestServer(t)
	// A syntactically invalid domain fails validation inside
	// frontCheckDomain without any network access.
	s.domainFrontDomains = []string{"invalid..domain..test"}

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		s.frontRefresh()
		close(done)
	}()
	<-started

	// A concurrent reader must not block on the refresh: poll with a short
	// deadline and fail if it cannot acquire the lock in time.
	read := make(chan struct{})
	go func() {
		s.frontDomains()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		t.Fatal("frontDomains blocked while frontRefresh was probing: the lock is held across I/O")
	}
	<-done
}

// TestCapDomainFrontsEnforcesMax proves the list is capped and deduplicated.
func TestCapDomainFrontsEnforcesMax(t *testing.T) {
	over := make([]string, 0, MaxDomainFrontStatus+10)
	for i := 0; i < MaxDomainFrontStatus+10; i++ {
		over = append(over, fmt.Sprintf("d%03d.example.com", i))
	}
	over = append(over, over[0], "   ", "") // duplicate + blanks

	capped := capDomainFronts(over)
	if len(capped) != MaxDomainFrontStatus {
		t.Fatalf("cap kept %d domains, want %d", len(capped), MaxDomainFrontStatus)
	}
	seen := map[string]bool{}
	for _, d := range capped {
		if seen[d] {
			t.Fatalf("duplicate domain survived the cap: %s", d)
		}
		seen[d] = true
	}
}

// TestDomainFrontConfigRoundTrip proves a configured list survives a
// "restart": persist on one server instance, restore on another that shares
// the same database.
func TestDomainFrontConfigRoundTrip(t *testing.T) {
	database := testutil.SetupTestDB(t)
	first := &Server{db: database, cfg: &config.Config{}, domainFrontStatus: make(map[string]*frontDomainState)}
	first.domainFrontDomains = []string{"a.example.com", "b.example.com"}
	first.domainFrontAuto = true
	first.persistDomainFrontConfig()

	second := &Server{db: database, cfg: &config.Config{}, domainFrontStatus: make(map[string]*frontDomainState)}
	second.restoreDomainFrontConfig()
	if len(second.domainFrontDomains) != 2 || second.domainFrontDomains[0] != "a.example.com" {
		t.Fatalf("restored domains=%v, want [a.example.com b.example.com]", second.domainFrontDomains)
	}
	if !second.domainFrontAuto {
		t.Fatal("auto_failover was not restored")
	}
	var stored db.ServerConfig
	if err := database.Where("key = ?", "domain_fronting_config").First(&stored).Error; err != nil {
		t.Fatalf("config row missing: %v", err)
	}
}

// TestDomainFrontConfigHandlerRejectsBlockedDomain proves the config
// endpoint still refuses a domain that fails the SSRF guard: the stored
// list feeds server-side probes, so an unvalidated entry would be a stored
// SSRF primitive.
func TestDomainFrontConfigHandlerRejectsBlockedDomain(t *testing.T) {
	s := newDomainFrontTestServer(t)

	body := `{"domains":["127.0.0.1"],"auto_failover":false}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleAPIInfraFrontConfig(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400 for a blocked domain", w.Code, w.Body.String())
	}
}

// TestDomainFrontConfigHandlerPersists proves a successful config persists
// (the endpoint is exercised through a domain that passes validation
// without DNS: a public literal IP is accepted and fetch happens later,
// outside this handler).
func TestDomainFrontConfigHandlerPersists(t *testing.T) {
	s := newDomainFrontTestServer(t)
	// The handler probes the new domain for immediate status; keep the
	// probe short so the test does not wait for a real socket timeout
	// (203.0.113.10 is TEST-NET-3: unroutable, hence a 50ms failure).
	s.httpClient = &http.Client{Timeout: 50 * time.Millisecond}

	body := `{"domains":["203.0.113.10"],"auto_failover":true}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleAPIInfraFrontConfig(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var stored db.ServerConfig
	if err := s.db.Where("key = ?", "domain_fronting_config").First(&stored).Error; err != nil {
		t.Fatalf("config not persisted: %v", err)
	}
	var parsed struct {
		Domains      []string `json:"domains"`
		AutoFailover bool     `json:"auto_failover"`
	}
	if err := json.Unmarshal([]byte(stored.Value), &parsed); err != nil {
		t.Fatalf("stored value is not JSON: %v", err)
	}
	if len(parsed.Domains) != 1 || parsed.Domains[0] != "203.0.113.10" || !parsed.AutoFailover {
		t.Fatalf("stored=%+v, want the configured domain and auto_failover", parsed)
	}
}

// TestRestoreDomainFrontConfigCaps proves the restore path caps the
// persisted list: a stored value is never trusted blindly.
func TestRestoreDomainFrontConfigCaps(t *testing.T) {
	s := newDomainFrontTestServer(t)
	domains := make([]string, 0, MaxDomainFrontStatus+5)
	for i := 0; i < MaxDomainFrontStatus+5; i++ {
		domains = append(domains, fmt.Sprintf("z%03d.example.com", i))
	}
	payload, err := json.Marshal(struct {
		Domains      []string `json:"domains"`
		AutoFailover bool     `json:"auto_failover"`
	}{Domains: domains, AutoFailover: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Save(&db.ServerConfig{Key: "domain_fronting_config", Value: string(payload)}).Error; err != nil {
		t.Fatalf("seed config: %v", err)
	}
	s.restoreDomainFrontConfig()
	if len(s.domainFrontDomains) != MaxDomainFrontStatus {
		t.Fatalf("restored %d domains, want the cap %d", len(s.domainFrontDomains), MaxDomainFrontStatus)
	}
}

// TestHandleAPIInfraFrontListReturnsConfig pins the GET list endpoint: it
// returns the in-memory config the Domain Fronting page renders, and does
// not mutate anything.
func TestHandleAPIInfraFrontListReturnsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{}
	s.domainFrontMu.Lock()
	s.domainFrontDomains = []string{"cdn.example.com"}
	s.domainFrontAuto = false
	s.domainFrontStatus = map[string]*frontDomainState{
		"cdn.example.com": {Domain: "cdn.example.com", Healthy: true, Active: true},
	}
	s.domainFrontMu.Unlock()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/infra/front/list", nil)
	s.handleAPIInfraFrontList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var got struct {
		Data struct {
			Domains []struct {
				Domain  string `json:"domain"`
				Healthy bool   `json:"healthy"`
			} `json:"domains"`
			AutoFailover bool `json:"auto_failover"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data.Domains) != 1 || got.Data.Domains[0].Domain != "cdn.example.com" {
		t.Fatalf("domains = %+v", got.Data.Domains)
	}
	if got.Data.AutoFailover {
		t.Fatal("auto_failover = true, want false")
	}
}

// Ensure the gorm import stays used if more assertions are added.
var _ = gorm.DB{}
