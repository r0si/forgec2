package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// loginGeoTTL is how long a resolved country is trusted. A login IP that moves
// country mid-engagement is not a real scenario, so a week keeps the panel
// accurate without hammering any provider.
const loginGeoTTL = 7 * 24 * time.Hour

// loginGeoPerRequest bounds how many uncached lookups one panel request may
// trigger. The panel renders up to 50 rows; resolving the whole page would add
// seconds of latency and could trip a provider's rate limit on the first load.
const loginGeoPerRequest = 5

// loginCountry returns the country for an IP from the persisted cache,
// resolving it on demand (bounded) when the cache misses.
// Resolution is opt-in behind server.geoip_enabled, exactly like the beacon
// GeoIP path: a deployment that has not opted in must not make surprise
// outbound requests just because someone opened the audit page.
func (s *Server) loginCountry(ctx context.Context, ip string) (country, city string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", ""
	}
	if isPrivateIP(ip) {
		// A loopback/LAN address has no country; the panel shows the IP and
		// this resolves to "local" in the UI rather than "unknown".
		return "local", ""
	}

	if country, city := s.cachedLoginCountry(ctx, ip); country != "" {
		return country, city
	}

	s.configMu.RLock()
	enabled := s.cfg != nil && s.cfg.Server.GeoIPEnabled
	s.configMu.RUnlock()
	if !enabled {
		return "", ""
	}

	country, city = s.resolveLoginGeo(ctx, ip)
	if country == "" {
		return "", ""
	}
	row := db.LoginGeo{IP: ip, Country: country, City: city, ResolvedAt: time.Now()}
	// Best-effort: a cache write failure only costs another lookup later.
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		slog.Debug("Login geo cache write failed", "ip", ip, "err", err)
	}
	return country, city
}

// cachedLoginCountry reads the persisted cache without any outbound call. It
// exists so a page beyond the per-request lookup budget still shows the
// countries that were already resolved.
func (s *Server) cachedLoginCountry(ctx context.Context, ip string) (country, city string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", ""
	}
	if isPrivateIP(ip) {
		return "local", ""
	}
	var cached db.LoginGeo
	if err := s.db.WithContext(ctx).Where("ip = ?", ip).First(&cached).Error; err != nil {
		return "", ""
	}
	if time.Since(cached.ResolvedAt) >= loginGeoTTL {
		return "", ""
	}
	return cached.Country, cached.City
}

// loginGeoFailedProviderTTL is how long a provider that could not be reached
// is skipped. Without it every panel load retried the unreachable provider
// first (the demo host cannot reach ip-api.com), paying its connection
// timeout on each page.
const loginGeoFailedProviderTTL = 5 * time.Minute

var loginGeoProviderDown = struct {
	mu sync.Mutex
	at map[string]time.Time
}{at: make(map[string]time.Time)}

func loginGeoProviderUsable(name string) bool {
	loginGeoProviderDown.mu.Lock()
	defer loginGeoProviderDown.mu.Unlock()
	at, ok := loginGeoProviderDown.at[name]
	return !ok || time.Since(at) >= loginGeoFailedProviderTTL
}

func markLoginGeoProviderDown(name string) {
	loginGeoProviderDown.mu.Lock()
	defer loginGeoProviderDown.mu.Unlock()
	loginGeoProviderDown.at[name] = time.Now()
}

// resolveLoginGeo tries the providers in order and returns the first answer.
//
// ip-api.com is the international default (and the one the beacon path uses),
// but it is unreachable from some networks — the demo host reaches neither it
// nor other Western providers, while ip.taobao.com answers there. Trying both
// keeps the feature useful in either network instead of silently returning
// "unknown" everywhere.
func (s *Server) resolveLoginGeo(ctx context.Context, ip string) (country, city string) {
	type provider struct {
		name string
		url  string
		// parse extracts (country, city) from a 200 body; empty country means
		// "this provider had no answer", which falls through to the next one.
		parse func(body []byte) (string, string)
	}
	providers := []provider{
		{
			name: "ip-api.com",
			url:  "http://ip-api.com/json/" + url.PathEscape(ip) + "?fields=country,city,status",
			parse: func(body []byte) (string, string) {
				var r struct {
					Status  string `json:"status"`
					Country string `json:"country"`
					City    string `json:"city"`
				}
				if json.Unmarshal(body, &r) != nil || r.Status != "success" {
					return "", ""
				}
				return r.Country, r.City
			},
		},
		{
			name: "ip.taobao.com",
			url:  "https://ip.taobao.com/outGetIpInfo?accessKey=alibaba-inc&ip=" + url.PathEscape(ip),
			parse: func(body []byte) (string, string) {
				var r struct {
					Code int `json:"code"`
					Data struct {
						Country string `json:"country"`
						City    string `json:"city"`
					} `json:"data"`
				}
				if json.Unmarshal(body, &r) != nil || r.Code != 0 {
					return "", ""
				}
				// The provider answers "XX" for unknown, which is not a country.
				if r.Data.Country == "" || r.Data.Country == "XX" {
					return "", ""
				}
				return r.Data.Country, r.Data.City
			},
		},
	}

	for _, p := range providers {
		if !loginGeoProviderUsable(p.name) {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
		if err != nil {
			continue
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			slog.Debug("Login geo provider unreachable", "provider", p.name, "ip", ip, "err", err)
			markLoginGeoProviderDown(p.name)
			continue
		}
		// The status is read before the body: a 429 here is the provider's
		// rate limit, and decoding its error page as a location is exactly the
		// silent-empty-field defect the beacon path already fixed.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		status := resp.StatusCode
		_ = resp.Body.Close()
		if readErr != nil || status != http.StatusOK {
			slog.Debug("Login geo provider answered without a location", "provider", p.name, "ip", ip, "status", status)
			continue
		}
		if country, city := p.parse(body); country != "" {
			return country, city
		}
	}
	return "", ""
}
