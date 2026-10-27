package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/forgec2/forgec2/internal/server/opsec"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// ── Server core: struct, constructor, events, config reload, routes ────────
// Transports live in server_ws.go / server_net.go / server_listeners.go,
// lifecycle in server_lifecycle.go, retention in server_cleanup.go,
// HTTP glue in server_http.go.

type Server struct {
	cfg            *config.Config
	configMu       sync.RWMutex
	db             *gorm.DB
	router         *gin.Engine
	wsClients      map[*websocket.Conn]*wsClientConn
	wsMutex        sync.RWMutex
	wsUpgrader     websocket.Upgrader
	rateLimiter    *middleware.RateLimiter
	apiRateLimiter *middleware.APIRateLimiter
	loginLockout   *loginLockoutTracker
	socksEngine    *socksRelayEngine
	cookieProxy    *cookieProxyEngine
	tunEngine      *tunEngine
	startTime      time.Time

	dnsListener *DNSBeaconListener
	// dnsListenerMu guards the dnsListener pointer: the status poll, the
	// start/stop handlers and shutdown read and swap it concurrently, and an
	// unguarded read raced a start (a half-visible pointer) or a stop.
	dnsListenerMu         sync.Mutex
	icmpListener          *ICMPBeaconListener
	grpcListener          *GRPCListener
	sshListener           *SSHBeaconListener
	h2cListener           *H2CBeaconListener
	quicListener          *QUICBeaconListener
	smbLn                 net.Listener
	tcpLn                 net.Listener
	udpConn               net.PacketConn
	screenMonitorImplants map[string]time.Time
	screenMonitorMu       sync.Mutex
	// screenMonitorOps is the single-flight reservation for start/stop: DB
	// writes (and plugin hooks) must not run while holding screenMonitorMu,
	// or every frame ingest blocks behind an unrelated task insert.
	screenMonitorOps map[string]screenMonitorOp
	screenFrames     map[string]cachedScreenFrame
	screenFrameMu    sync.RWMutex
	// remoteInputLimiter throttles remote desktop input events per
	// (operator, agent). Value type: zero value works on bare test servers.
	remoteInputLimiter fixedWindowLimiter
	// offlineWarnAt is the unix-nanos of the last "OfflineThreshold is
	// invalid" warning; the getter runs per beacon and per poll, so the warn
	// is rate-limited (see offlineThreshold). Zero value works on bare test
	// servers.
	offlineWarnAt atomic.Int64

	// WebRTC live desktop hub (Pion sessions + embedded TURN). Lazily created
	// so bare test servers and operators who leave webrtc.enabled=false do not
	// allocate or bind anything.
	webrtc       *webrtcHub
	webrtcInitMu sync.Mutex

	// P0-3: rportfwd (reverse port forward)
	rportfwdListeners map[string]*rportfwdRelay
	rportfwdMu        sync.Mutex
	// rportfwdNextID allocates globally-unique operator conn IDs across all
	// relays: per-relay counters restart at 1, so two relays for one agent
	// would otherwise share connIDs and cross-deliver agent data frames.
	rportfwdNextID atomic.Uint64

	// lportfwd: agent-local listeners tunneled through the beacon; the
	// teamserver dials the final target on the agent's behalf. Keyed by the
	// frame ConnID chosen by the agent.
	lportfwdTargets map[string]*lportfwdTarget // key: agentID|connID (cross-agent hijack fix)
	lportfwdMu      sync.Mutex
	// lportfwdDeclared tracks operator-declared targets per agent
	// (from lportfwd_start task commands). lportfwd_connect frames must
	// match one — otherwise a compromised implant gets an arbitrary-dial
	// SSRF primitive into the teamserver's network.
	lportfwdDeclared map[string]map[string]bool

	trafficLog   *trafficRing
	trafficBytes *trafficByteAccumulator
	updateState  updateCheckState

	// Server-side traffic auto-adapt loop: last set_sleep trigger per agent.
	// RWMutex because the per-beacon hot path only reads the timestamp.
	autoAdaptMu   sync.RWMutex
	autoAdaptLast map[string]time.Time

	// Domain fronting
	domainFrontDomains []string
	domainFrontMu      sync.Mutex
	domainFrontAuto    bool
	domainFrontStatus  map[string]*frontDomainState

	// WebSocket hub
	wsHub        *WebSocketHub
	wsHubOnce    sync.Once
	shutdownOnce sync.Once

	// Operator WebSocket sessions (real-time updates)
	operatorSessions *operatorSessionTracker

	// Event system
	eventManager *EventManager

	// AI runs are detached from individual HTTP requests. The broker only keeps
	// live subscribers and cancellation handles; replayable events live in DB.
	aiRuns        *aiRunBroker
	aiRunCreateMu sync.Mutex

	// Per-(agent,domain) consecutive-failure fuse for credential checks
	credCheckFuse *credCheckFuseTracker

	// Beacon cipher removed, use ECDH session encryption (cfg.Crypto.Key = "ecdh:")

	// ECDH session manager (nil = disabled / old XOR mode)
	sessionManager *crypto.SessionManager

	// v3 per-implant registration secret store (seals/unseals reg secrets)
	regSecrets *crypto.RegSecretStore

	// Per-agent bookkeeping for online network-config refresh (config-over-wire
	// on encrypted beacon responses). Lazily initialised; see
	// beacon_netconfig.go.
	networkConfigMu         sync.Mutex
	networkConfigDelivered  map[string]networkConfigDelivery
	networkConfigGeneration uint64

	monitorCollector *MonitorCollector

	// Plugin marketplace
	marketplace *plugin.Marketplace

	// Plugin execution manager
	pluginManager *plugin.Manager

	// Circuit breaker for listener health
	circuitBreaker *CircuitBreaker

	// Optimizations
	configReloader *ConfigReloader
	backupManager  *BackupManager
	configPath     string
	ctx            context.Context
	ctxCancel      context.CancelFunc
	wg             sync.WaitGroup
	// updateWg tracks the hot-update goroutine. It is deliberately separate
	// from wg: the update ends by shutting the server down, and a worker
	// that waits for the waitgroup it belongs to can never be released.
	updateWg sync.WaitGroup
	// fastStop makes the shutdown cut in-flight requests instead of
	// draining them. The hot update uses it: the process is about to be
	// replaced, so a graceful drain would only add downtime.
	fastStop atomic.Bool

	// Extra listeners started dynamically via the UI (create listener).
	// Each entry is keyed by "scheme://host:port".
	extraListeners   map[string]io.Closer
	extraListenersMu sync.Mutex

	// Opt-in profiling listener (config server.pprof_addr). Kept out of
	// extraListeners on purpose: those entries are operator listeners and the
	// listener-health endpoint walks them, while this one only ever serves
	// /debug/pprof. nil when profiling is disabled.
	pprofSrv  *http.Server
	pprofAddr string
	pprofMu   sync.Mutex

	metrics *MetricsCollector

	// GeoIP lookup concurrency limiter
	geoIPSem chan struct{}

	// Task result background worker pool: limits concurrent goroutines spawned
	// for callbacks, plugin hooks, and token processing to prevent OOM under load.
	taskWorkerSem chan struct{}

	// Async audit pipeline: request paths enqueue, a single worker owns the
	// hash chain (auditLastHash) and persists batches. auditQueue is nil
	// until startAuditWorker runs (unit tests keep the sync fallback).
	auditQueue    chan []db.AuditLog
	auditLastHash string
	auditDropped  atomic.Int64
	// auditTamperID is the row id of the last reported chain-integrity failure,
	// so /ready polls do not re-log the same finding every few seconds.
	auditTamperMu sync.Mutex
	auditTamperID uint
	// auditAnchorFile records the chain head outside the audit table. Resolved at
	// startup (initAuditAnchor) because the audit path must not take configMu.
	auditAnchorFile string
	// auditVerdict* cache the chain verdict for the UI health endpoint.
	auditVerdictMu sync.Mutex
	auditVerdict   auditChainVerdict
	auditVerdictAt time.Time

	// Backlog metric label hygiene: agents currently exported as per-agent
	// gauge series, so drained agents can be pruned.
	backlogAgents   map[string]struct{}
	backlogAgentsMu sync.Mutex

	// NTLM relay session tracking
	ntlmRelays *ntlmRelayStore

	// Bulk operation history ring buffer
	bulkHistory   []BulkResult
	bulkHistoryMu sync.Mutex

	// Per-agent task queue depth tracking (agentID → pending task count)
	agentPendingTasks   map[string]int
	agentPendingTasksMu sync.Mutex

	// Beacon deduplication: track recently processed beacon fingerprints
	beaconDedupMu    sync.Mutex
	beaconDedupCache map[string]time.Time
	beaconDedupSweep time.Time

	// Protocol negotiation notices: agentID → when the agent was last told
	// about its protocol version. Keeps a beaconing agent from writing the
	// same notice to the audit log on every check-in.
	protocolNoticeMu sync.Mutex
	protocolNotices  map[string]time.Time

	// Per-agent seq flood lockout (agentID → unlock time). Set by acceptSeq
	// when a frame sequence jump exceeds the hard cap; guards the replay
	// window against a key-holding actor replay-flooding a valid frame.
	seqLockoutMu sync.Mutex
	seqLockout   map[string]time.Time

	// P2P relay: nesting depth travels explicitly with processRelayedEnvelopes
	// (see beacon_relay.go); no server-global counter is kept.

	// Task result idempotency: agentID + result id → processed timestamp.
	// Results re-sent after a dropped frame carry a new envelope seq, so
	// dedupe on the agent-supplied result id instead.
	resultDedupeMu    sync.Mutex
	resultDedupeCache map[string]time.Time
	resultDedupeSweep time.Time

	// Phishing landing page rate limiting keyed by token+IP
	landingLimiterMu    sync.Mutex
	landingLimiterHits  map[string]int
	landingLimiterSince map[string]time.Time

	// External C2 channels (WebSocket relay, Discord, Slack)
	extC2Channels map[string]*extC2WSChannel
	// extC2Runners tracks LIVE poller instances (Discord/Slack) so a deleted
	// channel can actually be stopped — previously the delete handler only
	// removed the metadata entry and the run loop kept reconnecting with the
	// "deleted" bot token until process restart.
	extC2Runners    map[string]extC2Runner
	extC2ChannelsMu sync.Mutex
	extC2TaskQueue  map[string][]extC2Task
	extC2TaskMu     sync.Mutex
	extC2Notify     map[string]chan struct{} // per-channel notification for push-based task delivery

	// Async build job tracking
	buildJobs   map[string]*BuildJob
	buildJobsMu sync.RWMutex

	// Serialized build execution queue: heavy go/garble toolchain invocations
	// are queued through a single worker so concurrent generate requests cannot
	// exhaust CPU/disk. Lazily initialized on first submit.
	buildQueue     chan *queuedBuild
	buildQueueOnce sync.Once

	// buildSem bounds total concurrent toolchain invocations (queue worker AND
	// synchronous stager/one-liner/shellcode builds) so a flood of generate
	// requests cannot exhaust CPU/RAM. Lazily initialized on first submit.
	buildSem chan struct{}

	// transientArtifacts tracks generated files (stager/one-liner builds) that
	// are served once and never tracked as BuildJobs, so they must be reaped
	// separately to avoid unbounded growth of data/agents.
	transientArtifacts   map[string]time.Time
	transientArtifactsMu sync.Mutex

	// Serializes lazy /stage stage-2 payload builds per token so concurrent
	// fetches for the same token do not trigger redundant toolchain builds.
	stageBuildLocks   map[string]*sync.Mutex
	stageBuildLocksMu sync.Mutex

	// Embedded frontend static files (nil = API-only mode)
	staticFS fs.FS

	// Precomputed serving metadata for the embedded files (content-hash ETag,
	// size, compressibility), built once by SetStaticFS before the server
	// starts. Read-only afterwards, no lock needed.
	staticMeta map[string]staticAssetMeta

	// Main HTTP server (for graceful shutdown)
	httpServer *http.Server

	// In-flight request tracker for graceful shutdown
	inFlight *middleware.InFlightTracker

	// Flapping suppression: track last status event time per agent
	agentStatusCooldown   map[string]time.Time
	agentStatusCooldownMu sync.Mutex

	// Reusable HTTP clients with connection pooling
	httpClient     *http.Client
	httpClientLong *http.Client

	// Cached automation rules
	automationRules   []AutomationRule
	automationRulesMu sync.RWMutex
	automationRulesAt time.Time

	// Nav stats cache (keyed by tenant id; 0 = legacy/unscoped operators)
	navStatsCache   map[uint]navStatsEntry
	navStatsCacheMu sync.RWMutex

	// SIEM webhook for security event forwarding
	siem *SIEMWebhook

	// TLS fingerprint randomization (JARM/JA3)
	tlsFingerprint *TLSFingerprintManager

	// Password change rate limiter (userID → last change time)
	pwdChangeTimes   map[uint]time.Time
	pwdChangeTimesMu sync.Mutex

	// Hot-reloadable TLS server cert/key pair (mtime-checked per handshake)
	tlsCerts *tlsCertLoader

	// Operator-plane mTLS: cached CA pool verifying operator client certs.
	// Loaded once at startup (restart to rotate); nil until then.
	operatorCAPool     *x509.CertPool
	operatorCALoadOnce sync.Once

	// OPSEC adaptive threat manager
	opsecAdaptive *opsec.AdaptiveManager

	// Chunked file-transfer HMAC integrity chain state
	fileChains *fileChainState

	// Fleet kill-switch broadcast state (mirrors the KillSwitch DB row; the
	// beacon hot path reads this cache instead of querying on every check-in)
	killSwitchMu    sync.RWMutex
	killSwitchArmed bool
	killSwitchToken string
}

