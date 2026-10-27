package server

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/fsnotify/fsnotify"
)

type ConfigReloader struct {
	cfg  *config.Config
	path string
	// liveMu is the server's configMu: r.cfg aliases the live s.cfg pointer,
	// so diffConfig must hold it while reading — settings saves now mutate
	// that struct under the write lock, and the manual reload endpoint makes
	// this path reachable from an operator request. Nil in standalone tests
	// that never wire a server.
	liveMu   *sync.RWMutex
	watcher  *fsnotify.Watcher
	mu       sync.Mutex
	running  bool
	onReload func(*config.Config, []string)
}

func NewConfigReloader(cfg *config.Config, path string, onReload func(*config.Config, []string)) *ConfigReloader {
	return &ConfigReloader{
		cfg:      cfg,
		path:     path,
		onReload: onReload,
	}
}

// TrackLiveConfig tells the reloader which lock guards the live config the
// reloader's r.cfg aliases (the server's configMu before the first reload
// re-points it). diffConfig reads r.cfg, so it must hold that lock for
// reading; settings saves mutate the same struct under the write lock.
func (r *ConfigReloader) TrackLiveConfig(mu *sync.RWMutex) {
	r.liveMu = mu
}

func (r *ConfigReloader) diff(newCfg *config.Config) (hotReloadable, staticOnly []string) {
	if r.liveMu != nil {
		r.liveMu.RLock()
		defer r.liveMu.RUnlock()
	}
	return diffConfig(r.cfg, newCfg)
}

func (r *ConfigReloader) Start() error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil
	}
	r.running = true
	r.mu.Unlock()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	r.watcher = watcher

	// Watch the parent directory, not the file: editors and config
	// writers (including our own Save) often replace the file atomically
	// via rename, which orphans a file watch and silently drops every later
	// event. Directory watches survive renames; monitor() filters by name.
	watchDir := filepath.Dir(r.path)
	if err := watcher.Add(watchDir); err != nil {
		watcher.Close()
		return err
	}

	slog.Info("Config reloader started", "path", r.path)

	go r.monitor(watcher)

	return nil
}

func (r *ConfigReloader) Stop() {
	r.mu.Lock()
	r.running = false
	if r.watcher != nil {
		r.watcher.Close()
		r.watcher = nil
	}
	r.mu.Unlock()
	slog.Info("Config reloader stopped")
}

// monitor consumes events from the watcher captured by Start. The watcher is
// passed in rather than re-read from r.watcher: Stop() clears that field, and
// a monitor goroutine that lost the scheduling race to Stop read nil and
// crashed on the first select with a nil pointer dereference.
func (r *ConfigReloader) monitor(w *fsnotify.Watcher) {
	var debounceMu sync.Mutex
	var debounce *time.Timer
	schedule := func() {
		debounceMu.Lock()
		defer debounceMu.Unlock()
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(ConfigReloadDebounce, func() {
			r.reload()
		})
	}

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}

			if !sameConfigFile(event.Name, r.path) {
				continue
			}

			// Write/Create/Rename/Remove/Chmod all reschedule: renames and
			// removals cover atomic-save editors, and a transient half-write
			// simply fails validation and keeps the current config.
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove|fsnotify.Chmod) == 0 {
				continue
			}

			schedule()

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			slog.Error("Config watcher error", "error", err)
		}
	}
}

// sameConfigFile matches the watched config path against an fsnotify
// event name across symlinks, relative spellings and case (Windows).
func sameConfigFile(eventName, watchPath string) bool {
	if eventName == watchPath {
		return true
	}
	absEvent, err1 := filepath.Abs(eventName)
	absWatch, err2 := filepath.Abs(watchPath)
	if err1 != nil || err2 != nil {
		return false
	}
	if absEvent == absWatch {
		return true
	}
	resolvedEvent, err1 := filepath.EvalSymlinks(absEvent)
	resolvedWatch, err2 := filepath.EvalSymlinks(absWatch)
	if err1 != nil || err2 != nil {
		// Unresolvable (deleted mid-rename, dangling link): fall back to
		// the absolute spelling instead of comparing two empty strings.
		return absEvent == absWatch
	}
	return resolvedEvent == resolvedWatch
}

