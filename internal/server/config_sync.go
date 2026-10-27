package server

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Hot-reload sync table.
//
// diffConfig classifies changed fields as hot/static, but classification
// alone never applied anything: the old onReload only synced crypto keys
// and CSRF, so "hot" fields like rate limits, JWT, SIEM or log level were
// silently ignored. Every hot token below has either a live Apply hook or
// is documented live-read (Apply == nil means request paths already read
// it per-request under configMu — no hook needed, but the token stays
// listed so the status matrix stays truthful).

type reloadMode string

const (
	reloadHot    reloadMode = "hot"
	reloadStatic reloadMode = "static"
)

type reloadGroup struct {
	Token string
	Mode  reloadMode
	// Apply syncs already-constructed runtime state. Nil = live-read.
	Apply func(s *Server) error
}

// reloadGroups is the single source of truth shared by onReload dispatch
// and GET /config/reload-status. Tokens must match diffConfig outputs.
func reloadGroups() []reloadGroup {
	live := func(string) func(*Server) error { return nil }
	_ = live
	return []reloadGroup{
		{Token: "server.port", Mode: reloadStatic},
		{Token: "server.beacon_key", Mode: reloadHot, Apply: (*Server).syncRegSecrets},
		{Token: "crypto.key", Mode: reloadStatic},
		{Token: "server.jwt_secret", Mode: reloadHot, Apply: (*Server).syncJWTSecret},
		{Token: "crypto.loot_key", Mode: reloadHot, Apply: (*Server).syncCryptoKeys},
		{Token: "crypto.extc2_key", Mode: reloadHot, Apply: (*Server).syncCryptoKeys},
		{Token: "crypto.csrf_key", Mode: reloadHot, Apply: (*Server).syncCSRFSecret},
		{Token: "crypto.totp_key", Mode: reloadHot},
		// Sections added to diffConfig when they were found missing from it: plugins is
		// the security-relevant one (trust keys, require_signed), backup retention and
		// schedule are read live, and the WebRTC/Protocol listeners bind at startup.
		{Token: "plugins", Mode: reloadHot, Apply: (*Server).applyPluginTrust},
		{Token: "backup", Mode: reloadHot},
		{Token: "webrtc", Mode: reloadStatic},
		{Token: "protocol", Mode: reloadStatic},
		{Token: "crypto.update_signing_key", Mode: reloadHot},
		// Read live per hot-update (performHotUpdate consults the current config), so
		// no hook is needed - but the token must exist: without it a change was
		// reported as "unknown token (diffConfig/table drift)" and the reload was
		// recorded as a failure instead of an applied change.
		{Token: "crypto.require_release_signature", Mode: reloadHot},
		{Token: "crypto.backup_key", Mode: reloadStatic},
		{Token: "crypto.max_decrypted_payload_size", Mode: reloadHot},
		{Token: "database.driver", Mode: reloadStatic},
		{Token: "database.path", Mode: reloadStatic},
		{Token: "database.dsn", Mode: reloadStatic},
		{Token: "database.pool", Mode: reloadHot, Apply: (*Server).syncDBPool},
		{Token: "logging.level", Mode: reloadHot, Apply: (*Server).syncLogLevel},
		{Token: "server.tls", Mode: reloadStatic},
		{Token: "server.mtls", Mode: reloadStatic},
		{Token: "server.pprof_addr", Mode: reloadStatic},
		{Token: "server.data_dir", Mode: reloadStatic},
		{Token: "server.listeners", Mode: reloadStatic},
		{Token: "auth", Mode: reloadStatic},
		{Token: "server.trusted_proxies", Mode: reloadHot, Apply: (*Server).syncTrustedProxies},
		{Token: "server.operator_allowed_cidrs", Mode: reloadHot},
		{Token: "server.operator_mtls", Mode: reloadStatic},
		{Token: "server.allowed_origins", Mode: reloadHot},
		{Token: "server.cookie_domain", Mode: reloadHot, Apply: (*Server).syncJWTSecret},
		{Token: "server.require_tls_for_auth", Mode: reloadHot, Apply: (*Server).syncJWTSecret},
		{Token: "server.enable_metrics", Mode: reloadHot},
		{Token: "server.socks_listen_host", Mode: reloadHot},
		{Token: "server.dns_obscure", Mode: reloadHot},
		{Token: "server.auto_recon", Mode: reloadHot},
		{Token: "server.lportfwd_enabled", Mode: reloadHot},
		{Token: "server.update_check", Mode: reloadHot},
		{Token: "server.vantage_points", Mode: reloadHot},
		{Token: "siem", Mode: reloadHot, Apply: (*Server).syncSIEM},
		{Token: "password_policy", Mode: reloadHot, Apply: (*Server).syncPasswordPolicy},
		{Token: "rate_limit.login", Mode: reloadHot},
		{Token: "rate_limit.beacon", Mode: reloadHot, Apply: (*Server).syncRateLimiters},
		{Token: "rate_limit.api", Mode: reloadHot, Apply: (*Server).syncRateLimiters},
		{Token: "rate_limit.extc2", Mode: reloadHot},
		{Token: "server.dns", Mode: reloadStatic},
		{Token: "server.grpc", Mode: reloadStatic},
		{Token: "malleable", Mode: reloadHot},
		{Token: "server.geoip_enabled", Mode: reloadHot},
		{Token: "server.cleanup_retention_days", Mode: reloadHot},
		{Token: "server.reg_secret_orphan_ttl_hours", Mode: reloadHot},
		{Token: "server.host", Mode: reloadStatic},
		{Token: "server.offline_threshold", Mode: reloadStatic},
		{Token: "server.session_max_age_hours", Mode: reloadStatic},
		{Token: "server.tcp", Mode: reloadStatic},
		{Token: "server.smb", Mode: reloadStatic},
		{Token: "server.icmp", Mode: reloadStatic},
		{Token: "server.udp", Mode: reloadStatic},
		{Token: "server.quic", Mode: reloadStatic},
		{Token: "server.ssh", Mode: reloadStatic},
		{Token: "implant", Mode: reloadHot},
		{Token: "security", Mode: reloadHot},
		{Token: "monitoring", Mode: reloadHot},
		{Token: "roe", Mode: reloadHot},
		{Token: "ai", Mode: reloadHot},
		{Token: "socks", Mode: reloadHot},
		{Token: "integrations", Mode: reloadHot},
		{Token: "server.tls_fingerprint", Mode: reloadHot, Apply: (*Server).syncTLSFingerprint},
	}
}