// BulkResult tracks a batch command operation.
type BulkResult struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Command   string    `json:"command"`
	TaskType  string    `json:"task_type"`
	Created   int       `json:"tasks_created"`
	Skipped   int       `json:"skipped_locked"`
	Failed    int       `json:"failed"`
	Operator  string    `json:"operator"`
	// TenantID scopes the in-memory history: the bulk endpoints returned every
	// tenant's commands (and their command strings, which can carry credentials)
	// to any operator with the permission.
	TenantID uint `json:"tenant_id"`
}

const maxBulkHistory = 50

func New(cfg *config.Config, database *gorm.DB) *Server { // config-lock: constructor, no reloader or HTTP server yet
	payload.SetConfiguredGoProxy(cfg.Implant.GoProxy)
	gin.SetMode(gin.ReleaseMode)

	if err := middleware.InitJWTSecret(cfg, ""); err != nil {
		slog.Error("Failed to initialize JWT secret", "err", err)
		os.Exit(1)
	}
	if err := middleware.InitCSRFSecret(cfg); err != nil {
		slog.Error("Failed to initialize CSRF secret", "err", err)
		os.Exit(1)
	}
	crypto.InitLootEncryption(cfg.Crypto.LootKey)
	crypto.InitExtC2Encryption(cfg.Crypto.ExtC2Key)

	inFlight := middleware.NewInFlightTracker()

	r := gin.New()
	r.RedirectTrailingSlash = false
	// Secure default: do NOT trust X-Forwarded-For / X-Real-IP headers unless the
	// operator explicitly configures trusted proxy IPs/CIDRs. Otherwise any client
	// can spoof the header to bypass login lockout, account lockout, and rate limits.
	if len(cfg.Server.TrustedProxies) > 0 {
		if err := r.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
			slog.Error("Invalid trusted_proxies config, ignoring", "error", err, "proxies", cfg.Server.TrustedProxies)
		}
		middleware.SetTrustedProxyIPs(cfg.Server.TrustedProxies)
	} else {
		_ = r.SetTrustedProxies(nil) // nil never errors: only a CIDR list can fail validation
		middleware.SetTrustedProxyIPs(nil)
	}
	r.Use(gin.Recovery())
	r.Use(inFlight.Middleware())
	r.Use(middleware.RequestID())
	r.Use(middleware.SecurityHeaders(cfg.Server.TLSEnabled))
	r.Use(middleware.NoCache())
	r.Use(middleware.ErrorHandler())

	s := &Server{
		cfg:                   cfg,
		db:                    database,
		router:                r,
		wsClients:             make(map[*websocket.Conn]*wsClientConn),
		loginLockout:          newLoginLockoutTracker(),
		socksEngine:           newSocksRelayEngine(),
		cookieProxy:           newCookieProxyEngine(),
		tunEngine:             newTunEngine(),
		startTime:             time.Now(),
		screenMonitorImplants: make(map[string]time.Time),
		screenMonitorOps:      make(map[string]screenMonitorOp),
		screenFrames:          make(map[string]cachedScreenFrame),
		rportfwdListeners:     make(map[string]*rportfwdRelay),
		lportfwdTargets:       make(map[string]*lportfwdTarget),
		lportfwdDeclared:      make(map[string]map[string]bool),
		trafficLog:            newTrafficRing(),
		trafficBytes:          newTrafficByteAccumulator(),
		autoAdaptLast:         make(map[string]time.Time),
		eventManager:          NewEventManager(database),
		operatorSessions:      &operatorSessionTracker{sessions: make(map[uint]*WSOperatorSession)},
		credCheckFuse:         newCredCheckFuseTracker(),
		extraListeners:        make(map[string]io.Closer),
		domainFrontStatus:     make(map[string]*frontDomainState),
		agentPendingTasks:     make(map[string]int),
		beaconDedupCache:      make(map[string]time.Time),
		seqLockout:            make(map[string]time.Time),
		landingLimiterHits:    make(map[string]int),
		landingLimiterSince:   make(map[string]time.Time),
		agentStatusCooldown:   make(map[string]time.Time),
		ntlmRelays:            newNTLMRelayStore(),
		backlogAgents:         make(map[string]struct{}),
		extC2Channels:         make(map[string]*extC2WSChannel),
		extC2Runners:          make(map[string]extC2Runner),
		extC2TaskQueue:        make(map[string][]extC2Task),
		extC2Notify:           make(map[string]chan struct{}),
		buildJobs:             make(map[string]*BuildJob),
		geoIPSem:              make(chan struct{}, GeoIPSemaphoreSize),
		taskWorkerSem:         make(chan struct{}, TaskWorkerPoolSize),
		wsUpgrader: websocket.Upgrader{
			ReadBufferSize:  WSReadBufSize,
			WriteBufferSize: WSWriteBufSize,
			CheckOrigin: func(r *http.Request) bool {
				return allowedOrigin(cfg, r)
			},
		},
		httpClient: &http.Client{
			Timeout: HTTPClientShortTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        HTTPMaxIdleConns,
				MaxIdleConnsPerHost: HTTPMaxIdleConnsPerHost,
				IdleConnTimeout:     HTTPIdleConnTimeout,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			},
		},
		httpClientLong: &http.Client{
			Timeout: HTTPClientLongTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        HTTPClientMaxIdleConns,
				MaxIdleConnsPerHost: HTTPClientMaxIdlePerHost,
				IdleConnTimeout:     HTTPIdleConnTimeout,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			},
		},
	}

	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	s.aiRuns = newAIRunBroker()
	s.initializeAIRuns()

	// Beacon dedup cache cleanup (bounded to prevent memory exhaustion)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic", "err", r, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(BeaconDedupCleanup)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.beaconDedupMu.Lock()
				now := time.Now()
				for k, t := range s.beaconDedupCache {
					if now.Sub(t) > BeaconDedupStaleAge {
						delete(s.beaconDedupCache, k)
					}
				}
				s.beaconDedupMu.Unlock()

				s.pwdChangeTimesMu.Lock()
				for k, t := range s.pwdChangeTimes {
					if now.Sub(t) > PwdChangeCleanupInterval {
						delete(s.pwdChangeTimes, k)
					}
				}
				s.pwdChangeTimesMu.Unlock()
			case <-s.ctx.Done():
				return
			}
		}
	}()

	s.inFlight = inFlight

	// Initialize rate limiters with context for graceful shutdown
	s.rateLimiter = middleware.NewRateLimiter(s.ctx, cfg.RateLimit.Beacon.Limit, time.Duration(cfg.RateLimit.Beacon.Window)*time.Second)
	s.apiRateLimiter = middleware.NewAPIRateLimiter(s.ctx, cfg.RateLimit.API.Capacity, cfg.RateLimit.API.Rate)

	// Start periodic cleanup for login lockout entries
	s.loginLockout.startCleanup(s.ctx, cfg.RateLimit.Login.Window)

	// Start periodic cleanup for beacon seq-flood lockout entries (S5)
	s.startSeqLockoutCleanup(s.ctx)

	// Beacon payload encryption via ECDH session (cfg.Crypto.Key = "ecdh:")
	if strings.HasPrefix(cfg.Crypto.Key, "ecdh:") {
		sm, err := crypto.NewSessionManagerWithCipher(crypto.DefaultSessionMaxAge, cfg.Crypto.SessionCipher)
		if err != nil {
			slog.Error("Failed to initialize ECDH session manager, falling back to XOR", "err", err)
		} else {
			s.sessionManager = sm
			slog.Info("ECDH session encryption enabled", "session_cipher", sm.Cipher())
		}
	}

	// v3 per-implant registration secret store (server-side sealing key
	// derived from the master beacon key; never embedded in payloads).
	if beaconKeyHex := cfg.Server.BeaconKey; beaconKeyHex != "" {
		if master, err := hex.DecodeString(beaconKeyHex); err == nil && len(master) > 0 {
			s.regSecrets = crypto.NewRegSecretStore(master)
			slog.Info("v3 per-implant registration secrets enabled")
		}
	}

	// Periodic cleanup of stale ECDH sessions to prevent unbounded map growth.
	// Interval-aware: a session idle longer than max(30min, 3x its agent's
	// sleep interval) is dropped. A static 30min cutoff would evict every
	// legitimate long-sleep agent, forcing a re-handshake storm on wake.
	if s.sessionManager != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.sweepECDHSessions()
				case <-s.ctx.Done():
					return
				}
			}
		}()
	}

	// Periodic cleanup of orphaned v3 registration secrets: a secret is created
	// before the toolchain runs, so a failed build leaves an unbound, unusable
	// row. Sweep unbound secrets older than server.reg_secret_orphan_ttl_hours
	// (default 7 days) at startup and hourly; the authentication path also
	// rejects expired unbound secrets between sweeps, so a stale build secret
	// cannot enroll an agent just because the tick has not fired yet. The same
	// pass prunes the online network-config delivery bookkeeping.
	if s.regSecrets != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			cleanup := func() {
				s.cleanupOrphanedRegSecrets(s.regSecretOrphanTTL())
				s.pruneNetworkConfigDeliveries(time.Now().Add(-networkConfigDeliveryTTL))
			}
			cleanup()
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					cleanup()
				case <-s.ctx.Done():
					return
				}
			}
		}()
	}

	s.apiRateLimiter.SetWhitelist(cfg.RateLimit.API.Whitelist)

	s.metrics = NewMetricsCollector(s)
	s.metrics.Register(prometheus.DefaultRegisterer)
	// Feed beacon 429s into Prometheus. middleware cannot import server (cycle),
	// so the limiter exposes a rejection callback.
	s.rateLimiter.SetOnLimit(func() {
		if s.metrics != nil {
			s.metrics.BeaconRateLimitedTotal.Inc()
		}
	})
	r.Use(metricsMiddleware(s.metrics))
	if s.socksEngine != nil {
		s.socksEngine.SetDropCounter(s.metrics.SocksDroppedTotal)
	}
	if s.tunEngine != nil {
		s.tunEngine.SetDropCounter(s.metrics.SocksDroppedTotal)
	}
	if s.eventManager != nil {
		s.eventManager.SetDropCounter(s.metrics.EventDroppedTotal)
	}

	if cfg.SIEM.Enabled && cfg.SIEM.URL != "" {
		s.siem = NewSIEMWebhook(s, cfg.SIEM.URL, cfg.SIEM.Token, cfg.SIEM.Actions)
		s.siem.ReloadRules()
		slog.Info("SIEM webhook enabled", "url", cfg.SIEM.URL)
	}

	// Two-man rule is secure-by-default; an explicit opt-out deserves a
	// loud warning because dangerous tasks then execute on single dispatch.
	if !cfg.Security.RequireApproval {
		slog.Warn("security.require_approval is OFF: dangerous tasks (dcsync, lateral, persistence, …) execute without a second operator's approval")
	}

	// TLS fingerprint randomization (JARM/JA3)
	s.initTLSFingerprint()

	// Password change rate limiter
	s.pwdChangeTimes = make(map[uint]time.Time)

	// Hot-reloadable server cert pair (eager load happens in configureTLS,
	// preserving the old fail-fast timing when TLS is enabled).
	s.tlsCerts = newTLSCertLoader(s.cfg.Server.CertFile, s.cfg.Server.KeyFile)

	// OPSEC adaptive threat manager
	s.opsecAdaptive = opsec.NewAdaptiveManager()
	s.opsecAdaptive.StartDecayLoop()

	s.fileChains = newFileChainState()

	// Fleet kill-switch broadcast state
	s.reloadKillSwitchState()

	// NOTE: setupRoutes() is NOT called here. Call SetupRoutes() after
	// SetStaticFS() to ensure the static file middleware runs before route handlers.

	// Initialize plugin marketplace
	s.marketplace = plugin.NewMarketplace(database)
	if s.cfg.Server.UpdateCheckEnabled {
		// Both the marketplace and the version checker phone home to
		// api.github.com — strictly opt-in for egress hygiene.
		s.marketplace.StartUpdateChecker(PluginUpdateCheckInterval)
	}

	// Initialize plugin execution manager
	s.pluginManager = plugin.NewManager(database)
	s.pluginManager.SetMarketplace(s.marketplace)
	// Package trust: verify manifests against the operator's Ed25519 plugin
	// signing keys before anything becomes callable. Unsigned packages still
	// load (with a warning) unless plugins.require_signed is set, so the
	// bundled plugins keep working out of the box.
	if keys, err := plugin.ParseTrustedPluginKeys(s.cfg.Plugins.TrustedKeys); err != nil {
		slog.Error("Invalid plugins.trusted_keys; plugin signature verification is disabled", "err", err)
	} else if len(keys) > 0 || s.cfg.Plugins.RequireSigned {
		s.pluginManager.SetTrust(keys, s.cfg.Plugins.RequireSigned)
		slog.Info("Plugin package trust enabled", "trusted_keys", len(keys), "require_signed", s.cfg.Plugins.RequireSigned)
	}
	if maxConc := s.cfg.Plugins.MaxConcurrent; maxConc > 0 {
		s.pluginManager.SetMaxConcurrent(maxConc)
	}
	if maxCPU := s.cfg.Plugins.MaxCPUSeconds; maxCPU > 0 {
		s.pluginManager.SetMaxCPUSeconds(maxCPU)
	}
	if maxWrite := s.cfg.Plugins.MaxWriteMB; maxWrite > 0 {
		s.pluginManager.SetMaxWriteMB(maxWrite)
	}
	pluginDir := filepath.Join(s.cfg.Server.DataDir, "plugins")
	if err := os.MkdirAll(pluginDir, 0750); err != nil {
		slog.Warn("Failed to create plugin data directory", "dir", pluginDir, "err", err)
	}
	if err := s.pluginManager.LoadFromDisk(pluginDir); err != nil {
		slog.Warn("Failed to load plugins from data directory", "dir", pluginDir, "err", err)
	}
	if err := s.pluginManager.LoadFromDisk("plugins"); err != nil {
		slog.Warn("Failed to load bundled plugins", "dir", "plugins", "err", err)
	}

	// Register event handlers
	s.eventManager.On(EventImplantCheckin, func(evt Event) {
		s.dispatchEvent(evt, true)
	})
	s.eventManager.On(EventImplantDisconnect, func(evt Event) {
		s.dispatchEvent(evt, true)
	})
	s.eventManager.On(EventTaskComplete, func(evt Event) {
		s.dispatchEvent(evt, false)
	})
	s.eventManager.On(EventTaskFail, func(evt Event) {
		// Failures go through the alert/notification path so operators get
		// pushed on task errors, not just webhooks/email.
		s.dispatchEvent(evt, true)
	})
	s.eventManager.On(EventCredentialFound, func(evt Event) {
		s.dispatchEvent(evt, true)
	})
	s.migrateAutomationRules()
	s.registerBuiltinAutomations()

	s.monitorCollector = NewMonitorCollector(s)
	s.monitorCollector.Start()

	s.loadScriptsFromDB()
	s.installScriptingBridge()

	return s
}

