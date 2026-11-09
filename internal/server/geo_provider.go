package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// geoProviderTimeout bounds one provider call. It is independent of the shared
// HTTP client's 30s timeout: a blackholed provider (ip-api.com from some
// networks) would otherwise hold the lookup for half a minute and starve the
// providers behind it.
const geoProviderTimeout = 4 * time.Second

// geoProviderBackoffTTL is how long a provider that could not answer is
// skipped. Without it every lookup retried the unreachable provider first,
// paying its connection timeout each time. Two minutes keeps a recoverable
// outage from blacking out locations for the rest of the hour.
const geoProviderBackoffTTL = 2 * time.Minute

var geoProviderDown = struct {
	mu sync.Mutex
	at map[string]time.Time
}{at: make(map[string]time.Time)}

func geoProviderUsable(name string) bool {
	geoProviderDown.mu.Lock()
	defer geoProviderDown.mu.Unlock()
	at, ok := geoProviderDown.at[name]
	return !ok || time.Since(at) >= geoProviderBackoffTTL
}

func markGeoProviderDown(name, reason string) {
	geoProviderDown.mu.Lock()
	already := false
	if at, ok := geoProviderDown.at[name]; ok && time.Since(at) < geoProviderBackoffTTL {
		already = true
	}
	geoProviderDown.at[name] = time.Now()
	geoProviderDown.mu.Unlock()
	if !already {
		// Warn once per window, not per lookup: a provider that is blocked or
		// throttled is worth seeing at default log level, because the symptom
		// otherwise is "locations are always unknown" with no explanation.
		slog.Warn("Geo provider unavailable; locations will stay unknown until it recovers",
			"provider", name, "reason", reason, "retry_after", geoProviderBackoffTTL)
	}
}

// geoLocation is what a provider knows about an IP. Providers that publish no
// coordinates leave Lat/Lon zero; callers must not treat that as "0,0".
type geoLocation struct {
	Country string
	City    string
	Lat     float64
	Lon     float64
}

// geoProvider is one lookup service. `locate` extracts a location from a 200
// body; an empty Country means "this provider had no answer" (fall through),
// while a non-empty refusal means the provider is rejecting us (rate limit)
// and should be backed off.
type geoProvider struct {
	name   string
	url    func(ip string) string
	locate func(body []byte) (geoLocation, string)
}

// geoProviders are tried in order. ip-api.com is the only one with
// coordinates, so it comes first for the agent map. It is unreachable from
// some networks (the demo host cannot reach it at all); ip.zxinc.org answers
// there in UTF-8 without a shared quota, and ip.taobao.com is the last resort
// because its public `alibaba-inc` key allows roughly 1 QPS globally.
var geoProviders = []geoProvider{
	{
		name: "ip-api.com",
		url: func(ip string) string {
			// The free tier is HTTP-only and answers `status:"fail"` inside a
			// 200 for a rejected lookup, which locateIPAPI classifies.
			return "http://ip-api.com/json/" + url.PathEscape(ip) + "?fields=status,message,country,city,lat,lon"
		},
		locate: locateIPAPI,
	},
	{
		name: "ip.zxinc.org",
		url: func(ip string) string {
			return "https://ip.zxinc.org/api.php?type=json&ip=" + url.PathEscape(ip)
		},
		locate: locateZXInc,
	},
	{
		name: "ip.taobao.com",
		url: func(ip string) string {
			return "https://ip.taobao.com/outGetIpInfo?accessKey=alibaba-inc&ip=" + url.PathEscape(ip)
		},
		locate: locateTaobao,
	},
}

