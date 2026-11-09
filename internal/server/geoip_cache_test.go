package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// newCannedClient wires a test transport into the server's outbound client
// without DNS. Provider hosts are matched by name by the transport.
func newCannedClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

// TestGeoIPCacheAndBudget protects the free tier of the GeoIP provider chain
// (ip-api.com allows 45 requests/minute per source).
//
// Two properties matter, and neither was enforced before:
//   - a repeat lookup for the same IP must not leave the process (every beacon check-in used to issue
//     one, since the call site only skips the database WRITE when the location is unchanged);
//   - the process must stop calling before the upstream 429, because once the limit is tripped
//     ip-api answers 429 for the rest of the minute and geolocation goes dark for every beacon.
func TestGeoIPCacheAndBudget(t *testing.T) {
	resetGeoIPState()
	clearProviderBackoff(t)
	t.Cleanup(resetGeoIPState)

	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip-api.com": `{"status":"success","country":"Testland","city":"Testville","lat":1.5,"lon":2.5}`,
	}}
	s := &Server{httpClient: newCannedClient(transport)}
	ctx := context.Background()

	// First lookup goes out and is cached.
	country, city, lat, lon := s.lookupGeoIP(ctx, "203.0.113.10")
	if country != "Testland" || city != "Testville" || lat != 1.5 || lon != 2.5 {
		t.Fatalf("lookup returned %q/%q/%v/%v", country, city, lat, lon)
	}
	if got := len(transport.calls()); got != 1 {
		t.Fatalf("first lookup issued %d requests, want 1", got)
	}

	// Second lookup for the same IP is served from the cache.
	s.lookupGeoIP(ctx, "203.0.113.10")
	if got := len(transport.calls()); got != 1 {
		t.Fatalf("a repeat lookup for the same IP still went out (%d requests)", got)
	}

	// Distinct IPs stop at the local budget instead of tripping the upstream limit.
	for i := 0; i < geoIPBudgetPerMinute+10; i++ {
		s.lookupGeoIP(ctx, fmt.Sprintf("198.51.100.%d", i))
	}
	got := len(transport.calls())
	if got > geoIPBudgetPerMinute+1 {
		t.Fatalf("%d requests went out; the local budget is %d per minute", got, geoIPBudgetPerMinute)
	}
	if got < geoIPBudgetPerMinute {
		t.Fatalf("only %d requests went out; the budget should allow %d", got, geoIPBudgetPerMinute)
	}

	// Private addresses never leave the process at all.
	before := len(transport.calls())
	if c, _, _, _ := s.lookupGeoIP(ctx, "10.1.2.3"); c != "" {
		t.Fatalf("a private address produced a location: %q", c)
	}
	if len(transport.calls()) != before {
		t.Fatal("a private address triggered a lookup")
	}
}

// TestGeoIPFallsBackToZXInc is the agent-side counterpart of the login-log
// fix: on a network where ip-api.com is blocked (the demo host), an agent's
// country must still resolve instead of staying blank.
func TestGeoIPFallsBackToZXInc(t *testing.T) {
	resetGeoIPState()
	clearProviderBackoff(t)
	t.Cleanup(resetGeoIPState)

	transport := &cannedProviderTransport{bodys: map[string]string{
		// ip-api refuses (quota), zxinc has no coordinates, taobao must not run.
		"ip-api.com":    `{"status":"fail","message":"quota exceeded"}`,
		"ip.zxinc.org":  `{"code":0,"data":{"country":"中国–北京–北京"}}`,
		"ip.taobao.com": `{"code":0,"data":{"country":"WRONG","city":"WRONG"}}`,
	}}
	s := &Server{httpClient: newCannedClient(transport)}

	country, city, lat, lon := s.lookupGeoIP(context.Background(), "203.0.113.30")
	if country != "中国" || city != "北京" {
		t.Fatalf("fallback lookup = %q/%q, want 中国/北京", country, city)
	}
	if lat != 0 || lon != 0 {
		t.Fatalf("coordinates = %v/%v, want 0/0 from a provider that publishes none", lat, lon)
	}
	calls := transport.calls()
	if len(calls) != 2 || calls[0] != "ip-api.com" || calls[1] != "ip.zxinc.org" {
		t.Fatalf("provider calls = %v, want ip-api then zxinc only", calls)
	}

	// The answer is cached, so a second check-in makes no provider call.
	before := len(transport.calls())
	if c, _, _, _ := s.lookupGeoIP(context.Background(), "203.0.113.30"); c != "中国" {
		t.Fatalf("cached lookup = %q", c)
	}
	if after := len(transport.calls()); after != before {
		t.Fatalf("a cached agent location still made %d provider call(s)", after-before)
	}
}