// dispatchEvent dispatches webhooks, notifications, alerts, and automation rules for an event.
func (s *Server) dispatchEvent(evt Event, includeAlert bool) {
	s.triggerWebhooks(evt)
	s.triggerEmailNotifications(evt)
	if includeAlert {
		s.TriggerAlertForEvent(evt)
	}
	rules := s.loadAutomationRules()
	// Attribute the event once, only when a rule could fire: the tenant
	// rides the event into ruleMayFireOn, executeAction targeting, and
	// the scripting bridge caller.
	needTenant := false
	for _, rule := range rules {
		if rule.Enabled && rule.EventType == string(evt.Type) {
			needTenant = true
			break
		}
	}
	if needTenant && evt.AgentID != "" {
		if tid, ok := s.agentTenantOf(evt.AgentID); ok {
			evt.TenantID = tid
		}
	}
	for _, rule := range rules {
		if rule.Enabled && rule.EventType == string(evt.Type) && s.ruleMayFireOn(rule.TenantID, evt.AgentID) {
			s.evaluateRule(evt, rule)
		}
	}
}

func (s *Server) InitOptimizations(configPath string) {
	s.configPath = configPath
	s.configReloader = NewConfigReloader(s.cfg, configPath, func(cfg *config.Config, changed []string) {
		slog.Info("Config reloaded, applying runtime changes", "changed", changed)

		s.configMu.Lock()
		s.cfg.CopyFrom(cfg)
		s.configMu.Unlock()
		// Malleable/profile settings are part of the delivered network config;
		// refresh every agent on its next beacon rather than waiting for the
		// periodic window.
		s.markNetworkConfigChanged()

		// Table-driven sync: every hot token either runs its Apply hook or
		// is documented live-read (Apply == nil). Static tokens are rejected
		// upstream in reload(); seeing one here is a programming error.
		byToken := make(map[string]reloadGroup, len(reloadGroups()))
		for _, g := range reloadGroups() {
			byToken[g.Token] = g
		}
		outcome := &reloadOutcome{At: time.Now(), Failed: map[string]string{}}
		for _, field := range changed {
			g, ok := byToken[field]
			if !ok {
				outcome.Failed[field] = "unknown token (diffConfig/table drift)"
				continue
			}
			if g.Mode != reloadHot {
				outcome.Failed[field] = "static field requires restart"
				continue
			}
			outcome.Changed = append(outcome.Changed, field)
			if g.Apply == nil {
				outcome.Applied = append(outcome.Applied, field)
				continue
			}
			if err := g.Apply(s); err != nil {
				slog.Error("Config reload hook failed, previous value kept where possible", "field", field, "err", err)
				outcome.Failed[field] = err.Error()
				continue
			}
			outcome.Applied = append(outcome.Applied, field)
			slog.Info("Config reload hook applied", "field", field)
		}
		if len(outcome.Failed) == 0 {
			outcome.Failed = nil
		}
		recordReloadOutcome(outcome)
		s.LogAuditRecord(nil, "config_reload", "settings", "", "fields: "+strings.Join(outcome.Applied, ","), len(outcome.Failed) == 0, nil)

		// Invalidate automation rule cache so next request fetches fresh rules
		s.automationRulesMu.Lock()
		s.automationRulesAt = time.Time{}
		s.automationRulesMu.Unlock()

		// Invalidate nav stats cache
		s.navStatsCacheMu.Lock()
		s.navStatsCache = nil
		s.navStatsCacheMu.Unlock()

		slog.Info("Config reload applied successfully")
	})
	// r.cfg aliases s.cfg until the first reload re-points it, so diffConfig
	// must read it under the same lock the settings saves now write under.
	s.configReloader.TrackLiveConfig(&s.configMu)
	if err := s.configReloader.Start(); err != nil {
		slog.Warn("Failed to start config reloader", "error", err)
	}

	backupDir := filepath.Join(s.cfg.Server.DataDir, "backups")

	if !isSQLiteDB(s.db) {
		// Backups are implemented on SQLite's VACUUM INTO; on other drivers the
		// endpoints answer 501 with guidance instead of pretending to work.
		slog.Warn("Built-in database backups disabled for non-SQLite driver; use an external backup tool",
			"driver", s.db.Dialector.Name())
		return
	}

	var err error
	s.backupManager, err = NewBackupManager(s.db, s.cfg.Database.Path, backupDir, s.backupKeyHex())
	if err != nil {
		slog.Warn("Failed to initialize backup manager", "error", err)
		return
	}
	s.backupManager.Sidecar = func() backupSidecar {
		s.configMu.RLock()
		defer s.configMu.RUnlock()
		return backupSidecar{
			Version:       "fbk1",
			CreatedAt:     time.Now().UTC().Format(time.RFC3339),
			KeyID:         s.backupKeyID(),
			ServerVersion: ServerVersion,
			Host:          s.cfg.Server.Host,
			Port:          s.cfg.Server.Port,
			TLSEnabled:    s.cfg.Server.TLSEnabled,
			DBDriver:      s.cfg.Database.Driver,
		}
	}
	if retain := s.cfg.Backup.Retain; retain > 0 {
		s.backupManager.SetRetention(retain)
	}

	schedule := s.cfg.Backup.Schedule
	if schedule == "" {
		schedule = "daily"
	}
	if err := s.backupManager.Start(schedule); err != nil {
		slog.Warn("Failed to start backup manager", "error", err)
	}
}

