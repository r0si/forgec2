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

// loginGeoPerRequest bounds how many lookups one panel request may enqueue.
// The panel renders up to 50 rows plus a 24h rollup; queueing the whole page
// would keep the background worker busy for minutes and could trip a
// provider's rate limit on the first load.
const loginGeoPerRequest = 8

// loginGeoQueueMax bounds the pending work. A login log covering a large fleet
// of source IPs must not grow an unbounded channel.
const loginGeoQueueMax = 512

// loginGeoSpacing is the minimum gap between two provider calls. ip.taobao.com
// enforces roughly one request per second (`code:4` when exceeded), and ip-api
// limits per minute; spacing the worker keeps both happy.
const loginGeoSpacing = 1100 * time.Millisecond

// loginGeoFailedProviderTTL is how long a provider that could not answer is
// skipped. Without it every resolution retried the unreachable provider first
// (the demo host cannot reach ip-api.com), paying its connection timeout each
// time.
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

// cachedLoginCountry reads the persisted cache without any outbound call.
func (s *Server) cachedLoginCountry(ctx context.Context, ip string) (country, city string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", ""
	}
	if isPrivateIP(ip) {
		// A loopback/LAN address has no country; the panel names this category
		// instead of showing a missing value.
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

// loginCountry answers from the cache and, on a miss, queues the IP for the
// background worker. It never blocks on a provider: the panel shows the IP
// immediately and the country appears on the next refresh.
func (s *Server) loginCountry(ctx context.Context, ip string) (country, city string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", ""
	}
	if isPrivateIP(ip) {
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
	s.enqueueLoginGeo(ip)
	return "", ""
}

// enqueueLoginGeo starts the worker once and hands it the IP without blocking.
func (s *Server) enqueueLoginGeo(ip string) {
	s.loginGeoOnce.Do(func() {
		s.loginGeoCh = make(chan string, loginGeoQueueMax)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.loginGeoWorker()
		}()
	})
	select {
	case s.loginGeoCh <- ip:
	default:
		// Queue full: dropping is correct. The next panel load asks again, and
		// blocking here would put the drop on the request path instead.
	}
}

// loginGeoWorker resolves queued IPs one at a time, spaced out so a provider's
// per-second quota is respected, and stops with the server context.
func (s *Server) loginGeoWorker() {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Deduplicate: several rows and the rollup can enqueue the same IP in one
	// request.
	queued := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return
		case ip := <-s.loginGeoCh:
			if queued[ip] {
				continue
			}
			queued[ip] = true
			if country, _ := s.cachedLoginCountry(ctx, ip); country != "" {
				delete(queued, ip)
				continue
			}
			country, city := s.resolveLoginGeo(ctx, ip)
			if country != "" {
				row := db.LoginGeo{IP: ip, Country: country, City: city, ResolvedAt: time.Now()}
				if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
					slog.Debug("Login geo cache write failed", "ip", ip, "err", err)
				}
			}
			// The marker only dedupes a burst; a failed lookup must be
			// retryable on a later panel load, so it is released here.
			delete(queued, ip)
			select {
			case <-ctx.Done():
				return
			case <-time.After(loginGeoSpacing):
			}
		}
	}
}

// parseIPAPI reads ip-api.com's answer. The bool marks a provider-level refusal
// (rate limit / quota), which backs the provider off instead of just missing.
func parseIPAPI(body []byte) (string, string, bool) {
	var r struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Country string `json:"country"`
		City    string `json:"city"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", "", false
	}
	if r.Status != "success" {
		// "private range"/"reserved range" are answers; the rest (quota,
		// invalid) means the provider is not serving us.
		busy := strings.Contains(r.Message, "limit") || strings.Contains(r.Message, "quota")
		return "", "", busy
	}
	return r.Country, r.City, false
}

// parseTaobao reads ip.taobao.com's answer; code 4 is "over max qps".
func parseTaobao(body []byte) (string, string, bool) {
	var r struct {
		Code int `json:"code"`
		Data struct {
			Country string `json:"country"`
			City    string `json:"city"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", "", false
	}
	if r.Code != 0 {
		return "", "", r.Code == 4
	}
	// The provider answers "XX" for unknown, which is not a country.
	if r.Data.Country == "" || r.Data.Country == "XX" {
		return "", "", false
	}
	return r.Data.Country, r.Data.City, false
}

// resolveLoginGeo tries the providers in order and returns the first answer.
//
// ip-api.com is the international default (and the one the beacon path uses),
// but it is unreachable from some networks — the demo host reaches neither it
// nor other Western providers, while ip.taobao.com answers there. Trying both
// keeps the feature useful in either network instead of silently reporting
// "unknown" everywhere.
func (s *Server) resolveLoginGeo(ctx context.Context, ip string) (country, city string) {
	type provider struct {
		name string
		url  string
		// parse extracts (country, city) from a 200 body; empty country means
		// "this provider had no answer", which falls through to the next one.
		parse func(body []byte) (string, string, bool)
	}
	providers := []provider{
		{
			name:  "ip-api.com",
			url:   "http://ip-api.com/json/" + url.PathEscape(ip) + "?fields=country,city,status,message",
			parse: parseIPAPI,
		},
		{
			name:  "ip.taobao.com",
			url:   "https://ip.taobao.com/outGetIpInfo?accessKey=alibaba-inc&ip=" + url.PathEscape(ip),
			parse: parseTaobao,
		},
	}

	for _, p := range providers {
		if !loginGeoProviderUsable(p.name) {
			continue
		}
		// Per-provider deadline, independent of the shared 30s client timeout:
		// a blackholed provider (ip-api.com from this network) would otherwise
		// stall the worker for half a minute per IP.
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, p.url, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			cancel()
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
		cancel()
		if readErr != nil {
			continue
		}
		if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
			slog.Debug("Login geo provider throttled", "provider", p.name, "ip", ip, "status", status)
			markLoginGeoProviderDown(p.name)
			continue
		}
		if status != http.StatusOK {
			continue
		}
		country, city, busy := p.parse(body)
		if busy {
			slog.Debug("Login geo provider refused the lookup", "provider", p.name, "ip", ip)
			markLoginGeoProviderDown(p.name)
			continue
		}
		if country != "" {
			return country, city
		}
	}
	return "", ""
}
