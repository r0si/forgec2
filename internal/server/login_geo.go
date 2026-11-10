package server

import (
	"context"
	"log/slog"
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
	if !isCountryName(cached.Country) {
		// A region-only row from before the provider fix ("北美地区") is a
		// miss: the worker must re-resolve it so the panel can show a country
		// and the next-level place.
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
		// One line per process tells an operator the pipeline is alive; per-IP
		// logging would flood a page load.
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

// resolveLoginGeo is the login log's view of the shared provider chain: it
// only needs country and city, and leaves coordinates to the agent path.
func (s *Server) resolveLoginGeo(ctx context.Context, ip string) (country, city string) {
	loc, ok := s.resolveGeoLocation(ctx, ip)
	if !ok {
		return "", ""
	}
	return loc.Country, loc.City
}