func (r *ConfigReloader) reload() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running {
		return
	}

	slog.Info("Detected config change, reloading", "path", r.path)

	data, err := os.ReadFile(r.path)
	if err != nil {
		slog.Error("Failed to read config file", "error", err)
		return
	}

	newCfg := config.DefaultConfig()
	if err := newCfg.LoadFromData(data); err != nil {
		slog.Error("Failed to parse config file", "error", err)
		return
	}
	// Re-apply the FORGEC2_* overrides: a reload must not silently drop a value the
	// environment supplied at startup (data_dir, db path, crypto keys, the plugin
	// trust policy). Load and this path have to agree on what "the config" means.
	config.ApplyEnvOverrides(newCfg)

	if err := newCfg.Validate(); err != nil {
		slog.Error("New config failed validation, keeping current config", "error", err)
		return
	}

	changed, staticOnly := r.diff(newCfg)
	if len(changed) == 0 && len(staticOnly) == 0 {
		slog.Info("Config file changed but no values differ, skipping reload")
		return
	}
	if len(staticOnly) > 0 {
		slog.Warn("Config file changed with non-hot-reloadable fields (restart required), rejecting reload", "fields", strings.Join(staticOnly, ", "))
		return
	}
	slog.Info("Config fields changed", "fields", strings.Join(changed, ", "))

	if r.onReload != nil {
		r.onReload(newCfg, changed)
	}
	r.cfg = newCfg

	slog.Info("Config reloaded successfully", "changed_fields", len(changed))
}

func (r *ConfigReloader) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	slog.Info("Manual config reload triggered", "path", r.path)

	data, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	newCfg := config.DefaultConfig()
	if err := newCfg.LoadFromData(data); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	// Same reason as the watcher path: the environment keeps applying after a reload.
	config.ApplyEnvOverrides(newCfg)

	if err := newCfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	changed, staticOnly := r.diff(newCfg)
	if len(staticOnly) > 0 {
		return fmt.Errorf("config has non-hot-reloadable fields that require restart: %s", strings.Join(staticOnly, ", "))
	}

	if r.onReload != nil {
		r.onReload(newCfg, changed)
	}
	r.cfg = newCfg

	slog.Info("Manual config reload completed", "changed_fields", len(changed))
	return nil
}