// resolveGeoLocation tries the providers in order and returns the first answer.
//
// Both the login log and the beacon/agent GeoIP path go through this function,
// so a network where ip-api.com is blocked (the demo host) resolves locations
// on both surfaces instead of reporting "unknown" forever on one of them.
func (s *Server) resolveGeoLocation(ctx context.Context, ip string) (geoLocation, bool) {
	for _, p := range geoProviders {
		if !geoProviderUsable(p.name) {
			continue
		}
		reqCtx, cancel := context.WithTimeout(ctx, geoProviderTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, p.url(ip), nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			cancel()
			slog.Debug("Geo provider unreachable", "provider", p.name, "ip", ip, "err", err)
			markGeoProviderDown(p.name, err.Error())
			continue
		}
		// The status is read before the body: a 429 here is the provider's
		// rate limit, and decoding its error page as a location is the
		// silent-empty-field defect this path already fixed once.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		status := resp.StatusCode
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			continue
		}
		if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
			slog.Debug("Geo provider throttled", "provider", p.name, "ip", ip, "status", status)
			markGeoProviderDown(p.name, fmt.Sprintf("HTTP %d", status))
			continue
		}
		if status != http.StatusOK {
			continue
		}
		loc, refusal := p.locate(body)
		if refusal != "" {
			slog.Debug("Geo provider refused the lookup", "provider", p.name, "ip", ip, "reason", refusal)
			markGeoProviderDown(p.name, refusal)
			continue
		}
		if loc.Country != "" {
			return loc, true
		}
	}
	return geoLocation{}, false
}

// locateIPAPI reads ip-api.com's answer, including coordinates. A non-empty
// refusal means the provider is not serving us (rate limit / quota), which
// backs it off instead of just missing one lookup.
func locateIPAPI(body []byte) (geoLocation, string) {
	var r struct {
		Status  string  `json:"status"`
		Message string  `json:"message"`
		Country string  `json:"country"`
		City    string  `json:"city"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	}
	if json.Unmarshal(body, &r) != nil {
		return geoLocation{}, ""
	}
	if r.Status != "success" {
		// "private range"/"reserved range" are answers; the rest (quota,
		// invalid) means the provider is not serving us.
		if strings.Contains(r.Message, "limit") || strings.Contains(r.Message, "quota") {
			return geoLocation{}, r.Message
		}
		return geoLocation{}, ""
	}
	return geoLocation{Country: r.Country, City: r.City, Lat: r.Lat, Lon: r.Lon}, ""
}

// locateZXInc reads ip.zxinc.org's answer. It is UTF-8 JSON with a `country`
// field shaped `国家–省–市` (en dash, U+2013), no coordinates and no rate
// limit, which makes it the dependable fallback where ip-api.com is blocked.
func locateZXInc(body []byte) (geoLocation, string) {
	var r struct {
		Code int `json:"code"`
		Data struct {
			Country string `json:"country"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return geoLocation{}, ""
	}
	if r.Code != 0 {
		// Non-zero means "no data for this IP", not a quota answer: keep
		// falling through instead of backing the provider off.
		return geoLocation{}, ""
	}
	parts := strings.FieldsFunc(r.Data.Country, func(rc rune) bool { return rc == '–' || rc == '—' })
	if len(parts) == 0 {
		return geoLocation{}, ""
	}
	country := strings.TrimSpace(parts[0])
	if country == "" {
		return geoLocation{}, ""
	}
	loc := geoLocation{Country: country}
	if len(parts) > 1 {
		loc.City = strings.TrimSpace(parts[len(parts)-1])
	}
	return loc, ""
}

// locateTaobao reads ip.taobao.com's answer; code 4 is "over max qps".
func locateTaobao(body []byte) (geoLocation, string) {
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Country string `json:"country"`
			City    string `json:"city"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return geoLocation{}, ""
	}
	if r.Code != 0 {
		if r.Code == 4 {
			return geoLocation{}, fmt.Sprintf("code 4: %s", truncateString(r.Msg, 80))
		}
		return geoLocation{}, ""
	}
	// The provider answers "XX" for unknown, which is not a country.
	if r.Data.Country == "" || r.Data.Country == "XX" {
		return geoLocation{}, ""
	}
	return geoLocation{Country: r.Data.Country, City: r.Data.City}, ""
}