func (s *Server) SetupRoutes() { // config-lock: boot, before the listener serves
	// Request logging middleware
	s.router.Use(middleware.RequestLogger())

	// CORS middleware — applies to all routes
	s.router.Use(middleware.CORS(s.cfg.Server.AllowedOrigins))

	// Unauthenticated routes
	s.registerPublicRoutes()

	// Protected routes
	auth := s.router.Group("/")
	auth.Use(s.operatorPlaneGuard())
	auth.Use(middleware.AuthRequired(s.db))
	auth.Use(middleware.CSRFProtect())
	auth.Use(middleware.RequestBodyLimit(MaxJSONBodySize))
	auth.Use(s.apiRateLimiter.LimitByUser())
	auth.Use(s.AuditMiddleware())
	auth.Use(s.ActivityMiddleware())
	{
		s.registerAgentRoutes(auth)
		s.registerAgentCommandRoutes(auth)
		s.registerAutomationRoutes(auth)
		s.registerPluginRoutes(auth)
		s.registerTaskRoutes(auth)
		s.registerCredentialRoutes(auth)
		s.registerGenerateRoutes(auth)
		s.registerListenerRoutes(auth)
		s.registerReconRoutes(auth)
		s.registerSettingsRoutes(auth)
		s.registerDebugRoutes(auth)
		s.registerUserRoutes(auth)
		s.registerCampaignRoutes(auth)
		s.registerIntegrationRoutes(auth)
		s.registerDNSRoutes(auth)
		s.registerExtendedRoutes(auth)
		s.registerAPIKeyRoutes(auth)
	}

	// Agent Beacon API (no auth — agents check in unauthenticated)
	beaconAPI := s.router.Group("/api/v1")
	beaconAPI.Use(s.rateLimiter.Limit())
	beaconAPI.Use(s.trafficMiddleware())
	beaconAPI.Use(middleware.RequestBodyLimit(BeaconMaxBodySize))
	{
		beaconAPI.POST("/beacon", s.handleBeacon)
		beaconAPI.POST("/screen_frame", s.handleScreenFrame)

		// Malleable profile support (similar to Cobalt Strike)
		beaconAPI.POST("/generate_204", s.handleBeacon)
		beaconAPI.POST("/th", s.handleBeacon)
		beaconAPI.GET("/generate_204", s.handleBeacon)
		beaconAPI.GET("/th", s.handleBeacon)
		// Default analytics-mimic endpoint (canonical default beacon_uri).
		beaconAPI.POST("/collect", s.handleBeacon)
		beaconAPI.GET("/collect", s.handleBeacon)
	}

	// Protected REST API (authentication required). The operator-plane guard
	// (operator_allowed_cidrs + operator mTLS) applies here too: these routes
	// are the scripting/automation surface, so leaving them outside the guard
	// let any authenticated caller bypass the operator network policy.
	// /api/v1/health stays exempt so container/orchestrator probes keep working.
	restAPI := s.router.Group("/api/v1")
	restAPI.Use(s.operatorPlaneGuardExcept("/api/v1/health"))
	restAPI.Use(middleware.AuthRequired(s.db))
	restAPI.Use(middleware.CSRFProtect())
	restAPI.Use(middleware.RequestBodyLimit(MaxJSONBodySize))
	restAPI.Use(s.apiRateLimiter.LimitByUser())
	{
		s.registerAPIRoutes(restAPI)
	}

	// Root-level malleable profile routes (agent beacon_uri does NOT include /api/v1/ prefix).
	// These sit outside the beaconAPI group, so apply the same rate limit and
	// traffic capture explicitly. Without the limiter, these endpoints were an
	// unbounded path into the decrypt hot path.
	s.router.POST("/generate_204", middleware.RequestBodyLimit(BeaconMaxBodySize), s.rateLimiter.Limit(), s.trafficMiddleware(), s.handleBeacon)
	// GET carries the limit too: handleBeacon does c.GetRawData() which
	// buffers the whole body BEFORE decodeBeaconEnvelope's length check can
	// fire — an unauthenticated chunked body was a memory-amplification vector.
	s.router.GET("/generate_204", middleware.RequestBodyLimit(BeaconMaxBodySize), s.rateLimiter.Limit(), s.trafficMiddleware(), s.handleBeacon)
	s.router.POST("/collect", middleware.RequestBodyLimit(BeaconMaxBodySize), s.rateLimiter.Limit(), s.trafficMiddleware(), s.handleBeacon)
	s.router.GET("/collect", middleware.RequestBodyLimit(BeaconMaxBodySize), s.rateLimiter.Limit(), s.trafficMiddleware(), s.handleBeacon)

	// Catch-all for profile-defined beacon URIs (e.g. bing /th?id=...)
	s.router.GET("/th", middleware.RequestBodyLimit(BeaconMaxBodySize), s.rateLimiter.Limit(), s.trafficMiddleware(), s.handleBeacon)
	s.router.POST("/th", middleware.RequestBodyLimit(BeaconMaxBodySize), s.rateLimiter.Limit(), s.trafficMiddleware(), s.handleBeacon)
	// Custom profile URIs must beacon even when no global malleable preset is
	// enabled: per-implant profiles bake arbitrary URIs at build time. Gate on
	// the Accept header (not the malleable flag) so SPA/API traffic still 404s
	// while any non-JSON client falls through to beacon decoding, which
	// rejects non-beacon bodies with 400. Rate limit + body limit apply
	// before the decrypt hot path in all cases.
	s.router.NoRoute(func(c *gin.Context) {
		if c.GetHeader("Accept") != "application/json" {
			middleware.RequestBodyLimit(BeaconMaxBodySize)(c)
			if c.IsAborted() {
				return
			}
			s.rateLimiter.Limit()(c)
			if c.IsAborted() {
				return
			}
			s.handleBeacon(c)
			return
		}
		respondError(c, http.StatusNotFound, "not found")
	})

	// External C2 (redirector-facing, no auth, rate-limited)
	extc2 := s.router.Group("/extc2/v1")
	extc2.Use(s.rateLimiter.Limit())
	{
		extc2.POST("/receive", s.handleExtC2Receive)
		extc2.POST("/send", s.handleExtC2Send)
	}

	// Post-group authenticated routes
	s.registerMonitorRoutes(auth)
	s.registerDashboardCharts(auth)
	s.registerBOFRoutes(auth)
	s.registerMiscRoutes(auth)
}
