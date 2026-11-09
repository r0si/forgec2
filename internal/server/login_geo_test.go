package server

import (
	"testing"
	"time"
)

// TestLoginGeoProvidersClassifyRateLimits pins the difference between "this IP
// has no location" and "this provider is refusing us".
//
// The distinction matters because a refused lookup backs the provider off for
// five minutes, while a genuine miss keeps trying other providers. Mixing them
// up reproduces the defect found in the browser probe: ip.taobao.com answered
// `code:4` (over max qps) and the panel showed "unknown" forever, because a
// refusal was treated as a plain miss and every refresh retried it.
func TestLoginGeoProvidersClassifyRateLimits(t *testing.T) {
	cases := []struct {
		name        string
		parse       func([]byte) (string, string, bool)
		body        string
		wantCountry string
		wantBusy    bool
	}{
		{
			name:        "ip-api success",
			parse:       parseIPAPI,
			body:        `{"status":"success","country":"Germany","city":"Berlin"}`,
			wantCountry: "Germany",
		},
		{
			name:     "ip-api quota refusal",
			parse:    parseIPAPI,
			body:     `{"status":"fail","message":"quota exceeded"}`,
			wantBusy: true,
		},
		{
			name:  "ip-api private range is a miss, not a refusal",
			parse: parseIPAPI,
			body:  `{"status":"fail","message":"private range"}`,
		},
		{
			name:        "taobao success",
			parse:       parseTaobao,
			body:        `{"code":0,"data":{"country":"中国","city":"北京"}}`,
			wantCountry: "中国",
		},
		{
			name:     "taobao qps refusal",
			parse:    parseTaobao,
			body:     `{"msg":"the request over max qps for user","code":4}`,
			wantBusy: true,
		},
		{
			name:  "taobao unknown country is a miss",
			parse: parseTaobao,
			body:  `{"code":0,"data":{"country":"XX","city":""}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			country, _, busy := tc.parse([]byte(tc.body))
			if country != tc.wantCountry {
				t.Fatalf("country = %q, want %q", country, tc.wantCountry)
			}
			if busy != tc.wantBusy {
				t.Fatalf("busy = %v, want %v", busy, tc.wantBusy)
			}
		})
	}
}

// TestLoginGeoProviderBackoffExpires proves a refused provider is skipped and
// then retried after the window, instead of being disabled for the process
// lifetime (which showed up as countries never resolving).
func TestLoginGeoProviderBackoffExpires(t *testing.T) {
	markLoginGeoProviderDown("test-provider")
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