// reloadOutcome records the last reload for /config/reload-status.
type reloadOutcome struct {
	At       time.Time         `json:"at"`
	Changed  []string          `json:"changed"`
	Applied  []string          `json:"applied"`
	Failed   map[string]string `json:"failed,omitempty"`
	Rejected []string          `json:"rejected_static,omitempty"`
}

// --- Apply hooks (all idempotent) ---

// Apply hooks run on the reload goroutine *after* the CopyFrom window closed,
// so each one snapshots the fields it needs under the read lock: a settings
// save can rewrite the same sections concurrently.

func (s *Server) syncCryptoKeys() error {
	s.configMu.RLock()
	lootKey, extc2Key := s.cfg.Crypto.LootKey, s.cfg.Crypto.ExtC2Key
	s.configMu.RUnlock()
	crypto.InitLootEncryption(lootKey)
	crypto.InitExtC2Encryption(extc2Key)
	return nil
}

func (s *Server) syncCSRFSecret() error {
	return middleware.InitCSRFSecret(s.cfg)
}

// syncJWTSecret reinstalls the signing key (with rotation grace) and the
// cookie/TLS flags derived from it. Callers: server.jwt_secret,
// server.cookie_domain, server.require_tls_for_auth.
func (s *Server) syncJWTSecret() error {
	return middleware.InitJWTSecret(s.cfg, "")
}

// syncRegSecrets rebuilds the v3 registration-secret store from a rotated
// master beacon key. WARNING: secrets sealed under the old master become
// unreadable, so already-deployed implants fail auth until regenerated —
// rotation is intentionally disruptive and always audited.
func (s *Server) syncRegSecrets() error {
	s.configMu.RLock()
	beaconKey := s.cfg.Server.BeaconKey
	s.configMu.RUnlock()
	master, err := hex.DecodeString(beaconKey)
	if err != nil || len(master) == 0 {
		return fmt.Errorf("invalid server.beacon_key, keeping previous store")
	}
	s.regSecrets = crypto.NewRegSecretStore(master)
	slog.Warn("v3 registration secret store rebuilt from rotated beacon key; previously deployed implants must be regenerated")
	return nil
}

func (s *Server) syncRateLimiters() error {
	s.configMu.RLock()
	beaconLimit, beaconWindow := s.cfg.RateLimit.Beacon.Limit, s.cfg.RateLimit.Beacon.Window
	apiCapacity, apiRate := s.cfg.RateLimit.API.Capacity, s.cfg.RateLimit.API.Rate
	apiWhitelist := s.cfg.RateLimit.API.Whitelist
	s.configMu.RUnlock()
	if s.rateLimiter != nil {
		s.rateLimiter.SetLimits(beaconLimit, time.Duration(beaconWindow)*time.Second)
	}
	if s.apiRateLimiter != nil {
		s.apiRateLimiter.SetCapacityRate(apiCapacity, apiRate)
		s.apiRateLimiter.SetWhitelist(apiWhitelist)
	}
	return nil
}

