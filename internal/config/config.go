package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/buildinfo"
	"gopkg.in/yaml.v3"
)

// Config holds all configuration for ForgeC2
type Config struct {
	mu         sync.RWMutex `yaml:"-"`
	ConfigPath string       `yaml:"-"` // absolute path to the config file, set on Load
	Plugins    struct {
		// TrustedKeys are hex Ed25519 public keys that may sign plugin
		// packages (manifest `signature` over the package `digest`).
		TrustedKeys []string `yaml:"trusted_keys"`
		// RequireSigned refuses unsigned plugin packages. Off by default so
		// the bundled plugins keep loading; turn it on once keys are set.
		RequireSigned bool `yaml:"require_signed"`
		// MaxConcurrent caps simultaneously running plugin processes across
		// the server (0 = built-in default).
		MaxConcurrent int `yaml:"max_concurrent"`
		// MaxCPUSeconds bounds the CPU time one native plugin run may burn
		// across its process tree before the tree is killed (0 = built-in
		// default; the wall-clock ceiling still applies).
		MaxCPUSeconds int `yaml:"max_cpu_seconds"`
		// MaxWriteMB bounds how many MiB one native plugin run may write
		// across its process tree before the tree is killed (0 = built-in
		// default; the wall-clock ceiling and CPU budget still apply).
		MaxWriteMB int `yaml:"max_write_mb"`
	} `yaml:"plugins"`
	Backup struct {
		// Retain is how many encrypted .fbk snapshots to keep (0 = 7).
		Retain int `yaml:"retain"`
		// Schedule is hourly/daily/weekly/monthly (default daily).
		Schedule string `yaml:"schedule"`
	} `yaml:"backup"`
	Server struct {
		Port                 int    `yaml:"port"`
		Host                 string `yaml:"host"`
		TLSEnabled           bool   `yaml:"tls_enabled"`
		CertFile             string `yaml:"cert_file"`
		KeyFile              string `yaml:"key_file"`
		ClientCAFile         string `yaml:"client_ca_file"`      // mTLS: CA cert file for client verification
		RequireClientCert    bool   `yaml:"require_client_cert"` // mTLS: require client certificate for beacon auth
		JWTSecret            string `yaml:"jwt_secret"`
		TCPEnabled           bool   `yaml:"tcp_enabled"`
		TCPAddr              string `yaml:"tcp_addr"`
		SMBEnabled           bool   `yaml:"smb_enabled"`
		SMBPipe              string `yaml:"smb_pipe"`
		DataDir              string `yaml:"data_dir"`
		DNSEnabled           bool   `yaml:"dns_enabled"`
		DNSDomain            string `yaml:"dns_domain"`
		DNSAddr              string `yaml:"dns_addr"`
		GRPCEnabled          bool   `yaml:"grpc_enabled"`
		GRPCAddr             string `yaml:"grpc_addr"`
		ICMPEnabled          bool   `yaml:"icmp_enabled"`
		ICMPAddr             string `yaml:"icmp_addr"`
		UDPEnabled           bool   `yaml:"udp_enabled"`
		UDPAddr              string `yaml:"udp_addr"`
		QUICEnabled          bool   `yaml:"quic_enabled"`
		QUICAddr             string `yaml:"quic_addr"`
		OfflineThreshold     int    `yaml:"offline_threshold"`      // seconds
		SessionMaxAgeHours   int    `yaml:"session_max_age_hours"`  // JWT expiry
		CleanupRetentionDays int    `yaml:"cleanup_retention_days"` // auto-purge cutoff
		// RegSecretOrphanTTLHours bounds how long an unbound v3 registration
		// secret stays usable. 0 = built-in default (168h = 7 days). A bound
		// secret (already used by a deployed agent) never expires: it is that
		// agent's long-term credential.
		RegSecretOrphanTTLHours int `yaml:"reg_secret_orphan_ttl_hours"`
		// AuditRetentionDays bounds audit_logs/opsec_history/circuit events
		// separately: forensic timelines outlive operational data. Floor 90,
		// default 365. The one-click maintenance purge never touches these
		// tables regardless of this setting.
		AuditRetentionDays   int           `yaml:"audit_retention_days"`
		UpdateCheckRepo      string        `yaml:"update_check_repo"`       // GitHub repo for update checks (e.g. "owner/repo")
		UpdateCheckEnabled   bool          `yaml:"update_check_enabled"`    // OPT-IN: phone home to GitHub releases (default OFF for egress hygiene)
		VantagePoints        []string      `yaml:"vantage_points"`          // external proxy URLs for circuit breaker probing
		SSHEnabled           bool          `yaml:"ssh_enabled"`             // enable SSH transport listener
		SSHPort              int           `yaml:"ssh_port"`                // SSH listener port (default 2222)
		LPortFwdEnabled      bool          `yaml:"lportfwd_enabled"`        // allow agents to open tunneled local port forwards (default true, set below)
		SSHAddr              string        `yaml:"ssh_addr"`                // SSH listener addr (default :ssh_port)
		SSHHostKey           string        `yaml:"ssh_host_key"`            // path to SSH host key (auto-generated if missing)
		SSHUser              string        `yaml:"ssh_user"`                // SSH user for agent auth
		SSHPassword          string        `yaml:"ssh_password"`            // SSH password (empty = any password or key-only)
		SSHKeyAuth           bool          `yaml:"ssh_key_auth"`            // allow public key authentication
		GeoIPEnabled         bool          `yaml:"geoip_enabled"`           // enable GeoIP lookup via ip-api.com (opt-in)
		AllowedOrigins       []string      `yaml:"allowed_origins"`         // allowed WebSocket/CORS origins (default: localhost,127.0.0.1,::1)
		TrustedProxies       []string      `yaml:"trusted_proxies"`         // trusted reverse proxy IPs/CIDRs for X-Forwarded-For (empty = trust none, use direct client IP)
		OperatorAllowedCIDRs []string      `yaml:"operator_allowed_cidrs"`  // operator-plane IP allowlist for login + authenticated API/UI (empty = disabled; beacons/payloads/phishing/health unaffected)
		OperatorMTLS         bool          `yaml:"operator_mtls"`           // require verified client certs on the operator plane (needs TLS + operator_client_ca_file; beacons unaffected)
		OperatorClientCAFile string        `yaml:"operator_client_ca_file"` // PEM CA bundle verifying operator client certs (independent from beacon mTLS CA)
		CookieDomain         string        `yaml:"cookie_domain"`           // domain for session/CSRF cookies (for cross-origin deployments)
		BeaconKey            string        `yaml:"beacon_key"`              // optional pre-shared key for agent beacon auth (X-Beacon-Key header)
		RequireTLSForAuth    bool          `yaml:"require_tls_for_auth"`    // require TLS before issuing session cookies (strongly recommended in production)
		EnableMetrics        bool          `yaml:"enable_metrics"`          // expose /metrics (default false; requires auth when enabled)
		SocksListenHost      string        `yaml:"socks_listen_host"`       // bind host for SOCKS/rportfwd (default 127.0.0.1)
		DBMaxOpenConns       int           `yaml:"db_max_open_conns"`       // max open connections for PostgreSQL pool (default 1; sqlite must stay 1)
		DBMaxIdleConns       int           `yaml:"db_max_idle_conns"`       // max idle connections for PostgreSQL pool (default 1; sqlite must stay 1)
		DBConnMaxLifetime    time.Duration `yaml:"db_conn_max_lifetime"`    // max connection lifetime for PostgreSQL pool (default 30m)
		DNSObscure           bool          `yaml:"dns_obscure"`             // XOR-obscure DNS C2 fragments (must match implant DNSObscure)
		AutoRecon            []string      `yaml:"auto_recon"`              // task types queued on first check-in (empty = disabled)
		// PprofAddr exposes Go's /debug/pprof endpoints ("" = disabled, the
		// default). Loopback only: the server refuses to start with a
		// non-loopback address, because those endpoints expose process
		// internals and accept no authentication. See docs/CAPACITY.md.
		PprofAddr string `yaml:"pprof_addr"`
	} `yaml:"server"`

	// Roe is the rules-of-engagement gate: tasks whose command/data/path
	// references an IP outside AllowCIDRs (when set) or inside DenyCIDRs are
	// refused, and likewise for AllowDomains/DenyDomains (suffix match).
	Roe struct {
		Enabled      bool     `yaml:"enabled"`
		AllowCIDRs   []string `yaml:"allow_cidrs"`
		DenyCIDRs    []string `yaml:"deny_cidrs"`
		AllowDomains []string `yaml:"allow_domains"`
		DenyDomains  []string `yaml:"deny_domains"`
	} `yaml:"roe"`

	Database struct {
		Path   string `yaml:"path"`
		Driver string `yaml:"driver"` // "sqlite" (default) or "postgres"
		DSN    string `yaml:"dsn"`    // PostgreSQL connection string (e.g. "host=localhost user=forgec2 password=... dbname=forgec2 port=5432 sslmode=disable")
	} `yaml:"database"`

	Implant struct {
		DefaultInterval     int    `yaml:"default_interval"` // seconds
		MinInterval         int    `yaml:"min_interval"`     // OPSEC: minimum allowed beacon interval in seconds (0 = no minimum, default 5)
		DefaultJitter       int    `yaml:"default_jitter"`   // percent
		MinJitter           int    `yaml:"min_jitter"`       // OPSEC: minimum allowed jitter percent (0 = no minimum, default 10)
		DefaultUA           string `yaml:"default_user_agent"`
		DefaultSkipTLS      bool   `yaml:"default_skip_tls"`
		DefaultWorkingStart string `yaml:"default_working_start"` // HH:MM local time (empty = disabled)
		DefaultWorkingEnd   string `yaml:"default_working_end"`   // HH:MM local time (empty = disabled)
		DefaultWorkingTZ    string `yaml:"default_working_tz"`    // IANA timezone (e.g. "America/New_York"), empty = UTC
		GoProxy             string `yaml:"goproxy"`               // optional GOPROXY for payload builds when env GOPROXY is unset
	} `yaml:"implant"`

	Auth struct {
		PasswordHash  string `yaml:"password_hash"`    // bcrypt hash, set on first run
		DefaultPasswd string `yaml:"default_password"` // plaintext; used only on first boot if password_hash is empty
		// LogGeneratedPassword controls whether the first-boot banner prints the
		// generated admin password in plaintext. Default true so an operator who
		// misses the one-shot banner is not locked out; set false to keep the
		// secret out of logs (the reset-password tool still works).
		LogGeneratedPassword *bool `yaml:"log_generated_password"`
		// SessionMaxConcurrent caps live sessions per user (0 = unlimited).
		// Overflow evicts the oldest session first (audited).
		SessionMaxConcurrent int `yaml:"session_max_concurrent"`
	} `yaml:"auth"`

	PasswordPolicy struct {
		MinLength     int  `yaml:"min_length"`     // minimum password length (default 8)
		RequireUpper  bool `yaml:"require_upper"`  // require uppercase letter (default true)
		RequireLower  bool `yaml:"require_lower"`  // require lowercase letter (default true)
		RequireDigit  bool `yaml:"require_digit"`  // require digit (default true)
		RequireSymbol bool `yaml:"require_symbol"` // require special character (default false)
		MaxAge        int  `yaml:"max_age_days"`   // force password rotation every N days (0 = disabled)
		BcryptCost    int  `yaml:"bcrypt_cost"`    // bcrypt hash cost (0 = default 10, min 4, max 31)
	} `yaml:"password_policy"`

	Crypto struct {
		Key                     string `yaml:"key"`                        // 32-byte hex key for XOR encryption, or "ecdh:" for ECDH+AES-256-GCM (empty=disabled)
		LootKey                 string `yaml:"loot_key"`                   // REQUIRED: 32-byte hex key for AES-256-GCM loot encryption (independent of the JWT secret)
		ExtC2Key                string `yaml:"extc2_key"`                  // REQUIRED: 32-byte hex key for AES-256-GCM ExtC2 channel encryption (independent)
		BackupKey               string `yaml:"backup_key"`                 // REQUIRED: 32-byte hex key for encrypted .fbk backups (independent)
		TotpKey                 string `yaml:"totp_key"`                   // REQUIRED: 32-byte hex key for SMTP / SSH-redirector credentials (independent; name kept for compatibility)
		CsrfKey                 string `yaml:"csrf_key"`                   // REQUIRED: 32-byte hex key for CSRF token binding (independent)
		MaxDecryptedPayloadSize int    `yaml:"max_decrypted_payload_size"` // max bytes for decrypted beacon body (0 = default 10MB)
		UpdateSigningKey        string `yaml:"update_signing_key"`         // OPTIONAL: 32-byte hex Ed25519 pubkey; when set, hot updates require a detached "<checksum>.sig" release signature (empty = checksum-only + warning unless require_release_signature)
		RequireReleaseSignature bool   `yaml:"require_release_signature"`  // when true, refuse hot updates if update_signing_key is empty or the detached .sig fails verification (default false for upgrade compat; set true in production)
		// SessionCipher selects the beacon session AEAD: "" / "aes-gcm" (default)
		// or "chacha20" (ChaCha20-Poly1305). Agreed at handshake via the MAC'd
		// auth response field sc; both suites use a 12-byte nonce so the wire
		// framing is identical. Changing the value requires agents that parse sc.
		SessionCipher string `yaml:"session_cipher"`
	} `yaml:"crypto"`

	Malleable struct {
		Enabled     bool              `yaml:"enabled"`
		ProfileName string            `yaml:"profile_name"` // preset name: default, microsoft, google_analytics, cloudflare_cdn, akamai
		StatusCode  int               `yaml:"status_code"`
		ContentType string            `yaml:"content_type"`
		Headers     map[string]string `yaml:"headers"`
		Prepend     string            `yaml:"prepend"`
		Append      string            `yaml:"append"`
		// Request-side transforms: applied by the agent to the OUTGOING beacon
		// body (and as request headers) and stripped by the server on inbound.
		// Kept separate from the response-side Prepend/Append so operators can
		// shape upload and download traffic independently (Cobalt-Strike-style
		// http-get/http-post prepend/append).
		RequestPrepend string            `yaml:"request_prepend"`
		RequestAppend  string            `yaml:"request_append"`
		RequestHeaders map[string]string `yaml:"request_headers"`
		// Placements: JSON array [{target,chain}] cover copies, e.g.
		// [{"target":"cookie:SESSION","chain":"base64"}].
		Placements string `yaml:"placements"`
		// RespJitterMs caps the random per-beacon reply delay (0..N ms) that
		// decorrelates response timing. 0 disables. Applies to beacon replies
		// on HTTP/TCP/UDP transports.
		RespJitterMs int `yaml:"resp_jitter_ms"`
	} `yaml:"malleable"`

	AI struct {
		Enabled bool `yaml:"enabled"`
		// Provider names are NOT listed here on purpose: the accepted set lives
		// in ai_providers.go and is queried with AIProviderNames(). A copied
		// list in a comment is exactly how the old drift bug stayed invisible.
		Provider string `yaml:"provider"`
		APIKey   string `yaml:"api_key"`
		Model    string `yaml:"model"`
		Endpoint string `yaml:"endpoint"` // optional, override the provider default; required for "custom"
		// AllowPrivateEndpoint and AllowedEndpoints relax the SSRF guard for
		// AI provider calls only, so an operator can point the assistant at a
		// local Ollama/vLLM or an air-gapped internal gateway. Off by default:
		// loopback and private addresses are rejected. Entries are matched
		// against the endpoint host:port. This deliberately has no UI toggle —
		// widening network reach is a config-file-level decision.
		AllowPrivateEndpoint  bool     `yaml:"allow_private_endpoint"`
		AllowedEndpoints      []string `yaml:"allowed_endpoints"`
		SystemPrompt          string   `yaml:"system_prompt"`
		EngagementNotes       string   `yaml:"engagement_notes"`         // persistent engagement memory injected into the system prompt
		MaxConversationTurns  int      `yaml:"max_conversation_turns"`   // 0 = unlimited (default)
		MaxToolRounds         int      `yaml:"max_tool_rounds"`          // 0 = unlimited (default)
		MaxDuplicateToolCalls int      `yaml:"max_duplicate_tool_calls"` // 0 = unlimited; else cap identical tool+args repeats
		AllowExecute          bool     `yaml:"allow_execute"`            // permit AI to run commands on agents (default false = safe)
		MaxRunsPerUser        int      `yaml:"max_runs_per_user"`        // active background runs per operator (default 2)
		MaxRunsPerTenant      int      `yaml:"max_runs_per_tenant"`      // active background runs per tenant (default 8)
		RunRetentionDays      int      `yaml:"run_retention_days"`       // durable event retention (default 30)
	} `yaml:"ai"`

	Logging struct {
		Level string `yaml:"level"` // debug, info, warn, error
	} `yaml:"logging"`

	SIEM struct {
		Enabled bool   `yaml:"enabled"`
		URL     string `yaml:"url"`     // webhook URL for SIEM event forwarding
		Token   string `yaml:"token"`   // optional bearer token for SIEM webhook
		Actions string `yaml:"actions"` // comma-separated action filters (empty = all)
	} `yaml:"siem"`

	Integrations struct {
		Slack SlackConfig `yaml:"slack"`
	} `yaml:"integrations"`

	Listeners ListenersConfig `yaml:"listeners"`

	// Allow external programs to write plain SOCKS5 operations server -> agent
	Socks struct {
		Enabled      bool        `yaml:"enabled"`
		AuthRequired bool        `yaml:"auth_required"`
		Users        []SocksUser `yaml:"users"`
		AllowedDests []string    `yaml:"allowed_destinations"`
	} `yaml:"socks"`

	TLSFingerprint TLSFingerprintConfig `yaml:"tls_fingerprint"`

	RateLimit struct {
		Login struct {
			MaxAttempts int      `yaml:"max_attempts"` // max login attempts per window
			Window      int      `yaml:"window"`       // window in seconds
			LockoutTime int      `yaml:"lockout_time"` // lockout duration in seconds
			Whitelist   []string `yaml:"whitelist"`    // whitelisted IPs
		} `yaml:"login"`
		API struct {
			Capacity  float64  `yaml:"capacity"`  // token bucket capacity (max burst)
			Rate      float64  `yaml:"rate"`      // tokens per second per user
			Whitelist []string `yaml:"whitelist"` // whitelisted IPs
		} `yaml:"api"`
		Beacon struct {
			Limit  int `yaml:"limit"`  // requests per window
			Window int `yaml:"window"` // window in seconds
		} `yaml:"beacon"`
		ExtC2 struct {
			Enabled    bool    `yaml:"enabled"`
			APIToken   string  `yaml:"api_token"`   // shared secret for extc2 API auth (empty = no auth)
			Rate       float64 `yaml:"rate"`        // requests per second per beacon ID
			Burst      int     `yaml:"burst"`       // max burst size
			CleanupAge int     `yaml:"cleanup_age"` // minutes to keep idle entries
		} `yaml:"extc2"`
	} `yaml:"rate_limit"`

	// Security holds operator-facing guardrails.
	Security struct {
		// RequireApproval enforces a two-man rule: task types flagged
		// RequiresApproval (irreversible / high-impact ops) are created
		// in "pending_approval" state and must be approved by an operator
		// DIFFERENT from the task creator before the beacon claims them.
		RequireApproval bool `yaml:"require_approval"`
	} `yaml:"security"`

	// Monitoring drives the metric alert bridge: threshold checks over the
	// Prometheus gauges that fire notifications + webhooks. Off by default —
	// alerting is an outbound-sensitive capability the operator opts into.
	Monitoring struct {
		AlertsEnabled   bool `yaml:"alerts_enabled"`    // master switch (default false)
		AgentsOnlineMin int  `yaml:"agents_online_min"` // alert when online <= this while agents exist
		TasksPendingMax int  `yaml:"tasks_pending_max"` // alert when pending tasks exceed this
		CooldownSeconds int  `yaml:"cooldown_seconds"`  // per-rule silence window (default 600)
		EvalSeconds     int  `yaml:"eval_seconds"`      // evaluation cadence (default 30)
		// Task-failure-rate rule: percentage of terminal tasks that failed
		// inside the look-back window. Requires min samples to avoid noise.
		TaskFailureRateMaxPct float64 `yaml:"task_failure_rate_max_pct"` // default 25
		FailureWindowMinutes  int     `yaml:"failure_window_minutes"`    // default 10
		FailureMinSamples     int     `yaml:"failure_min_samples"`       // default 20
	} `yaml:"monitoring"`

	// WebRTC drives the real-time remote-desktop stream (Pion + embedded TURN).
	// Off by default: enabling it opens a TURN listener (UDP by default), which
	// operators must deliberately expose on their network.
	WebRTC struct {
		Enabled bool `yaml:"enabled"`
		// TurnAddr is the UDP listen address for the embedded TURN/STUN server
		// (":3478" = all interfaces, port 3478).
		TurnAddr string `yaml:"turn_addr"`
		// PublicIP is the relay address advertised to peers. Empty = detect a
		// non-loopback local IPv4 at startup; set this for a NAT/public host.
		PublicIP string `yaml:"public_ip"`
		Realm    string `yaml:"realm"`
		// RelayMinPort / RelayMaxPort bound TURN relay allocations.
		RelayMinPort int `yaml:"relay_min_port"`
		RelayMaxPort int `yaml:"relay_max_port"`
		// SessionTTLSeconds bounds one live remote-desktop session (0 = 3600).
		SessionTTLSeconds int `yaml:"session_ttl_seconds"`
		// MaxSessions caps concurrent live sessions (0 = 32).
		MaxSessions int `yaml:"max_sessions"`
	} `yaml:"webrtc"`

	// Protocol governs the wire-protocol negotiation between the server and
	// the agents. The compatibility window decides how many protocol
	// versions older than the current one the server still serves, so a
	// protocol change can be rolled out gradually - or an old version can be
	// cut off immediately.
	Protocol struct {
		// CompatWindow is how many protocol versions older than the current
		// one the server still serves (0 = only the current version). The
		// window never serves a version whose envelope changed
		// incompatibly.
		CompatWindow int `yaml:"compat_window"`
	} `yaml:"protocol"`
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	cfg := &Config{}
	cfg.Server.Port = 8000
	// The console listens on every interface by default: a C2 server that is
	// only reachable from localhost cannot be operated from another host
	// (or from a container network) without editing the config first. The
	// console is protected by TLS + authentication, and an operator who wants
	// it local sets server.host to 127.0.0.1.
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.TLSEnabled = true // self-signed cert auto-generated on first run
	cfg.Server.RequireTLSForAuth = true
	cfg.Server.TCPEnabled = false
	cfg.Server.TCPAddr = ""
	cfg.Server.SMBEnabled = false
	cfg.Server.SMBPipe = "forgec2"
	cfg.Server.DNSEnabled = false
	cfg.Server.DNSDomain = ""
	cfg.Server.DNSAddr = ":53"
	cfg.Server.ICMPEnabled = false
	cfg.Server.ICMPAddr = "0.0.0.0"
	cfg.Server.UDPEnabled = false
	cfg.Server.UDPAddr = ":8899"
	cfg.Server.QUICEnabled = false
	cfg.Server.QUICAddr = ":4433"
	cfg.Server.SSHEnabled = false
	cfg.Server.SSHPort = 2222
	cfg.Server.SSHAddr = ""
	cfg.Server.SSHUser = "forgec2"
	cfg.Server.SSHPassword = ""
	cfg.Server.SSHKeyAuth = true
	cfg.Server.DataDir = "data"
	cfg.Server.SSHHostKey = filepath.Join(cfg.Server.DataDir, "ssh_host_key")
	cfg.Server.OfflineThreshold = 60
	cfg.Server.SessionMaxAgeHours = 24
	cfg.Server.CleanupRetentionDays = 30
	cfg.Server.RegSecretOrphanTTLHours = 168
	cfg.Server.AuditRetentionDays = 365
	// The default release source is the repository that actually publishes the
	// releases (build-stamped so a fork points at its own). A hard-coded
	// placeholder here silently disabled every update check: the GitHub API
	// answers 404 for an unknown repository and the checker treated that as
	// "no new release".
	cfg.Server.UpdateCheckRepo = buildinfo.Repo
	cfg.Server.UpdateCheckEnabled = false // update checks phone home to api.github.com — opt-in only
	cfg.Server.EnableMetrics = false
	cfg.Server.SocksListenHost = "127.0.0.1"
	cfg.Server.DBMaxOpenConns = 1
	cfg.Server.DBMaxIdleConns = 1
	// NOTE: sqlite is a single writer — the pool stays 1/1 (enforced by
	// Validate and syncDBPool). Postgres operators must set
	// server.db_max_open_conns / db_max_idle_conns explicitly.
	cfg.Server.DBConnMaxLifetime = 30 * time.Minute
	// lportfwd tunnels agent-local connections out through the teamserver.
	// On by default (mirrors rportfwd/socks availability); operators can set
	// server.lportfwd_enabled: false to refuse the task at creation time.
	cfg.Server.LPortFwdEnabled = true

	// Metric alert bridge: opt-in, conservative defaults.
	cfg.Monitoring.AlertsEnabled = false
	cfg.Monitoring.AgentsOnlineMin = 0
	cfg.Monitoring.TasksPendingMax = 100
	cfg.Monitoring.CooldownSeconds = 600
	cfg.Monitoring.EvalSeconds = 30
	cfg.Monitoring.TaskFailureRateMaxPct = 25
	cfg.Monitoring.FailureWindowMinutes = 10
	cfg.Monitoring.FailureMinSamples = 20

	// Live remote desktop (WebRTC + embedded TURN) is opt-in.
	cfg.WebRTC.Enabled = false
	cfg.WebRTC.TurnAddr = ":3478"
	cfg.WebRTC.PublicIP = ""
	cfg.WebRTC.Realm = "forgec2"
	cfg.WebRTC.RelayMinPort = 49152
	cfg.WebRTC.RelayMaxPort = 65535
	cfg.WebRTC.SessionTTLSeconds = 3600
	cfg.WebRTC.MaxSessions = 32

	// Secure by default: enforce the two-man rule for all task types flagged
	// dangerous (see server/tasktypes.go dangerousTaskTypes). Solo operators
	// can still approve their own tasks; set security.require_approval: false
	// to disable. The dangerous list now covers credential-access (creds,
	// mimikatz, kerberoast, dpapi_*) and destructive delete.
	cfg.Security.RequireApproval = true

	cfg.Database.Path = filepath.Join(cfg.Server.DataDir, "db/forgec2.db")
	cfg.Database.Driver = "sqlite"
	cfg.Server.CertFile = filepath.Join(cfg.Server.DataDir, "server.crt")
	cfg.Server.KeyFile = filepath.Join(cfg.Server.DataDir, "server.key")

	cfg.Implant.DefaultInterval = 5
	cfg.Implant.MinInterval = 5
	cfg.Implant.MinJitter = 10

	cfg.PasswordPolicy.MinLength = 8
	cfg.PasswordPolicy.RequireUpper = true
	cfg.PasswordPolicy.RequireLower = true
	cfg.PasswordPolicy.RequireDigit = true
	cfg.PasswordPolicy.RequireSymbol = false
	cfg.PasswordPolicy.MaxAge = 0
	cfg.Implant.DefaultJitter = 20
	cfg.Implant.DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"
	cfg.Implant.DefaultWorkingStart = ""
	cfg.Implant.DefaultWorkingEnd = ""
	cfg.Implant.DefaultWorkingTZ = ""

	cfg.Malleable.Enabled = false
	cfg.Malleable.StatusCode = 200
	cfg.Malleable.ContentType = "application/json"
	cfg.Malleable.RespJitterMs = 150
	cfg.Malleable.Headers = map[string]string{
		"Server": "nginx/1.24.0",
	}
	cfg.AI.Enabled = false
	cfg.AI.Provider = "deepseek"
	cfg.AI.Model = "deepseek-chat"
	cfg.AI.MaxConversationTurns = 0  // 0 = unlimited
	cfg.AI.MaxToolRounds = 0         // 0 = unlimited
	cfg.AI.MaxDuplicateToolCalls = 0 // 0 = unlimited
	cfg.AI.MaxRunsPerUser = 2
	cfg.AI.MaxRunsPerTenant = 8
	cfg.AI.RunRetentionDays = 30
	// Blank: handlers_ai.effectiveAISystemPrompt substitutes the built-in
	// operations prompt so new installs get tool-use rules without copying
	// a long string into every config.yaml.
	cfg.AI.SystemPrompt = ""
	cfg.TLSFingerprint.JARMEnabled = true
	cfg.TLSFingerprint.JARMRotate = "24h"
	cfg.TLSFingerprint.JA3Enabled = true
	cfg.TLSFingerprint.JA3Profile = "random"
	cfg.TLSFingerprint.JA3Rotate = "24h"

	cfg.Logging.Level = "info"

	// Secure default: ECDH+AES-256-GCM beacon encryption enabled. Plaintext
	// rejection is unconditional (v2 defines no plaintext frames), so
	// crypto.force_ecdh was removed as a compat no-op.
	cfg.Crypto.Key = "ecdh:"

	cfg.RateLimit.Login.MaxAttempts = 5
	cfg.RateLimit.Login.Window = 60
	cfg.RateLimit.Login.LockoutTime = 900
	cfg.RateLimit.Login.Whitelist = []string{}

	cfg.RateLimit.API.Capacity = 100
	cfg.RateLimit.API.Rate = 50
	cfg.RateLimit.API.Whitelist = []string{"127.0.0.1", "::1"}

	cfg.RateLimit.Beacon.Limit = 100
	cfg.RateLimit.Beacon.Window = 60

	cfg.Socks.Enabled = false
	cfg.Socks.AuthRequired = false
	cfg.Socks.Users = []SocksUser{}
	cfg.Socks.AllowedDests = []string{}

	cfg.RateLimit.ExtC2.Enabled = true
	cfg.RateLimit.ExtC2.Rate = 10
	cfg.RateLimit.ExtC2.Burst = 20
	cfg.RateLimit.ExtC2.CleanupAge = 30

	// Listener defaults
	cfg.Listeners.H2C.Enabled = false
	cfg.Listeners.H2C.Addr = ":8081"

	// Serve the previous protocol version for one release so a protocol
	// change does not black out the deployed agents overnight. Set
	// protocol.compat_window to 0 to serve only the current version.
	cfg.Protocol.CompatWindow = 1

	return cfg
}

