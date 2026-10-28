package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestGeoIPCacheAndBudget protects the free tier of ip-api.com (45 requests/minute per source).
//
// Two properties matter, and neither was enforced before:
//   - a repeat lookup for the same IP must not leave the process (every beacon check-in used to issue
//     one, since the call site only skips the database WRITE when the location is unchanged);
//   - the process must stop calling before the upstream 429, because once the limit is tripped
//     ip-api answers 429 for the rest of the minute and geolocation goes dark for every beacon.
func TestGeoIPCacheAndBudget(t *testing.T) {
	resetGeoIPState()
	t.Cleanup(resetGeoIPState)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","country":"Testland","city":"Testville","lat":1.5,"lon":2.5}`)
	}))
	defer srv.Close()

	s := &Server{httpClient: &http.Client{Transport: rewriteTransport{srv.URL}}}
	ctx := context.Background()

	// First lookup goes out and is cached.
	country, city, lat, lon := s.lookupGeoIP(ctx, "203.0.113.10")
	if country != "Testland" || city != "Testville" || lat != 1.5 || lon != 2.5 {
		t.Fatalf("lookup returned %q/%q/%v/%v", country, city, lat, lon)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("first lookup issued %d requests, want 1", got)
	}

	// Second lookup for the same IP is served from the cache.
	s.lookupGeoIP(ctx, "203.0.113.10")
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("a repeat lookup for the same IP still went out (%d requests)", got)
	}

	// Distinct IPs stop at the local budget instead of tripping the upstream limit.
	for i := 0; i < geoIPBudgetPerMinute+10; i++ {
		s.lookupGeoIP(ctx, fmt.Sprintf("198.51.100.%d", i))
	}
	got := int(atomic.LoadInt32(&calls))
	if got > geoIPBudgetPerMinute+1 {
		t.Fatalf("%d requests went out; the local budget is %d per minute", got, geoIPBudgetPerMinute)
	}
	if got < geoIPBudgetPerMinute {
		t.Fatalf("only %d requests went out; the budget should allow %d", got, geoIPBudgetPerMinute)
	}

	// Private addresses never leave the process at all.
	before := atomic.LoadInt32(&calls)
	if c, _, _, _ := s.lookupGeoIP(ctx, "10.1.2.3"); c != "" {
		t.Fatalf("a private address produced a location: %q", c)
	}
	if atomic.LoadInt32(&calls) != before {
		t.Fatal("a private address triggered a lookup")
	}
}

// rewriteTransport points the server's client at a test server without DNS.
type rewriteTransport struct{ base string }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(t.base, "http://")
	return http.DefaultTransport.RoundTrip(clone)
}

// TestGeoIPRejectedLookupIsNotCached keeps a failure from poisoning the cache for geoIPCacheTTL.
func TestGeoIPRejectedLookupIsNotCached(t *testing.T) {
	resetGeoIPState()
	t.Cleanup(resetGeoIPState)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"fail","message":"reserved range"}`)
	}))
	defer srv.Close()

	s := &Server{httpClient: &http.Client{Transport: rewriteTransport{srv.URL}}}
	s.lookupGeoIP(context.Background(), "203.0.113.20")
	s.lookupGeoIP(context.Background(), "203.0.113.20")
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("a failed lookup was cached: %d request(s) for two calls", got)
	}
	if time.Since(geoIPState.windowStart) > time.Minute {
		t.Fatal("the rate window did not start")
	}
}