func (s *Server) syncSIEM() error {
	s.configMu.RLock()
	enabled, url, token, actions := s.cfg.SIEM.Enabled, s.cfg.SIEM.URL, s.cfg.SIEM.Token, s.cfg.SIEM.Actions
	s.configMu.RUnlock()
	if !enabled || url == "" {
		if s.siem != nil {
			s.siem.UpdateConfig(false, "", "", "")
		}
		return nil
	}
	if s.siem == nil {
		s.siem = NewSIEMWebhook(s, url, token, actions)
		if s.siem != nil {
			s.siem.ReloadRules()
		}
		return nil
	}
	s.siem.UpdateConfig(true, url, token, actions)
	return nil
}

func (s *Server) syncLogLevel() error {
	// Apply hooks run after the reloader released configMu (CopyFrom window),
	// so read the level under the lock like every other request path: a
	// settings save can mutate it concurrently.
	s.configMu.RLock()
	level := s.cfg.Logging.Level
	s.configMu.RUnlock()
	if !SetLogLevel(level) {
		return fmt.Errorf("unknown logging.level %q, keeping previous", level)
	}
	return nil
}

func (s *Server) syncTrustedProxies() error {
	if s.router == nil {
		return nil
	}
	s.configMu.RLock()
	proxies := append([]string(nil), s.cfg.Server.TrustedProxies...)
	s.configMu.RUnlock()
	if len(proxies) > 0 {
		if err := s.router.SetTrustedProxies(proxies); err != nil {
			return fmt.Errorf("invalid trusted_proxies: %w", err)
		}
		middleware.SetTrustedProxyIPs(proxies)
	} else {
		_ = s.router.SetTrustedProxies(nil) // nil never errors: only a CIDR list can fail validation
		middleware.SetTrustedProxyIPs(nil)
	}
	return nil
}

func (s *Server) syncPasswordPolicy() error {
	s.configMu.RLock()
	bcryptCost := s.cfg.PasswordPolicy.BcryptCost
	s.configMu.RUnlock()
	if bcryptCost > 0 {
		middleware.SetBcryptCost(bcryptCost)
	}
	return nil
}

func (s *Server) syncDBPool() error {
	if s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	s.configMu.RLock()
	driver := s.cfg.Database.Driver
	maxOpen, maxIdle, maxLifetime := s.cfg.Server.DBMaxOpenConns, s.cfg.Server.DBMaxIdleConns, s.cfg.Server.DBConnMaxLifetime
	s.configMu.RUnlock()
	// SQLite is a single-writer database: a pool larger than 1 only
	// manufactures SQLITE_BUSY contention under beacon load. Clamp here so a
	// hot-reloaded postgres-oriented template cannot break it at runtime.
	if driver == "sqlite" || driver == "" {
		if maxOpen > 1 {
			slog.Warn("Refusing to raise sqlite connection pool above 1 (single writer)",
				"requested", maxOpen)
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		return nil
	}
	if maxOpen > 0 {
		sqlDB.SetMaxOpenConns(maxOpen)
	}
	if maxIdle > 0 {
		sqlDB.SetMaxIdleConns(maxIdle)
	}
	if maxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(maxLifetime)
	}
	return nil
}

func (s *Server) syncTLSFingerprint() error {
	s.initTLSFingerprint()
	return nil
}

var (
	lastReloadMu sync.Mutex
	lastReload   *reloadOutcome
)

func recordReloadOutcome(o *reloadOutcome) {
	lastReloadMu.Lock()
	defer lastReloadMu.Unlock()
	lastReload = o
}

// handleReloadStatus reports the hot/static matrix plus the last reload.
// Agents and auditors use it to tell "applied live" from "needs restart".
// Mechanism distinguishes hook-synced state ("hook") from per-request
// live reads ("live-read"), both of which take effect without restart.
func (s *Server) handleReloadStatus(c *gin.Context) {
	groups := reloadGroups()
	out := make([]gin.H, 0, len(groups))
	for _, g := range groups {
		mech := "restart"
		if g.Mode == reloadHot {
			mech = "live-read"
			if g.Apply != nil {
				mech = "hook"
			}
		}
		out = append(out, gin.H{"token": g.Token, "mode": string(g.Mode), "mechanism": mech})
	}
	lastReloadMu.Lock()
	last := lastReload
	lastReloadMu.Unlock()
	respond(c, gin.H{"groups": out, "last_reload": last})
}