// isWeakSecret returns true if the secret is commonly known, too short, or trivially guessable.
func isWeakSecret(s string) bool {
	if len(s) < 32 {
		return true
	}
	weak := []string{
		"forgec2_secret_key_change_this_in_production",
		"change_me", "changeme", "change_this", "secret", "password", "admin",
		"jwt_secret", "your_secret_here", "todo", "placeholder", "redacted",
		"replace_in_production", "replace_me",
		"0b0d003179038fb3742671c8242100f77f03be74aa69dbc38139fe1e500d9dfd",
	}
	lower := strings.ToLower(s)
	for _, w := range weak {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// ShouldLogGeneratedPassword reports whether the first-boot banner may print the
// generated admin password in plaintext. Defaults to true: the banner is the only
// place the one-shot credential is ever shown, so an operator who misses it would
// otherwise be locked out. Set auth.log_generated_password: false to keep the
// secret out of logs.
func (c *Config) ShouldLogGeneratedPassword() bool {
	return c.Auth.LogGeneratedPassword == nil || *c.Auth.LogGeneratedPassword
}

// isWeakDefaultPassword rejects trivially guessable first-boot admin
// passwords (e.g. the historical "Admin123!" example value). Operators must
// either choose a strong password or leave default_password empty so the
// server auto-generates a random one.
func isWeakDefaultPassword(s string) bool {
	if len(s) < 12 {
		return true
	}
	lower := strings.ToLower(s)
	weak := []string{
		"admin", "password", "changeme", "change_me", "123456", "12345678",
		"qwerty", "letmein", "welcome", "forgec2", "default", "test",
	}
	for _, w := range weak {
		if strings.Contains(lower, w) {
			return true
		}
	}
	hasUpper := false
	hasLower := false
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return !(hasUpper && hasLower && hasDigit)
}

// EnsureStorageKeys generates any missing independent storage key
// (loot/extc2/backup/credential/csrf) and persists the config when possible.
// Auto-generation previously happened only when the config file was missing,
// so the documented "cp config.example.yaml config.yaml" path produced a
// config that failed Validate() with five "key is required" errors. Keys
// supplied via FORGEC2_* env vars are left untouched (env wins), and a
// read-only config file is not fatal: the generated values are used for this
// process and a warning explains the consequence (data encrypted under an
// ephemeral key).
func (c *Config) EnsureStorageKeys() error {
	targets := []struct {
		name  string
		field *string
	}{
		{"loot key", &c.Crypto.LootKey},
		{"ExtC2 key", &c.Crypto.ExtC2Key},
		{"backup key", &c.Crypto.BackupKey},
		{"credential key", &c.Crypto.TotpKey},
		{"CSRF key", &c.Crypto.CsrfKey},
	}
	changed := false
	for _, t := range targets {
		if *t.field != "" {
			continue
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		*t.field = hex.EncodeToString(key)
		changed = true
		slog.Warn("Storage key auto-generated", "key", t.name,
			"hint", "set it explicitly in config.yaml or via the matching FORGEC2_* env var to keep it stable across restarts")
	}
	if !changed || c.ConfigPath == "" {
		return nil
	}
	if err := c.Save(c.ConfigPath); err != nil {
		slog.Warn("Storage keys generated but config could not be persisted; they stay valid for this process only",
			"path", c.ConfigPath, "err", err)
	}
	return nil
}

// splitAndTrim splits a comma-separated env value, dropping empty entries.
func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyDataDirEnv honours FORGEC2_DATA_DIR (used by the container images,
// where the persistent volume is /data but the default data_dir is relative).
// Paths that still hold their data-dir-derived defaults are re-pointed at the
// new root; explicit operator values are preserved.
func applyDataDirEnv(cfg *Config) {
	dir := strings.TrimSpace(os.Getenv("FORGEC2_DATA_DIR"))
	if dir == "" || dir == cfg.Server.DataDir {
		return
	}
	oldDir := cfg.Server.DataDir
	rebase := func(path *string, suffix string) {
		// Compare normalized forms: YAML on Windows commonly carries
		// "data/db/forgec2.db" while filepath.Join produces backslashes.
		if *path == "" || filepath.Clean(*path) == filepath.Clean(filepath.Join(oldDir, suffix)) {
			*path = filepath.Join(dir, suffix)
		}
	}
	rebase(&cfg.Database.Path, filepath.Join("db", "forgec2.db"))
	rebase(&cfg.Server.CertFile, "server.crt")
	rebase(&cfg.Server.KeyFile, "server.key")
	rebase(&cfg.Server.SSHHostKey, "ssh_host_key")
	cfg.Server.DataDir = dir
	slog.Info("server.data_dir overridden from environment", "data_dir", dir)
}

// Load loads config from file, creates default if not exists
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// create default config file
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return nil, err
			}
			// Fresh deployment: mint independent random storage keys now so
			// loot/ExtC2/backup/credential/CSRF primitives are cryptographically
			// isolated from the JWT secret (a JWT compromise must not decrypt
			// credentials, backups, or bind CSRF tokens).
			for _, gen := range []struct {
				field *string
				slogW string
			}{
				{&cfg.Server.JWTSecret, "JWT secret"},
				{&cfg.Crypto.LootKey, "loot key"},
				{&cfg.Crypto.ExtC2Key, "ExtC2 key"},
				{&cfg.Crypto.BackupKey, "backup key"},
				{&cfg.Crypto.TotpKey, "credential key"},
				{&cfg.Crypto.CsrfKey, "CSRF key"},
				{&cfg.Server.BeaconKey, "beacon key"},
			} {
				key := make([]byte, 32)
				if _, rerr := rand.Read(key); rerr != nil {
					return nil, rerr
				}
				*gen.field = hex.EncodeToString(key)
			}
			out, _ := yaml.Marshal(cfg)
			if err := os.WriteFile(path, out, 0600); err != nil {
				return nil, err
			}
			cfg.ConfigPath = path
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	// Silently ignoring an unknown key is the dangerous default for a security
	// product: a typo in a control name ("require_release_signature") is dropped and
	// the operator believes it is on. Warn with the exact paths - refuse would break
	// a config written by a newer build.
	if unknown := UnknownConfigKeys(data); len(unknown) > 0 {
		slog.Warn("config contains keys this build does not recognise; they were ignored",
			"path", path, "keys", formatUnknownKeys(unknown), "count", len(unknown))
	}
	cfg.ConfigPath = path

	// Every FORGEC2_* override, then the secret minting only a real startup may
	// perform. The hot-reload path calls ApplyEnvOverrides for the first half, which
	// is exactly what used to be missing there.
	// Order matters: minting saves the file, so it must run before the full override
	// set is applied or the save would copy environment secrets onto disk.
	applySecretEnvForMinting(cfg)
	if err := generateMissingSecrets(cfg, path); err != nil {
		return nil, err
	}
	ApplyEnvOverrides(cfg)

	return cfg, nil
}

// Endpoint returns the API endpoint for the configured provider
func (c *Config) AIEndpoint() string {
	if c.AI.Endpoint != "" {
		return c.AI.Endpoint
	}
	// Resolved from the provider registry. Returns "" only for providers that
	// deliberately have no vendor default ("custom"); callers must treat that
	// as a configuration error rather than guessing a host, because guessing
	// here is what used to send one vendor's key to another.
	return DefaultAIEndpoint(c.AI.Provider)
}

// validatePprofAddr rejects profiling addresses that are not loopback. The
// /debug/pprof endpoints have no authentication, so a routable bind is a
// configuration error at load time and again at bind time in internal/server
// (defense in depth: config.Validate runs before the server owns any socket).
func validatePprofAddr(addr string) error {
	if addr == "" {
		return nil
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		// "localhost:port" is the one non-literal form net.Listen resolves;
		// accepting it keeps the operator's spelling working without widening
		// what counts as loopback.
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr != nil || port == "" || !strings.EqualFold(host, "localhost") {
			return fmt.Errorf("server.pprof_addr %q must be a loopback host:port such as 127.0.0.1:6060", addr)
		}
		return nil
	}
	if !ap.Addr().IsLoopback() {
		return fmt.Errorf("server.pprof_addr %q must be loopback (127.0.0.1 or ::1): the profiling endpoints have no authentication", addr)
	}
	return nil
}

// Validate checks configuration for invalid or dangerous values.
func (c *Config) Validate() error {
	var errs []error

	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		errs = append(errs, errors.New("server.port must be between 1 and 65535"))
	}
	if c.WebRTC.Enabled {
		if strings.TrimSpace(c.WebRTC.TurnAddr) == "" {
			errs = append(errs, errors.New("webrtc.turn_addr is required when webrtc.enabled is true"))
		}
		if c.WebRTC.RelayMinPort < 1 || c.WebRTC.RelayMinPort > 65535 ||
			c.WebRTC.RelayMaxPort < 1 || c.WebRTC.RelayMaxPort > 65535 ||
			c.WebRTC.RelayMinPort > c.WebRTC.RelayMaxPort {
			errs = append(errs, errors.New("webrtc relay ports must be valid (1-65535, min <= max)"))
		}
		if c.WebRTC.SessionTTLSeconds < 0 || c.WebRTC.SessionTTLSeconds > 86400 {
			errs = append(errs, errors.New("webrtc.session_ttl_seconds must be between 0 and 86400"))
		}
		if c.WebRTC.MaxSessions < 0 || c.WebRTC.MaxSessions > 256 {
			errs = append(errs, errors.New("webrtc.max_sessions must be between 0 and 256"))
		}
	}
	if (c.Database.Driver == "" || c.Database.Driver == "sqlite") && c.Server.DBMaxOpenConns > 1 {
		errs = append(errs, errors.New("server.db_max_open_conns must be 1 for sqlite (single writer; larger pools cause SQLITE_BUSY under load)"))
	}
	if c.Server.OfflineThreshold < 1 {
		errs = append(errs, errors.New("server.offline_threshold must be >= 1 second"))
	}
	if c.Server.OfflineThreshold > 86400 {
		errs = append(errs, errors.New("server.offline_threshold must be <= 86400 (24h)"))
	}
	if c.Server.SessionMaxAgeHours < 1 {
		errs = append(errs, errors.New("server.session_max_age_hours must be >= 1"))
	}
	if c.Server.SessionMaxAgeHours > 8760 {
		errs = append(errs, errors.New("server.session_max_age_hours must be <= 8760 (1 year)"))
	}
	if c.Server.CleanupRetentionDays < 0 {
		errs = append(errs, errors.New("server.cleanup_retention_days must be >= 0"))
	}
	if c.Protocol.CompatWindow < 0 {
		errs = append(errs, errors.New("protocol.compat_window must be >= 0 (0 = serve only the current protocol version)"))
	}
	if c.Protocol.CompatWindow > 16 {
		errs = append(errs, errors.New("protocol.compat_window must be <= 16"))
	}
	if c.Server.CleanupRetentionDays > 3650 {
		errs = append(errs, errors.New("server.cleanup_retention_days must be <= 3650 (10 years)"))
	}
	if c.Server.RegSecretOrphanTTLHours < 0 {
		errs = append(errs, errors.New("server.reg_secret_orphan_ttl_hours must be >= 0 (0 = built-in 7 days)"))
	}
	if c.Server.RegSecretOrphanTTLHours > 8760 {
		errs = append(errs, errors.New("server.reg_secret_orphan_ttl_hours must be <= 8760 (1 year)"))
	}
	if c.Server.AuditRetentionDays < 0 {
		errs = append(errs, errors.New("server.audit_retention_days must be >= 0"))
	}
	if c.Server.AuditRetentionDays > 3650 {
		errs = append(errs, errors.New("server.audit_retention_days must be <= 3650 (10 years)"))
	}
	if c.Implant.DefaultInterval < 1 {
		errs = append(errs, errors.New("implant.default_interval must be >= 1 second"))
	}
	if c.Implant.DefaultInterval > 86400 {
		errs = append(errs, errors.New("implant.default_interval must be <= 86400 (24h)"))
	}
	if c.Implant.DefaultJitter < 0 || c.Implant.DefaultJitter > 100 {
		errs = append(errs, errors.New("implant.default_jitter must be between 0 and 100"))
	}
	if c.Implant.MinInterval < 0 {
		errs = append(errs, errors.New("implant.min_interval must be >= 0 (0 = no minimum)"))
	}
	if c.Implant.MinInterval > 86400 {
		errs = append(errs, errors.New("implant.min_interval must be <= 86400 (24h)"))
	}
	if c.Implant.MinJitter < 0 || c.Implant.MinJitter > 100 {
		errs = append(errs, errors.New("implant.min_jitter must be between 0 and 100"))
	}
	if c.Implant.DefaultInterval < c.Implant.MinInterval {
		errs = append(errs, fmt.Errorf("implant.default_interval (%d) must be >= implant.min_interval (%d)", c.Implant.DefaultInterval, c.Implant.MinInterval))
	}
	if c.Implant.DefaultJitter < c.Implant.MinJitter {
		errs = append(errs, fmt.Errorf("implant.default_jitter (%d) must be >= implant.min_jitter (%d)", c.Implant.DefaultJitter, c.Implant.MinJitter))
	}
	if c.Logging.Level != "" && c.Logging.Level != "debug" && c.Logging.Level != "info" && c.Logging.Level != "warn" && c.Logging.Level != "error" {
		errs = append(errs, errors.New(`logging.level must be one of: debug, info, warn, error`))
	}
	if c.RateLimit.Login.MaxAttempts < 1 {
		errs = append(errs, errors.New("rate_limit.login.max_attempts must be >= 1"))
	}
	if c.RateLimit.Login.Window < 1 {
		errs = append(errs, errors.New("rate_limit.login.window must be >= 1 second"))
	}
	if c.RateLimit.Login.LockoutTime < 1 {
		errs = append(errs, errors.New("rate_limit.login.lockout_time must be >= 1 second"))
	}
	if c.RateLimit.API.Rate < 0 {
		errs = append(errs, errors.New("rate_limit.api.rate must be >= 0"))
	}
	if c.RateLimit.Beacon.Limit < 1 {
		errs = append(errs, errors.New("rate_limit.beacon.limit must be >= 1"))
	}
	if c.RateLimit.Beacon.Window < 1 {
		errs = append(errs, errors.New("rate_limit.beacon.window must be >= 1 second"))
	}
	if c.Server.TCPEnabled && c.Server.TCPAddr == "" {
		errs = append(errs, errors.New("server.tcp_addr is required when tcp_enabled is true"))
	}
	if c.Server.SMBEnabled && c.Server.SMBPipe == "" {
		errs = append(errs, errors.New("server.smb_pipe is required when smb_enabled is true"))
	}
	if c.Server.DNSEnabled && c.Server.DNSDomain == "" {
		errs = append(errs, errors.New("server.dns_domain is required when dns_enabled is true"))
	}
	if c.Server.ICMPEnabled && c.Server.ICMPAddr == "" {
		errs = append(errs, errors.New("server.icmp_addr is required when icmp_enabled is true"))
	}
	if c.Server.GRPCEnabled && c.Server.GRPCAddr == "" {
		errs = append(errs, errors.New("server.grpc_addr is required when grpc_enabled is true"))
	}
	if err := validatePprofAddr(strings.TrimSpace(c.Server.PprofAddr)); err != nil {
		errs = append(errs, err)
	}
	if c.Server.SSHEnabled && c.Server.SSHPort <= 0 {
		errs = append(errs, errors.New("server.ssh_port must be > 0 when ssh_enabled is true"))
	}

	// AI validation
	if c.AI.Enabled && c.AI.APIKey == "" && os.Getenv("FORGEC2_AI_API_KEY") == "" {
		slog.Warn("ai.api_key is empty — AI features will fail at runtime unless set via env FORGEC2_AI_API_KEY")
	}
	if c.AI.MaxConversationTurns < 0 {
		errs = append(errs, errors.New("ai.max_conversation_turns must be >= 0 (0 = unlimited)"))
	}
	if c.AI.MaxToolRounds < 0 {
		errs = append(errs, errors.New("ai.max_tool_rounds must be >= 0 (0 = unlimited)"))
	}
	if c.AI.MaxDuplicateToolCalls < 0 {
		errs = append(errs, errors.New("ai.max_duplicate_tool_calls must be >= 0 (0 = unlimited)"))
	}
	if c.AI.MaxRunsPerUser < 0 || c.AI.MaxRunsPerTenant < 0 {
		errs = append(errs, errors.New("ai run concurrency limits must be >= 0"))
	}
	if c.AI.RunRetentionDays < 0 {
		errs = append(errs, errors.New("ai.run_retention_days must be >= 0"))
	}
	if c.AI.Enabled && c.AI.Provider != "" {
		provider := NormalizeAIProvider(c.AI.Provider)
		if !IsKnownAIProvider(provider) {
			errs = append(errs, AIProviderError())
		} else if AIProviderNeedsExplicitEndpoint(provider) && strings.TrimSpace(c.AI.Endpoint) == "" {
			// "custom" names no vendor, so there is nothing safe to assume.
			// Failing at boot is far better than POSTing the key to a host
			// the operator never chose.
			errs = append(errs, fmt.Errorf("ai.endpoint is required when ai.provider is %q", provider))
		}
	}

	// TLS validation. cert_file/key_file paths are required when TLS is on,
	// but the files themselves may be absent: the server auto-generates a
	// self-signed certificate at startup (Run -> GenerateSelfSignedCert).
	if c.Server.TLSEnabled {
		if c.Server.CertFile == "" {
			errs = append(errs, errors.New("server.cert_file is required when server.tls_enabled is true"))
		}
		if c.Server.KeyFile == "" {
			errs = append(errs, errors.New("server.key_file is required when server.tls_enabled is true"))
		}
	}
	if c.Server.RequireTLSForAuth && !c.Server.TLSEnabled {
		errs = append(errs, errors.New("server.require_tls_for_auth is true but server.tls_enabled is false — refusing to issue session cookies over plain HTTP (set tls_enabled: true or require_tls_for_auth: false)"))
	}

	// Operator-plane hardening validation. CIDRs are checked with netip
	// (bare IPs accepted, normalized server-side); mTLS needs TLS plus a
	// CA bundle path (the file itself is loaded at startup like cert_file).
	for _, cidr := range c.Server.OperatorAllowedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			if _, err2 := netip.ParseAddr(cidr); err2 != nil {
				errs = append(errs, fmt.Errorf("server.operator_allowed_cidrs entry %q is not a CIDR or IP: %v", cidr, err2))
			}
		}
	}
	if c.Server.OperatorMTLS {
		if !c.Server.TLSEnabled {
			errs = append(errs, errors.New("server.operator_mtls is true but server.tls_enabled is false — client certificates require TLS"))
		}
		if strings.TrimSpace(c.Server.OperatorClientCAFile) == "" {
			errs = append(errs, errors.New("server.operator_mtls is true but server.operator_client_ca_file is empty"))
		}
	}

	// Rate limit validation
	if c.RateLimit.API.Capacity <= 0 {
		errs = append(errs, errors.New("rate_limit.api.capacity must be > 0"))
	}
	if c.RateLimit.ExtC2.Rate < 0 {
		errs = append(errs, errors.New("rate_limit.extc2.rate must be >= 0"))
	}
	if c.RateLimit.ExtC2.Burst < 1 {
		errs = append(errs, errors.New("rate_limit.extc2.burst must be >= 1 (burst=0 blocks all requests)"))
	}
	if c.RateLimit.ExtC2.CleanupAge < 0 {
		errs = append(errs, errors.New("rate_limit.extc2.cleanup_age must be >= 0"))
	}
	if c.RateLimit.ExtC2.APIToken == "" {
		slog.Warn("rate_limit.extc2.api_token is empty — a random token will be auto-generated on startup")
	}
	if c.Malleable.Enabled && c.Malleable.StatusCode != 0 && (c.Malleable.StatusCode < 100 || c.Malleable.StatusCode > 599) {
		errs = append(errs, errors.New("malleable.status_code must be between 100 and 599 or 0 for default"))
	}
	if c.Malleable.RespJitterMs < 0 || c.Malleable.RespJitterMs > 5000 {
		errs = append(errs, errors.New("malleable.resp_jitter_ms must be between 0 and 5000"))
	}
	if p := c.TLSFingerprint.JA3Profile; p != "" && p != "random" && p != "chrome" && p != "firefox" && p != "edge" && p != "safari" {
		errs = append(errs, errors.New("tls_fingerprint.ja3_profile must be empty, random, chrome, firefox, edge or safari"))
	}
	if c.PasswordPolicy.MinLength < 4 {
		errs = append(errs, errors.New("password_policy.min_length must be >= 4"))
	}
	if c.PasswordPolicy.MinLength > 128 {
		errs = append(errs, errors.New("password_policy.min_length must be <= 128"))
	}
	if c.PasswordPolicy.MaxAge < 0 {
		errs = append(errs, errors.New("password_policy.max_age_days must be >= 0"))
	}
	if c.PasswordPolicy.BcryptCost < 0 {
		errs = append(errs, errors.New("password_policy.bcrypt_cost must be >= 0 (0 = default 10)"))
	}
	if c.PasswordPolicy.BcryptCost > 0 && (c.PasswordPolicy.BcryptCost < 4 || c.PasswordPolicy.BcryptCost > 31) {
		errs = append(errs, errors.New("password_policy.bcrypt_cost must be between 4 and 31 when non-zero"))
	}
	if c.Auth.DefaultPasswd != "" && isWeakDefaultPassword(c.Auth.DefaultPasswd) {
		errs = append(errs, errors.New("auth.default_password is too weak — set a strong password or leave it empty to auto-generate a random one on first boot"))
	}
	if c.Auth.SessionMaxConcurrent < 0 {
		errs = append(errs, errors.New("auth.session_max_concurrent must be >= 0 (0 = unlimited)"))
	}
	if c.Socks.Enabled {
		for _, dest := range c.Socks.AllowedDests {
			if !strings.Contains(dest, ":") {
				errs = append(errs, fmt.Errorf("socks.allowed_destinations entry %q must be in host:port format", dest))
			}
		}
	}

	if c.Listeners.H2C.Enabled && c.Listeners.H2C.Addr == "" {
		errs = append(errs, errors.New("listeners.h2c.addr is required when listeners.h2c.enabled is true"))
	}
	if c.Server.QUICEnabled && c.Server.QUICAddr == "" {
		errs = append(errs, errors.New("server.quic_addr is required when server.quic_enabled is true"))
	}

	// Crypto validation — the v2/v3 beacon stack implements ONLY the ECDH
	// chain (HKDF+X25519+AES-256-GCM+MAC+replay window). A legacy XOR key or
	// a disabled (empty) mode cannot authenticate any built implant:
	// registration requires the per-implant v3 secret which only the ECDH
	// session manager issues, and plaintext frames are rejected by the
	// protocol. Refuse to boot rather than run silently-broken beacons.
	if c.Crypto.Key == "" || !strings.HasPrefix(c.Crypto.Key, "ecdh:") {
		errs = append(errs, errors.New("crypto.key must be \"ecdh:\" (ECDH beacon encryption) — the v2/v3 beacon protocol does not implement the legacy XOR/plaintext modes, so any other value leaves every built implant unable to authenticate"))
	}
	if c.Crypto.MaxDecryptedPayloadSize < 0 {
		errs = append(errs, errors.New("crypto.max_decrypted_payload_size must be >= 0 (0 = default 10MB)"))
	}
	if c.Crypto.MaxDecryptedPayloadSize > 104857600 {
		errs = append(errs, errors.New("crypto.max_decrypted_payload_size must be <= 104857600 (100MB)"))
	}
	switch c.Crypto.SessionCipher {
	case "", "aes-gcm", "chacha20", "chacha20-poly1305":
	default:
		errs = append(errs, fmt.Errorf("crypto.session_cipher must be \"\", \"aes-gcm\", or \"chacha20\" (got %q)", c.Crypto.SessionCipher))
	}
	// Crypto validation — storage keys are REQUIRED and must be 32-byte hex.
	// The legacy SHA-256(jwt_secret) derivation cascade was removed (breaking
	// change): each key is independent so a JWT compromise cannot decrypt
	// credentials, backups, or bind CSRF tokens.
	cryptoKeys := []struct {
		name string
		val  string
	}{
		{"crypto.loot_key", c.Crypto.LootKey},     // loot credentials (FC2ENC:)
		{"crypto.extc2_key", c.Crypto.ExtC2Key},   // ExtC2 channel (FC2EXT:)
		{"crypto.backup_key", c.Crypto.BackupKey}, // .fbk backups
		{"crypto.totp_key", c.Crypto.TotpKey},     // SMTP / SSH redirector credentials
		{"crypto.csrf_key", c.Crypto.CsrfKey},     // CSRF token binding
	}
	for _, k := range cryptoKeys {
		if k.val == "" {
			errs = append(errs, fmt.Errorf("%s is required (auto-generated only on fresh installs; the legacy derivation from server.jwt_secret was removed - set a 64-character hex key in config.yaml or the corresponding FORGEC2_%s env var; note existing encrypted data created with the old derived key can no longer be decrypted)", k.name, envSuffixForKey(k.name)))
			continue
		}
		if len(k.val) != 64 {
			errs = append(errs, fmt.Errorf("%s must be a 64-character hex string (32 bytes)", k.name))
			continue
		}
		if _, err := hex.DecodeString(k.val); err != nil {
			errs = append(errs, fmt.Errorf("%s must be a valid hex string", k.name))
		}
	}
	// Release-signing key is OPTIONAL (empty = checksum-only hot updates with
	// a warning); when set it must be a 32-byte Ed25519 public key in hex.
	if c.Crypto.UpdateSigningKey != "" {
		if len(c.Crypto.UpdateSigningKey) != 64 {
			errs = append(errs, errors.New("crypto.update_signing_key must be a 64-character hex string (32-byte Ed25519 public key)"))
		} else if _, err := hex.DecodeString(c.Crypto.UpdateSigningKey); err != nil {
			errs = append(errs, errors.New("crypto.update_signing_key must be a valid hex string"))
		}
	}

	return errors.Join(errs...)
}

