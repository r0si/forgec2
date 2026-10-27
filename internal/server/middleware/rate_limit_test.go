package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestTokenBucketAllowsThenRefills pins the arithmetic the whole rate limiter rests on: a
// fresh bucket spends its capacity, refuses when empty, and refills at the configured rate.
// None of this had coverage, and a refill bug is silent - limits either never bite or bite
// forever.
func TestTokenBucketAllowsThenRefills(t *testing.T) {
	tb := NewTokenBucket(3, 1000) // refills ~1 token per millisecond
	for i := 0; i < 3; i++ {
		if !tb.Allow() {
			t.Fatalf("request %d was refused with a full bucket", i+1)
		}
	}
	if tb.Allow() {
		t.Fatal("a fourth request was allowed from a 3-token bucket")
	}
	if got := tb.Tokens(); got > 0.05 {
		t.Fatalf("tokens after exhaustion = %v, want ~0", got)
	}

	time.Sleep(10 * time.Millisecond)
	if !tb.Allow() {
		t.Fatal("the bucket never refilled")
	}

	// Capacity is a ceiling, not a running total: a long idle period must not exceed it.
	time.Sleep(20 * time.Millisecond)
	if got := tb.Tokens(); got > 3 {
		t.Fatalf("tokens = %v, want at most the capacity 3", got)
	}
}

// TestTokenBucketSnapshotIsConsistent keeps the status surface honest: Snapshot reports the
// same numbers the bucket is enforcing.
func TestTokenBucketSnapshotIsConsistent(t *testing.T) {
	tb := NewTokenBucket(5, 10)
	tokens, capacity, rate, lastRefill, accessedAt := tb.Snapshot()
	if capacity != 5 || rate != 10 {
		t.Fatalf("snapshot capacity/rate = %v/%v, want 5/10", capacity, rate)
	}
	if tokens < 4.9 || tokens > 5 {
		t.Fatalf("snapshot tokens = %v, want ~5", tokens)
	}
	if lastRefill.IsZero() || accessedAt.IsZero() {
		t.Fatal("snapshot timestamps are zero")
	}
}

// TestAPIRateLimiterReusesBuckets pins per-key identity: two calls for the same key must
// share one bucket, or the limit would reset on every request.
func TestAPIRateLimiterReusesBuckets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewAPIRateLimiter(ctx, 2, 1)
	defer rl.Stop()

	a := rl.GetBucket("ip:10.0.0.1")
	b := rl.GetBucket("ip:10.0.0.1")
	if a != b {
		t.Fatal("the same key produced two buckets")
	}
	if other := rl.GetBucket("ip:10.0.0.2"); other == a {
		t.Fatal("different keys share a bucket")
	}
}

// TestAPIRateLimiterBoundsItsMap covers the eviction path: an attacker rotating source
// addresses must not grow the map without limit.
func TestAPIRateLimiterBoundsItsMap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewAPIRateLimiter(ctx, 1, 1)
	defer rl.Stop()

	for i := 0; i < maxBuckets+25; i++ {
		rl.GetBucket("ip:" + itoaRateLimit(i))
	}
	rl.mu.Lock()
	size := len(rl.buckets)
	rl.mu.Unlock()
	if size > maxBuckets {
		t.Fatalf("bucket map grew to %d, cap is %d", size, maxBuckets)
	}
}

// TestRateLimiterMiddlewareSends429AndRetryAfter pins the wire contract a client depends on:
// over the limit means 429 with Retry-After, not a hang or a 500.
func TestRateLimiterMiddlewareSends429AndRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewAPIRateLimiter(ctx, 1, 1)
	defer rl.Stop()

	router := gin.New()
	router.Use(rl.LimitByUser())
	router.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	call := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.9:1234"
		router.ServeHTTP(w, req)
		return w
	}
	if w := call(); w.Code != http.StatusOK {
		t.Fatalf("first request = %d", w.Code)
	}
	w := call()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 carried no Retry-After header")
	}
	if got := w.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("X-RateLimit-Remaining = %q on a 429", got)
	}
}

// TestRateLimiterWhitelistedIPBypasses keeps the operator escape hatch working: a
// whitelisted source is never throttled, which is what makes the limiter safe to enable.
func TestRateLimiterWhitelistedIPBypasses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewAPIRateLimiter(ctx, 1, 1)
	defer rl.Stop()
	rl.SetWhitelist([]string{"10.0.0.77"})

	status := rl.GetStatus()
	if list, ok := status["whitelist"].([]string); !ok || len(list) != 1 || list[0] != "10.0.0.77" {
		t.Fatalf("whitelist not reported: %+v", status["whitelist"])
	}

	router := gin.New()
	router.Use(rl.LimitByUser())
	router.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.77:5555"
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("whitelisted request %d = %d", i+1, w.Code)
		}
	}
}

func itoaRateLimit(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