func diffConfig(old, new *config.Config) (hotReloadable []string, staticOnly []string) {
	if old.Server.Port != new.Server.Port {
		staticOnly = append(staticOnly, "server.port")
	}
	// The profiling listener is bound once at startup (and is meant to exist
	// only while an operator is measuring), so a live change cannot be applied.
	if old.Server.PprofAddr != new.Server.PprofAddr {
		staticOnly = append(staticOnly, "server.pprof_addr")
	}
	if old.Server.BeaconKey != new.Server.BeaconKey {
		hotReloadable = append(hotReloadable, "server.beacon_key")
	}
	// crypto.key only gates the ECDH-vs-XOR mode check at startup and
	// crypto.backup_key is baked into the backup manager at construction
	// (no live re-key path): rotation needs a restart, so reject loudly
	// instead of pretending to apply it.
	if old.Crypto.Key != new.Crypto.Key {
		staticOnly = append(staticOnly, "crypto.key")
	}
	if old.Server.JWTSecret != new.Server.JWTSecret {
		hotReloadable = append(hotReloadable, "server.jwt_secret")
	}
	if old.Crypto.LootKey != new.Crypto.LootKey {
		hotReloadable = append(hotReloadable, "crypto.loot_key")
	}
	if old.Crypto.ExtC2Key != new.Crypto.ExtC2Key {
		hotReloadable = append(hotReloadable, "crypto.extc2_key")
	}
	if old.Crypto.CsrfKey != new.Crypto.CsrfKey {
		hotReloadable = append(hotReloadable, "crypto.csrf_key")
	}
	if old.Crypto.TotpKey != new.Crypto.TotpKey {
		hotReloadable = append(hotReloadable, "crypto.totp_key")
	}
	// Signing key is read live per hot-update: no Apply step needed.
	if old.Crypto.UpdateSigningKey != new.Crypto.UpdateSigningKey {
		hotReloadable = append(hotReloadable, "crypto.update_signing_key")
	}
	if old.Crypto.RequireReleaseSignature != new.Crypto.RequireReleaseSignature {
		hotReloadable = append(hotReloadable, "crypto.require_release_signature")
	}
	if old.Crypto.RequireReleaseSignature != new.Crypto.RequireReleaseSignature {
		hotReloadable = append(hotReloadable, "crypto.require_release_signature")
	}
	if old.Crypto.BackupKey != new.Crypto.BackupKey {
		staticOnly = append(staticOnly, "crypto.backup_key")
	}
	if old.Database.Driver != new.Database.Driver {
		staticOnly = append(staticOnly, "database.driver")
	}
	if old.Database.DSN != new.Database.DSN {
		staticOnly = append(staticOnly, "database.dsn")
	}
	if old.Server.DBMaxOpenConns != new.Server.DBMaxOpenConns ||
		old.Server.DBMaxIdleConns != new.Server.DBMaxIdleConns ||
		old.Server.DBConnMaxLifetime != new.Server.DBConnMaxLifetime {
		hotReloadable = append(hotReloadable, "database.pool")
	}
	if old.Database.Path != new.Database.Path {
		staticOnly = append(staticOnly, "database.path")
	}
	if old.Logging.Level != new.Logging.Level {
		hotReloadable = append(hotReloadable, "logging.level")
	}
	if old.Server.TLSEnabled != new.Server.TLSEnabled || old.Server.CertFile != new.Server.CertFile || old.Server.KeyFile != new.Server.KeyFile {
		staticOnly = append(staticOnly, "server.tls")
	}
	if !reflect.DeepEqual(old.SIEM, new.SIEM) {
		hotReloadable = append(hotReloadable, "siem")
	}
	if !reflect.DeepEqual(old.PasswordPolicy, new.PasswordPolicy) {
		hotReloadable = append(hotReloadable, "password_policy")
	}
	if !reflect.DeepEqual(old.RateLimit.Login, new.RateLimit.Login) {
		hotReloadable = append(hotReloadable, "rate_limit.login")
	}
	if !reflect.DeepEqual(old.RateLimit.API, new.RateLimit.API) {
		hotReloadable = append(hotReloadable, "rate_limit.api")
	}
	if !reflect.DeepEqual(old.RateLimit.Beacon, new.RateLimit.Beacon) {
		hotReloadable = append(hotReloadable, "rate_limit.beacon")
	}
	if !reflect.DeepEqual(old.RateLimit.ExtC2, new.RateLimit.ExtC2) {
		hotReloadable = append(hotReloadable, "rate_limit.extc2")
	}
	if old.Server.DNSEnabled != new.Server.DNSEnabled || old.Server.DNSDomain != new.Server.DNSDomain || old.Server.DNSAddr != new.Server.DNSAddr {
		staticOnly = append(staticOnly, "server.dns")
	}
	if old.Server.GRPCEnabled != new.Server.GRPCEnabled || old.Server.GRPCAddr != new.Server.GRPCAddr {
		staticOnly = append(staticOnly, "server.grpc")
	}
	// Malleable profile rotation and other operator-tunable fields were
	// previously not in the whitelist at all, so edits like rotating the
	// profile were silently ignored ("no values differ, skipping" → C2 profile
	// rotation never took effect). Every field is live-read per request, so
	// the whole block is hot-reloadable.
	if !reflect.DeepEqual(old.Malleable, new.Malleable) {
		hotReloadable = append(hotReloadable, "malleable")
	}
	if old.Server.GeoIPEnabled != new.Server.GeoIPEnabled {
		hotReloadable = append(hotReloadable, "server.geoip_enabled")
	}
	if old.Server.CleanupRetentionDays != new.Server.CleanupRetentionDays {
		hotReloadable = append(hotReloadable, "server.cleanup_retention_days")
	}
	if old.Server.RegSecretOrphanTTLHours != new.Server.RegSecretOrphanTTLHours {
		hotReloadable = append(hotReloadable, "server.reg_secret_orphan_ttl_hours")
	}
	// --- Static-only fields not yet tracked above ---
	if old.Server.Host != new.Server.Host {
		staticOnly = append(staticOnly, "server.host")
	}
	if old.Server.OfflineThreshold != new.Server.OfflineThreshold {
		staticOnly = append(staticOnly, "server.offline_threshold")
	}
	if old.Server.SessionMaxAgeHours != new.Server.SessionMaxAgeHours {
		staticOnly = append(staticOnly, "server.session_max_age_hours")
	}
	if old.Server.TCPEnabled != new.Server.TCPEnabled || old.Server.TCPAddr != new.Server.TCPAddr {
		staticOnly = append(staticOnly, "server.tcp")
	}
	if old.Server.SMBEnabled != new.Server.SMBEnabled || old.Server.SMBPipe != new.Server.SMBPipe {
		staticOnly = append(staticOnly, "server.smb")
	}
	if old.Server.ICMPEnabled != new.Server.ICMPEnabled || old.Server.ICMPAddr != new.Server.ICMPAddr {
		staticOnly = append(staticOnly, "server.icmp")
	}
	if old.Server.UDPEnabled != new.Server.UDPEnabled || old.Server.UDPAddr != new.Server.UDPAddr {
		staticOnly = append(staticOnly, "server.udp")
	}
	if old.Server.QUICEnabled != new.Server.QUICEnabled || old.Server.QUICAddr != new.Server.QUICAddr {
		staticOnly = append(staticOnly, "server.quic")
	}
	if old.Server.SSHEnabled != new.Server.SSHEnabled || old.Server.SSHPort != new.Server.SSHPort ||
		old.Server.SSHAddr != new.Server.SSHAddr || old.Server.SSHHostKey != new.Server.SSHHostKey ||
		old.Server.SSHUser != new.Server.SSHUser || old.Server.SSHPassword != new.Server.SSHPassword ||
		old.Server.SSHKeyAuth != new.Server.SSHKeyAuth {
		staticOnly = append(staticOnly, "server.ssh")
	}
	if old.Server.ClientCAFile != new.Server.ClientCAFile || old.Server.RequireClientCert != new.Server.RequireClientCert {
		staticOnly = append(staticOnly, "server.mtls")
	}
	if old.Server.DataDir != new.Server.DataDir {
		staticOnly = append(staticOnly, "server.data_dir")
	}
	if !reflect.DeepEqual(old.Listeners, new.Listeners) {
		staticOnly = append(staticOnly, "server.listeners")
	}
	if !reflect.DeepEqual(old.Auth, new.Auth) {
		staticOnly = append(staticOnly, "auth")
	}
	// Whole-section comparisons for operator-tunable blocks. Previously edits
	// here (implant minimums, two-man rule, monitoring thresholds, RoE, AI
	// provider, SOCKS egress) fell through both lists and were silently
	// ignored. They apply to future builds/tasks on reload (CopyFrom now
	// carries every section).
	if !reflect.DeepEqual(old.Implant, new.Implant) {
		hotReloadable = append(hotReloadable, "implant")
	}
	if !reflect.DeepEqual(old.Security, new.Security) {
		hotReloadable = append(hotReloadable, "security")
	}
	if !reflect.DeepEqual(old.Monitoring, new.Monitoring) {
		hotReloadable = append(hotReloadable, "monitoring")
	}
	if !reflect.DeepEqual(old.Roe, new.Roe) {
		hotReloadable = append(hotReloadable, "roe")
	}
	if !reflect.DeepEqual(old.AI, new.AI) {
		hotReloadable = append(hotReloadable, "ai")
	}
	if !reflect.DeepEqual(old.Socks, new.Socks) {
		hotReloadable = append(hotReloadable, "socks")
	}
	if !reflect.DeepEqual(old.Integrations, new.Integrations) {
		hotReloadable = append(hotReloadable, "integrations")
	}
	if !reflect.DeepEqual(old.Server.AllowedOrigins, new.Server.AllowedOrigins) {
		hotReloadable = append(hotReloadable, "server.allowed_origins")
	}
	if !reflect.DeepEqual(old.Server.TrustedProxies, new.Server.TrustedProxies) {
		hotReloadable = append(hotReloadable, "server.trusted_proxies")
	}
	// Operator-plane CIDRs are read live per request: no Apply step needed.
	if !reflect.DeepEqual(old.Server.OperatorAllowedCIDRs, new.Server.OperatorAllowedCIDRs) {
		hotReloadable = append(hotReloadable, "server.operator_allowed_cidrs")
	}
	// Operator mTLS touches the TLS listener (ClientAuth) and the cached CA
	// pool: rotation needs a restart, so reject loudly instead of half-applying.
	if old.Server.OperatorMTLS != new.Server.OperatorMTLS || old.Server.OperatorClientCAFile != new.Server.OperatorClientCAFile {
		staticOnly = append(staticOnly, "server.operator_mtls")
	}
	if old.Server.CookieDomain != new.Server.CookieDomain {
		hotReloadable = append(hotReloadable, "server.cookie_domain")
	}
	if old.Server.RequireTLSForAuth != new.Server.RequireTLSForAuth {
		hotReloadable = append(hotReloadable, "server.require_tls_for_auth")
	}
	if old.Server.EnableMetrics != new.Server.EnableMetrics {
		hotReloadable = append(hotReloadable, "server.enable_metrics")
	}
	if old.Server.SocksListenHost != new.Server.SocksListenHost {
		hotReloadable = append(hotReloadable, "server.socks_listen_host")
	}
	if old.Server.DNSObscure != new.Server.DNSObscure {
		hotReloadable = append(hotReloadable, "server.dns_obscure")
	}
	if !reflect.DeepEqual(old.Server.AutoRecon, new.Server.AutoRecon) {
		hotReloadable = append(hotReloadable, "server.auto_recon")
	}
	if old.Server.LPortFwdEnabled != new.Server.LPortFwdEnabled {
		hotReloadable = append(hotReloadable, "server.lportfwd_enabled")
	}
	if old.Server.UpdateCheckEnabled != new.Server.UpdateCheckEnabled || old.Server.UpdateCheckRepo != new.Server.UpdateCheckRepo {
		hotReloadable = append(hotReloadable, "server.update_check")
	}
	if !reflect.DeepEqual(old.Server.VantagePoints, new.Server.VantagePoints) {
		hotReloadable = append(hotReloadable, "server.vantage_points")
	}
	if old.Crypto.MaxDecryptedPayloadSize != new.Crypto.MaxDecryptedPayloadSize {
		hotReloadable = append(hotReloadable, "crypto.max_decrypted_payload_size")
	}
	if !reflect.DeepEqual(old.TLSFingerprint, new.TLSFingerprint) {
		hotReloadable = append(hotReloadable, "server.tls_fingerprint")
	}
	if len(staticOnly) > 0 {
		slog.Warn("Config file changed with non-hot-reloadable fields (restart required)", "fields", staticOnly)
	}
	return
}