// envSuffixForKey maps a config field path to the FORGEC2_* env var suffix.
func envSuffixForKey(field string) string {
	return strings.ToUpper(strings.SplitN(field, ".", 2)[1])
}

// Lock acquires the config mutex for safe concurrent reads/writes.
func (c *Config) Lock() {
	c.mu.Lock()
}

// Unlock releases the config mutex.
func (c *Config) Unlock() {
	c.mu.Unlock()
}

// RLock acquires the config read lock for concurrent reads.
func (c *Config) RLock() {
	c.mu.RLock()
}

// RUnlock releases the config read lock.
func (c *Config) RUnlock() {
	c.mu.RUnlock()
}

// Save persists the config (e.g. after setting password)
// Save is defined in save_preserve.go: it preserves keys this build does not know.

// CopyFrom copies all exported fields from src into c with mutex protection.
// Every top-level section must be listed here, otherwise hot-reload silently
// keeps stale values for the omitted sections.
func (c *Config) CopyFrom(src *Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Server = src.Server
	c.Database = src.Database
	c.Implant = src.Implant
	c.Auth = src.Auth
	c.PasswordPolicy = src.PasswordPolicy
	c.Crypto = src.Crypto
	c.Malleable = src.Malleable
	c.AI = src.AI
	c.Logging = src.Logging
	c.SIEM = src.SIEM
	c.Integrations = src.Integrations
	c.Socks = src.Socks
	c.RateLimit = src.RateLimit
	c.Security = src.Security
	c.Monitoring = src.Monitoring
	c.Listeners = src.Listeners
	c.Roe = src.Roe
	c.TLSFingerprint = src.TLSFingerprint
	// These four were missing, so a hot reload reported success while these sections
	// kept the values the process started with. Plugins was the dangerous one: the
	// live plugin trust policy (trusted keys, require_signed, the sandbox limits) did
	// not change when the operator edited it.
	c.Plugins = src.Plugins
	c.Backup = src.Backup
	c.WebRTC = src.WebRTC
	c.Protocol = src.Protocol
}

