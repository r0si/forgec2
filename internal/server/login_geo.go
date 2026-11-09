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
// enforces roughly one request per second on the shared `alibaba-inc` key
// (`code:4` when exceeded, observed even at 1.1s spacing), and ip-api limits
// per minute; spacing the worker at 2s keeps all of them happy.
const loginGeoSpacing = 2 * time.Second

// loginGeoFailedProviderTTL is how long a provider that could not answer is
// skipped. Without it every resolution retried the unreachable provider first
// (the demo host cannot reach ip-api.com), paying its connection timeout each
// time. Two minutes keeps a recoverable outage from blacking out countries for
// the rest of the hour.
const loginGeoFailedProviderTTL = 2 * time.Minute

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

func markLoginGeoProviderDown(name, reason string) {
	loginGeoProviderDown.mu.Lock()
	already := false
	if at, ok := loginGeoProviderDown.at[name]; ok && time.Since(at) < loginGeoFailedProviderTTL {
		already = true
	}
	loginGeoProviderDown.at[name] = time.Now()
	loginGeoProviderDown.mu.Unlock()
	if !already {
		// Warn once per window, not per lookup: a provider that is blocked or
		// throttled is worth seeing at default log level, because the symptom
		// otherwise is "countries are always unknown" with no explanation.
		slog.Warn("Login geo provider unavailable; countries will stay unknown until it recovers",
			"provider", name, "reason", reason, "retry_after", loginGeoFailedProviderTTL)
	}
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
		// Distinguishes "the operator turned it off" from "the worker is not
		// running" in a support log: both look like countries never appear.
		loginGeoDisabledLog.Do(func() {
			slog.Info("Login geo lookups disabled; sign-in countries stay unresolved",
				"flag", "server.geoip_enabled")
		})
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
		// One line per process tells an operator the pipeline is alive and how
		// many IPs are waiting; per-IP logging would flood a page load.
		loginGeoQueuedLog.Do(func() {
			slog.Info("Login geo: uncached sign-in IP queued for background resolution",
				"ip", ip, "spacing", loginGeoSpacing)
		})
	default:
		// Queue full: dropping is correct. The next panel load asks again, and
		// blocking here would put the drop on the request path instead.
	}
}

// loginGeoQueuedLog keeps the "worker is alive" line to one per process.
var (
	loginGeoQueuedLog   sync.Once
	loginGeoDisabledLog sync.Once
)

// loginGeoWorker resolves queued IPs one at a time, spaced out so a provider's
// per-second quota is respected, and stops with the server context.
func (s *Server) loginGeoWorker() {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	slog.Info("Login geo worker started", "spacing", loginGeoSpacing, "queue", loginGeoQueueMax)
	// Deduplicate: several rows and the rollup can enqueue the same IP in one
	// request.
	queued := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			slog.Info("Login geo worker stopped")
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
					// A write failure means the country never appears no matter
					// how often the panel refreshes, so it must be visible.
					slog.Warn("Login geo cache write failed", "ip", ip, "err", err)
				} else {
					// Once per IP per week: the useful "it worked" evidence.
					slog.Info("Login geo resolved", "ip", ip, "country", country, "city", city)
				}
			} else {
				slog.Debug("Login geo could not resolve an IP", "ip", ip)
			}
			// The marker only dedupes a burst; a failed lookup must be
			// retryable on a later panel load, so it is released here.
			delete(queued, ip)
			select {
			case <-ctx.Done():
				slog.Info("Login geo worker stopped")
				return
			case <-time.After(loginGeoSpacing):
			}
		}
	}
}

// parseIPAPI reads ip-api.com's answer. A non-empty refusal means the provider
// is not serving us (rate limit / quota), which backs it off instead of just
// missing one lookup.
func parseIPAPI(body []byte) (country, city, refusal string) {
	var r struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Country string `json:"country"`
		City    string `json:"city"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", "", ""
	}
	if r.Status != "success" {
		// "private range"/"reserved range" are answers; the rest (quota,
		// invalid) means the provider is not serving us.
		if strings.Contains(r.Message, "limit") || strings.Contains(r.Message, "quota") {
			return "", "", r.Message
		}
		return "", "", ""
	}
	return r.Country, r.City, ""
}

// parseTaobao reads ip.taobao.com's answer; code 4 is "over max qps".
func parseTaobao(body []byte) (country, city, refusal string) {
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Country string `json:"country"`
			City    string `json:"city"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", "", ""
	}
	if r.Code != 0 {
		if r.Code == 4 {
			return "", "", fmt.Sprintf("code 4: %s", truncateString(r.Msg, 80))
		}
		return "", "", ""
	}
	// The provider answers "XX" for unknown, which is not a country.
	if r.Data.Country == "" || r.Data.Country == "XX" {
		return "", "", ""
	}
	return r.Data.Country, r.Data.City, ""
}

// parseZXInc reads ip.zxinc.org's answer. It is UTF-8 JSON with a `country`
// field shaped `国家–省–市` (en dash, U+2013) and no rate limit, which makes it
// the dependable fallback on networks that cannot reach ip-api.com.
func parseZXInc(body []byte) (country, city, refusal string) {
	var r struct {
		Code int `json:"code"`
		Data struct {
			Country string `json:"country"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", "", ""
	}
	if r.Code != 0 {
		// Non-zero means "no data for this IP", not a quota answer: keep
		// falling through instead of backing the provider off.
		return "", "", ""
	}
	parts := strings.FieldsFunc(r.Data.Country, func(rc rune) bool { return rc == '–' || rc == '—' })
	if len(parts) == 0 {
		return "", "", ""
	}
	country = strings.TrimSpace(parts[0])
	if country == "" {
		return "", "", ""
	}
	if len(parts) > 1 {
		city = strings.TrimSpace(parts[len(parts)-1])
	}
	return country, city, ""
}

// resolveLoginGeo tries the providers in order and returns the first answer.
//
// ip-api.com is the international default (and the one the beacon path uses),
// but it is unreachable from some networks — the demo host reaches neither it
// nor other Western providers. ip.zxinc.org answers there, in UTF-8, without a
// shared quota; ip.taobao.com is kept as a last resort because its public
// `alibaba-inc` key enforces about 1 QPS globally. Trying all three keeps the
// feature useful in either network instead of silently reporting "unknown".
func (s *Server) resolveLoginGeo(ctx context.Context, ip string) (country, city string) {
	type provider struct {
		name string
		url  string
		// parse extracts (country, city) from a 200 body; empty country means
		// "this provider had no answer", which falls through to the next one.
		// A non-empty refusal means the provider is rejecting us (rate limit),
		// which marks it unusable for a while instead of just missing data.
		parse func(body []byte) (string, string, string)
	}
	providers := []provider{
		{
			name:  "ip-api.com",
			url:   "http://ip-api.com/json/" + url.PathEscape(ip) + "?fields=country,city,status,message",
			parse: parseIPAPI,
		},
		{
			name:  "ip.zxinc.org",
			url:   "https://ip.zxinc.org/api.php?type=json&ip=" + url.PathEscape(ip),
			parse: parseZXInc,
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
			markLoginGeoProviderDown(p.name, err.Error())
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
			markLoginGeoProviderDown(p.name, fmt.Sprintf("HTTP %d", status))
			continue
		}
		if status != http.StatusOK {
			continue
		}
		country, city, refusal := p.parse(body)
		if refusal != "" {
			slog.Debug("Login geo provider refused the lookup", "provider", p.name, "ip", ip, "reason", refusal)
			markLoginGeoProviderDown(p.name, refusal)
			continue
		}
		if country != "" {
			return country, city
		}
	}
	return "", ""
}
