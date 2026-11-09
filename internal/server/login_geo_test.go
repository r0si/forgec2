package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
)

// cannedProviderTransport answers per provider host, so a test can state what
// each provider says without DNS or a real network. Hosts with no canned body
// behave like an unreachable provider.
type cannedProviderTransport struct {
	mu    sync.Mutex
	hosts []string
	bodys map[string]string
}

func (c *cannedProviderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.hosts = append(c.hosts, r.URL.Host)
	body, ok := c.bodys[r.URL.Host]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial tcp: lookup %s: no such host", r.URL.Host)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

func (c *cannedProviderTransport) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.hosts...)
}

// clearProviderBackoff drops the global backoff markers so one test's refusal
// cannot decide another test's provider order.
func clearProviderBackoff(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		loginGeoProviderDown.mu.Lock()
		loginGeoProviderDown.at = make(map[string]time.Time)
		loginGeoProviderDown.mu.Unlock()
	})
	loginGeoProviderDown.mu.Lock()
	loginGeoProviderDown.at = make(map[string]time.Time)
	loginGeoProviderDown.mu.Unlock()
}

// TestLoginGeoProvidersClassifyRateLimits pins the difference between "this IP
// has no location" and "this provider is refusing us".
//
// The distinction matters because a refused lookup backs the provider off for
// two minutes, while a genuine miss keeps trying other providers. Mixing them
// up reproduces the defect found in the browser probe: ip.taobao.com answered
// `code:4` (over max qps) and the panel showed "unknown" forever, because a
// refusal was treated as a plain miss and every refresh retried it.
func TestLoginGeoProvidersClassifyRateLimits(t *testing.T) {
	cases := []struct {
		name        string
		parse       func([]byte) (string, string, string)
		body        string
		wantCountry string
		wantCity    string
		wantRefusal bool
	}{
		{
			name:        "ip-api success",
			parse:       parseIPAPI,
			body:        `{"status":"success","country":"Germany","city":"Berlin"}`,
			wantCountry: "Germany",
			wantCity:    "Berlin",
		},
		{
			name:        "ip-api quota refusal",
			parse:       parseIPAPI,
			body:        `{"status":"fail","message":"quota exceeded"}`,
			wantRefusal: true,
		},
		{
			name:  "ip-api private range is a miss, not a refusal",
			parse: parseIPAPI,
			body:  `{"status":"fail","message":"private range"}`,
		},
		{
			name:        "zxinc splits country/province/city",
			parse:       parseZXInc,
			body:        `{"code":0,"data":{"country":"中国–北京–北京"}}`,
			wantCountry: "中国",
			wantCity:    "北京",
		},
		{
			name:        "zxinc foreign answer",
			parse:       parseZXInc,
			body:        `{"code":0,"data":{"country":"美国–加利福尼亚州–圣克拉拉–山景城"}}`,
			wantCountry: "美国",
			wantCity:    "山景城",
		},
		{
			name:  "zxinc empty answer is a miss",
			parse: parseZXInc,
			body:  `{"code":0,"data":{"country":""}}`,
		},
		{
			name:        "taobao success",
			parse:       parseTaobao,
			body:        `{"code":0,"data":{"country":"中国","city":"北京"}}`,
			wantCountry: "中国",
			wantCity:    "北京",
		},
		{
			name:        "taobao qps refusal",
			parse:       parseTaobao,
			body:        `{"msg":"the request over max qps for user","code":4}`,
			wantRefusal: true,
		},
		{
			name:  "taobao unknown country is a miss",
			parse: parseTaobao,
			body:  `{"code":0,"data":{"country":"XX","city":""}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			country, city, refusal := tc.parse([]byte(tc.body))
			if country != tc.wantCountry || city != tc.wantCity {
				t.Fatalf("location = %q/%q, want %q/%q", country, city, tc.wantCountry, tc.wantCity)
			}
			if (refusal != "") != tc.wantRefusal {
				t.Fatalf("refusal = %q, want refusal=%v", refusal, tc.wantRefusal)
			}
		})
	}
}

// TestLoginGeoProviderChainOrder proves the fallback actually falls through in
// order and stops at the first answer.
//
// This is the shape the demo needed: ip-api.com refuses (its network cannot
// reach it), ip.zxinc.org answers, and ip.taobao.com must not be called at all
// (its shared key allows ~1 QPS).
func TestLoginGeoProviderChainOrder(t *testing.T) {
	clearProviderBackoff(t)
	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip-api.com":    `{"status":"fail","message":"quota exceeded"}`,
		"ip.zxinc.org":  `{"code":0,"data":{"country":"德国–柏林–柏林"}}`,
		"ip.taobao.com": `{"code":0,"data":{"country":"WRONG","city":"WRONG"}}`,
	}}
	s := &Server{httpClient: &http.Client{Transport: transport}, cfg: &config.Config{}}

	country, city := s.resolveLoginGeo(context.Background(), "203.0.113.90")
	if country != "德国" || city != "柏林" {
		t.Fatalf("resolved %q/%q, want 德国/柏林", country, city)
	}
	calls := transport.calls()
	want := []string{"ip-api.com", "ip.zxinc.org"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("provider calls = %v, want %v (taobao must not run once zxinc answers)", calls, want)
	}
	// The refusal must be remembered: the next lookup skips ip-api entirely.
	before := len(transport.calls())
	if country, _ := s.resolveLoginGeo(context.Background(), "203.0.113.91"); country != "德国" {
		t.Fatalf("second lookup = %q", country)
	}
	if after := transport.calls(); len(after) != before+1 || after[before] != "ip.zxinc.org" {
		t.Fatalf("calls after refusal = %v, want only ip.zxinc.org", after[before:])
	}
}

// TestLoginGeoProviderBackoffExpires proves a refused provider is skipped and
// then retried after the window, instead of being disabled for the process
// lifetime (which showed up as countries never resolving).
func TestLoginGeoProviderBackoffExpires(t *testing.T) {
	markLoginGeoProviderDown("test-provider", "test")
	if loginGeoProviderUsable("test-provider") {
		t.Fatal("a provider marked down must be skipped")
	}
	loginGeoProviderDown.mu.Lock()
	loginGeoProviderDown.at["test-provider"] = time.Now().Add(-loginGeoFailedProviderTTL - time.Second)
	loginGeoProviderDown.mu.Unlock()
	if !loginGeoProviderUsable("test-provider") {
		t.Fatal("a provider past the backoff window must be tried again")
	}
	loginGeoProviderDown.mu.Lock()
	delete(loginGeoProviderDown.at, "test-provider")
	loginGeoProviderDown.mu.Unlock()
}

// TestLoginGeoWorkerResolvesQueuedIP pins the whole background pipeline: the
// panel's read must not wait on a provider, the worker must resolve the queued
// IP into the cache, and a later read must answer from that cache without
// touching the network again.
//
// This is the local counterpart of the demo defect where public login IPs kept
// showing "unknown": the summary chips only fill in if the worker actually
// writes login_geos.
func TestLoginGeoWorkerResolvesQueuedIP(t *testing.T) {
	clearProviderBackoff(t)
	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip-api.com":   `{"status":"fail","message":"private range"}`,
		"ip.zxinc.org": `{"code":0,"data":{"country":"中国–北京–北京"}}`,
	}}

	cfg := &config.Config{}
	cfg.Server.GeoIPEnabled = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Server{
		db:         testutil.SetupTestDB(t),
		cfg:        cfg,
		ctx:        ctx,
		httpClient: &http.Client{Transport: transport, Timeout: 5 * time.Second},
	}

	const ip = "203.0.113.77"
	start := time.Now()
	country, _ := s.loginCountry(context.Background(), ip)
	elapsed := time.Since(start)
	if country != "" {
		t.Fatalf("uncached IP already had a country %q", country)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("the request path blocked for %s on an external lookup", elapsed)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if country, city := s.cachedLoginCountry(context.Background(), ip); country == "中国" && city == "北京" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker never cached the country (provider calls: %v)", transport.calls())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A cache hit must stay offline.
	before := len(transport.calls())
	if country, city := s.loginCountry(context.Background(), ip); country != "中国" || city != "北京" {
		t.Fatalf("cached read = %q/%q, want 中国/北京", country, city)
	}
	if after := transport.calls(); len(after) != before {
		t.Fatalf("a cache hit made %d extra provider call(s)", len(after)-before)
	}
}

// TestLoginGeoDisabledMakesNoCall holds the opt-in boundary: with
// server.geoip_enabled false, opening the login log must not contact anyone.
func TestLoginGeoDisabledMakesNoCall(t *testing.T) {
	clearProviderBackoff(t)
	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip.zxinc.org": `{"code":0,"data":{"country":"中国"}}`,
	}}
	cfg := &config.Config{} // GeoIPEnabled stays false
	s := &Server{
		db:         testutil.SetupTestDB(t),
		cfg:        cfg,
		ctx:        context.Background(),
		httpClient: &http.Client{Transport: transport, Timeout: 5 * time.Second},
	}
	if country, _ := s.loginCountry(context.Background(), "203.0.113.78"); country != "" {
		t.Fatalf("disabled lookup returned %q", country)
	}
	time.Sleep(300 * time.Millisecond)
	if calls := transport.calls(); len(calls) != 0 {
		t.Fatalf("geoip_enabled=false still made %d provider call(s)", len(calls))
	}
	var rows int64
	if err := s.db.Model(&db.LoginGeo{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("disabled lookup cached %d row(s)", rows)
	}
}

// TestLoginGeoSummaryPayloadHasStableShape guards the JSON the panel reads: the
// chips need country/failed/ok even when a country is still unresolved (empty
// string), because the UI labels that bucket instead of hiding it.
func TestLoginGeoSummaryPayloadHasStableShape(t *testing.T) {
	encoded, err := json.Marshal([]loginCountryStat{
		{Country: "中国", Failed: 1, OK: 2},
		{Country: "", Failed: 0, OK: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"country":"中国","failed":1,"ok":2},{"country":"","failed":0,"ok":3}]`
	if string(encoded) != want {
		t.Fatalf("payload = %s, want %s", encoded, want)
	}
}

// TestLoginGeoCacheIsPruned proves the cache has an eviction and not only a
// writer: rows past the TTL are removed, fresh ones survive. Without this the
// table keeps one row per source IP ever seen, forever.
func TestLoginGeoCacheIsPruned(t *testing.T) {
	s := &Server{db: testutil.SetupTestDB(t), ctx: context.Background()}

	fresh := db.LoginGeo{IP: "203.0.113.10", Country: "中国", ResolvedAt: time.Now().Add(-time.Hour)}
	stale := db.LoginGeo{IP: "203.0.113.11", Country: "德国", ResolvedAt: time.Now().Add(-loginGeoTTL - time.Hour)}
	for _, row := range []db.LoginGeo{fresh, stale} {
		if err := s.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	s.cleanupExpiredLoginGeo()

	var kept []db.LoginGeo
	if err := s.db.Order("ip").Find(&kept).Error; err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].IP != fresh.IP {
		t.Fatalf("kept rows = %+v, want only the fresh %s", kept, fresh.IP)
	}
}