// SocksUser holds SOCKS5 username/password auth credentials.
type SocksUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// TLSFingerprintConfig holds JARM/JA3 fingerprint randomization settings
type TLSFingerprintConfig struct {
	JARMEnabled bool   `yaml:"jarm_randomize"`
	JARMRotate  string `yaml:"jarm_rotate_interval"`
	JA3Enabled  bool   `yaml:"ja3_randomize"`
	JA3Profile  string `yaml:"ja3_profile"`
	JA3Rotate   string `yaml:"ja3_rotate_interval"`
}

// SlackConfig holds Slack integration settings
type SlackConfig struct {
	Enabled       bool   `yaml:"enabled"`
	AppToken      string `yaml:"app_token"`
	BotToken      string `yaml:"bot_token"`
	SigningSecret string `yaml:"signing_secret"`
}

// ListenersConfig holds transport listener configurations.
type ListenersConfig struct {
	H2C H2CListenerConfig `yaml:"h2c"`
}

// H2CListenerConfig configures the H2C (HTTP/2 cleartext) listener.
type H2CListenerConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

// LoadFromData loads config from byte data
func (c *Config) LoadFromData(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return yaml.Unmarshal(data, c)
}

// AllowedOrigin checks if the given origin hostname is in the configured allow list.
// Returns false (deny) when no origins are configured.
func (c *Config) AllowedOrigin(hostname string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.Server.AllowedOrigins) == 0 {
		return false
	}
	// Case-insensitive for the same reason as the server-side check: RFC 4343
	// hostnames are not case-sensitive, and a config entry typed with capitals
	// must not silently never match.
	want := strings.ToLower(strings.TrimSpace(hostname))
	for _, allowed := range c.Server.AllowedOrigins {
		if want == strings.ToLower(strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}