// TestGeoIPFailedLookupIsNotCached keeps a failure from poisoning the cache for
// geoIPCacheTTL: every provider misses, so the next check-in must retry.
func TestGeoIPFailedLookupIsNotCached(t *testing.T) {
	resetGeoIPState()
	clearProviderBackoff(t)
	t.Cleanup(resetGeoIPState)

	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip-api.com":    `{"status":"fail","message":"reserved range"}`,
		"ip.zxinc.org":  `{"code":1,"data":{"country":""}}`,
		"ip.taobao.com": `{"code":0,"data":{"country":"XX","city":""}}`,
	}}
	s := &Server{httpClient: newCannedClient(transport)}

	if country, _, _, _ := s.lookupGeoIP(context.Background(), "203.0.113.20"); country != "" {
		t.Fatalf("a failed lookup produced %q", country)
	}
	first := len(transport.calls())
	s.lookupGeoIP(context.Background(), "203.0.113.20")
	if got := len(transport.calls()); got <= first {
		t.Fatalf("a failed lookup was cached: %d requests total for two calls", got)
	}
	if _, cached := geoIPState.entries["203.0.113.20"]; cached {
		t.Fatal("a failed lookup was written to the cache")
	}
	if time.Since(geoIPState.windowStart) > time.Minute {
		t.Fatal("the rate window did not start")
	}
}

// TestGeoAgentUpdatesKeepCoordinates proves a fallback answer does not move an
// agent's marker: country/city update, coordinates only when a provider
// actually supplied them.
func TestGeoAgentUpdatesKeepCoordinates(t *testing.T) {
	withCoords := geoAgentUpdates("中国", "北京", 39.9, 116.4)
	if withCoords["latitude"] != 39.9 || withCoords["longitude"] != 116.4 {
		t.Fatalf("coordinates dropped: %+v", withCoords)
	}
	without := geoAgentUpdates("中国", "北京", 0, 0)
	if _, ok := without["latitude"]; ok {
		t.Fatalf("zero latitude written: %+v", without)
	}
	if _, ok := without["longitude"]; ok {
		t.Fatalf("zero longitude written: %+v", without)
	}
	if without["country"] != "中国" || without["city"] != "北京" {
		t.Fatalf("country/city missing: %+v", without)
	}
}

// TestGeoIPPrivateAddressSkipsProviderChain holds the "never dial a private
// address" boundary for the agent path.
func TestGeoIPPrivateAddressSkipsProviderChain(t *testing.T) {
	resetGeoIPState()
	t.Cleanup(resetGeoIPState)
	transport := &cannedProviderTransport{bodys: map[string]string{
		"ip-api.com": `{"status":"success","country":"Testland"}`,
	}}
	s := &Server{httpClient: newCannedClient(transport)}
	for _, ip := range []string{"10.0.0.5", "127.0.0.1", "192.168.1.1", "100.64.0.1", "::1"} {
		if country, _, _, _ := s.lookupGeoIP(context.Background(), ip); country != "" {
			t.Fatalf("%s produced %q", ip, country)
		}
	}
	if calls := transport.calls(); len(calls) != 0 {
		t.Fatalf("private addresses reached the provider chain: %v", calls)
	}
}
