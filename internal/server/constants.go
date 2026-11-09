package server

import "time"

// ServerVersion is overridden at release time via
// -ldflags "-X github.com/forgec2/forgec2/internal/server.ServerVersion=<tag>".
// A const cannot be overridden that way, hence var. Local builds report "dev".
var ServerVersion = "dev"

const (
	BeaconRateLimit  = 100
	BeaconRateWindow = 1 * time.Minute

	LoginRateWindow      = 1 * time.Minute
	RateLimiterCleanup   = 5 * time.Minute
	DefaultPageSize      = 20
	MaxPageSize          = 100
	DefaultTaskPageSize  = 50
	MaxTaskPageSize      = 200
	AgentDetailTaskLimit = 50
	AgentTasksLimit      = 20
	DashboardRecentTasks = 5
	BeaconTaskFetchLimit = 10

	MaxUploadSize = 50 * 1024 * 1024 // 50 MB max for file transfers
	MaxResultSize = 1 * 1024 * 1024  // 1 MB max per task result to prevent DB bloat
	// HostedPayloadTTL bounds how long one-liner payload downloads stay on
	// disk. NOTE: payloads are NOT content-addressed/deduplicated across
	// requests on purpose — each build embeds a fresh per-implant v3
	// registration secret that binds to exactly one agent on first check-in
	// (see bindRegSecret), so re-serving an older build to a second operator
	// would hand out an implant that can never register.
	HostedPayloadTTL        = 24 * time.Hour  // hosted one-liner payload retention
	MaxJSONBodySize         = 2 * 1024 * 1024 // 2 MB max for JSON/form request bodies
	MaxPendingTasksPerAgent = 50              // max pending tasks per agent before rejecting new ones
	// AbortReserveSlots are extra headroom slots beyond MaxPendingTasksPerAgent
	// reserved for abort injections: cancelling a running task must be able to
	// queue its abort even when the agent's backlog is full.
	AbortReserveSlots = 2
	// ApprovalExpiryDuration bounds how long a task waits in pending_approval
	// before auto-reject. Stale approvals are a confused-deputy risk (the
	// world changed since the operator hit "create").
	ApprovalExpiryDuration = 24 * time.Hour
	MaxCommandLength       = 10000            // max characters in a command string
	MaxNotesLength         = 5000             // max characters in agent notes/tags
	MaxChatMessageBytes    = 8 * 1024         // max bytes in a chat message
	MaxShellcodeSize       = 10 * 1024 * 1024 // 10 MB max decoded shellcode payload
	MaxTechniqueLength     = 64               // max characters in an injection/spawn technique name
	MaxTargetLength        = 128              // max characters in a spawn target executable

	// ─── Map Size Limits ───
	MaxWSConnections     = 256              // max concurrent WebSocket dashboard clients
	MaxOperatorWSConns   = 64               // max concurrent operator /ws/operator sockets
	MaxExtC2QueuePerChan = 200              // max queued tasks per ext C2 channel before dropping
	MaxBuildJobs         = 50               // max concurrent build jobs
	MaxScreenMonitors    = 100              // max screen monitor registrations
	MaxRPortFwdListeners = 100              // max reverse port forward listeners
	MaxExtraListeners    = 64               // max dynamically created listeners
	MaxDomainFrontStatus = 128              // max domain fronting entries
	StaleMapCleanupAge   = 30 * time.Minute // clean map entries older than this

	// Public (unauthenticated) route budgets — IP-based, independent of the
	// failure lockout so even successful requests cannot be flooded.
	PublicDownloadRate = 60 // max public payload/stage downloads per IP per window
	LoginRequestRate   = 10 // max login POSTs per IP per window (lockout handles failures)

	// Remote desktop input bounds. The agent-side dispatcher understands
	// move/click/key only, and coordinates are injected straight into
	// SetCursorPos/keybd_event without validation agent-side — so the API
	// boundary is the only place that can reject garbage.
	MaxRemoteInputCoord  = 32767 // reject |x|/|y| beyond this
	MaxRemoteInputKeyLen = 32    // reject longer key names
	// MaxRemoteInputEventsPerWindow throttles accepted input events per
	// (operator, agent): every event becomes a task row plus an audit row,
	// so an unbounded stream is a write-amplification primitive.
	MaxRemoteInputEventsPerWindow = 40
	RemoteInputWindow             = 1 * time.Second
	// Screen monitor stream command bounds (interval/quality validated in the
	// handler as well): width is spliced into the agent's CSV command, so an
	// arbitrary string there was parameter injection.
	MinScreenMonitorWidth = 16
	MaxScreenMonitorWidth = 16384

	// SOCKS Relay
	SocksMaxFrameSize   = 64 * 1024       // 64 KB per relay frame
	SocksFastInterval   = 500             // ms – agent fast-poll when relay active
	SocksCleanupTimeout = 5 * time.Minute // clean dead connections after 5 min
	SocksMaxConns       = 256             // max concurrent connections per relay session
	// Low-MTU transports (udp/icmp/dns) ride single datagrams: tunnel data
	// for one beacon is split into ~1KB frames capped at ~4KB total, with
	// the remainder requeued losslessly for the next beacon. Stream
	// transports (http/tcp/grpc/quic/ssh/smb) keep 64KB uncapped frames.
	SocksLowMTUFrameSize = 1024 // per-frame data cap on datagram transports
	SocksLowMTUBudget    = 4096 // total tunnel data bytes per beacon on datagram transports

	// ─── Query Limits ───
	APIAgentListLimit      = 500
	APITaskListLimit       = 200
	APICredentialListLimit = 500
	APIListenerListLimit   = 100
	APIAuditLogListLimit   = 200
	ExportTaskLimit        = 10000
	CSVResultTruncLen      = 500
	CSVErrorTruncLen       = 500
	// HandoverTaskLimit / HandoverTaskChunk bound the handover bundle's task
	// export. Task.Result is ciphertext capped at MaxResultSize (1 MiB) per row,
	// so the read is chunked: decrypt first and truncate second, with at most
	// HandoverTaskChunk rows resident at a time instead of the whole window.
	HandoverTaskLimit    = 50000
	HandoverTaskChunk    = 1000
	HandoverResultMaxLen = 4096
	// IOCScanTaskLimit / IOCScanChunk bound the indicator sweep over task
	// output. Task.Result is capped at MaxResultSize (1 MiB) per row, so
	// reading the whole window in one query could materialise tens of GiB. The
	// sweep chunks instead of truncating: a size cut would silently drop
	// indicators, and SUBSTR cannot be used because the column is ciphertext.
	IOCScanTaskLimit = 20000
	IOCScanChunk     = 500
	// IOCScanHostLimit / IOCScanHostChunk bound the network-host half of the
	// same sweep.
	IOCScanHostLimit       = 10000
	IOCScanHostChunk       = 2000
	TopologyAgentLimit     = 5000
	LootAgentLimit         = 5000
	AgentQueryLimit        = 5000
	AutoTagRuleLimit       = 200
	AutoTagAgentLimit      = 5000
	AutoTagAssignmentLimit = 50000
	// AutoTagApplyBatchLimit caps how many assignments one apply call may
	// insert in a single batched transaction.
	AutoTagApplyBatchLimit = 20000
	BloodHoundResultLimit  = 50000
	CampaignTaskLimit      = 10000
	MITRETimelineLimit     = 500
	ClaudeMaxTokens        = 4096
	MaxBOFResultLimit      = 100
	AutomationRuleLimit    = 200
	// AgentStatusHistoryLimit caps the newest status-history rows returned per
	// agent; a flapping agent can accumulate tens of thousands of events in
	// one retention window.
	AgentStatusHistoryLimit = 2000

	// ─── Timeouts ───
	HTTPReadTimeout  = 30 * time.Second
	HTTPWriteTimeout = 60 * time.Second
	HTTPIdleTimeout  = 120 * time.Second

	// ─── HTTP Client / Background Cleanup / Misc ───
	HTTPClientMaxIdleConns    = 10
	HTTPClientMaxIdlePerHost  = 3
	PwdChangeCleanupInterval  = 10 * time.Minute
	SeqLockoutCleanupInterval = 10 * time.Minute
	SocksStatsFlushEvery      = 100
	AgentStatusCooldown       = 60 * time.Second
	ExtC2WriteTimeout         = 10 * time.Second

	WSReadDeadline      = 60 * time.Second
	WSPingInterval      = 30 * time.Second
	WSWriteDeadline     = 10 * time.Second
	WSMaxMessageSize    = 512 * 1024 // 512 KB
	WSReadBufSize       = 16 * 1024  // 16 KB
	WSWriteBufSize      = 16 * 1024  // 16 KB
	BatchFlushDelay     = 1 * time.Second
	BatchFlushThreshold = 16

	TCPReadDeadline        = 60 * time.Second
	TCPWriteDeadline       = 10 * time.Second
	ActivityUpdateThrottle = 60 * time.Second

	BeaconPingInterval    = 30 * time.Second
	BeaconWriteDeadline   = 10 * time.Second
	BeaconReadDeadline    = 60 * time.Second
	OperatorWriteDeadline = 5 * time.Second

	SOCKSHandshakeTimeout  = 30 * time.Second
	SOCKSRelayWriteTimeout = 10 * time.Second

	RemoteDesktopWriteDeadline = 5 * time.Second
	RemoteDesktopReadDeadline  = 5 * time.Minute
	// RemoteDesktopPingInterval keeps a read-idle operator stream alive: the
	// legacy /ws/remote-desktop read loop only extends its read deadline on
	// pongs, so without server pings every session died at the 5-minute
	// deadline even while actively watching.
	RemoteDesktopPingInterval = 30 * time.Second
	// MaxRDOperatorsPerAgent / MaxRDOperatorConnections bound the legacy RD
	// websocket hub. Without caps, one authenticated operator could open
	// unlimited sockets (each a goroutine + read buffer) against one agent.
	MaxRDOperatorsPerAgent   = 8
	MaxRDOperatorConnections = 64
	MonitorMetricsInterval   = 30 * time.Second
	MonitorAlertInterval     = 1 * time.Minute
	// agentReapPageSize bounds one page of the offline-sweep query so the
	// reaper stays complete past a single page on large fleets.
	agentReapPageSize = 5000

	AIAPITimeout              = 120 * time.Second
	AITaskWaitMax             = 60 * time.Second
	AITaskPollMinInterval     = 250 * time.Millisecond
	WebhookHTTPTimeout        = 10 * time.Second
	AutomationDownloadTimeout = 60 * time.Second
	// GeoIPLookupTimeout bounds one asynchronous agent-IP lookup. It must cover
	// the whole provider chain (3 providers x 4s), or the first provider being
	// blackholed — ip-api.com from some networks — would consume the budget
	// before the fallback that works there is tried.
	GeoIPLookupTimeout       = 12 * time.Second
	ReachabilityDialTimeout  = 5 * time.Second
	DomainHealthCheckTimeout = 8 * time.Second
	WebhookDeliveryTimeout   = 15 * time.Second
	UpdateCheckInitialDelay  = 10 * time.Second
	UpdateCheckInterval      = 1 * time.Hour
	// UpdateCheckJitter spreads the background checks so a fleet of servers
	// that booted together does not hit api.github.com in lockstep.
	UpdateCheckJitter       = 5 * time.Minute
	GitHubAPITimeout        = 15 * time.Second
	GracefulShutdownDelay   = 500 * time.Millisecond
	UpdateDownloadTimeout   = 5 * time.Minute
	ChecksumDownloadTimeout = 30 * time.Second
	// updateRestartDeadlineSeconds bounds how long the generated restarter
	// waits for the old process to exit and for the new build to report a
	// healthy boot before it rolls back.
	updateRestartDeadlineSeconds = 60
	// updateLockStaleAfter is how long a leftover update lock is honoured
	// before a new attempt reclaims it.
	updateLockStaleAfter = 30 * time.Minute
	// updateResultWaitTimeout bounds how long a freshly started server waits
	// for the restarter to publish the outcome of the update it just booted.
	updateResultWaitTimeout   = 30 * time.Second
	CBCheckInterval           = 60 * time.Second
	CBDialTimeout             = 5 * time.Second
	CBHealthCheckTimeout      = 5 * time.Second
	ConfigReloadDebounce      = 500 * time.Millisecond
	CacheCleanupInterval      = 5 * time.Minute
	GRPCMaxRecvMsgSize        = 10 * 1024 * 1024 // 10 MB
	PluginUpdateCheckInterval = 6 * time.Hour
	ACMEProvisionTimeout      = 5 * time.Minute
	PeriodicCleanupInterval   = 24 * time.Hour
	RPortFwdCleanupInterval   = 5 * time.Minute

	// ─── Stale Agent Thresholds ───
	GhostAgentCutoff        = 1 * time.Hour
	PayloadCleanupInterval  = 30 * time.Minute
	ListenerActiveThreshold = 5 * time.Minute
	MaxReportDateRange      = 365 * 24 * time.Hour

	// ─── Beacon Processing ───
	CallbackHTTPTimeout      = 30 * time.Second
	StaleTaskRequeueInterval = 5 * time.Minute
	StaleRunningTaskTimeout  = 10 * time.Minute
	// BeaconMaxBodySize caps unauthenticated beacon payloads. Beacons are
	// ECDH-encrypted (base64 ~1.33x expansion) and may carry file chunks, so the
	// cap is larger than MaxJSONBodySize while still bounding memory exhaustion.
	BeaconMaxBodySize = 64 * 1024 * 1024
	// AckedTaskResultTimeout bounds tasks the agent acknowledged but never
	// returned a result for. Acknowledged tasks are never dispatched twice (they
	// may still be executing), so after this window they are marked failed
	// instead of being left "running" forever.
	AckedTaskResultTimeout = 30 * time.Minute
	AuditLogResultMaxLen   = 300
	AuditLogDetailsMaxLen  = 600

	// ─── Auth / Session ───
	DefaultSessionHours = 24
	SecondsPerHour      = 3600
	RememberMeMaxAgeSec = 7 * 86400
	LangCookieMaxAgeSec = 365 * 24 * 3600

	// ─── Validation Bounds ───
	MinOfflineThresholdSec  = 5
	MaxOfflineThresholdSec  = 3600
	MinSessionMaxAgeHours   = 1
	MaxSessionMaxAgeHours   = 720
	MinCleanupRetentionDays = 1
	MaxCleanupRetentionDays = 365
	MinHTTPStatusCode       = 100
	MaxHTTPStatusCode       = 599

	// ─── Auth Alert Defaults ───
	DefaultMaxLoginAttempts = 5
	DefaultLoginWindowSec   = 60
	DefaultLockoutTimeSec   = 900

	// ─── AI ───
	// Keep normal long-form model replies intact. The old 8 KB ceiling cut
	// common 4K-token answers roughly halfway through (and could split a UTF-8
	// rune). Rendering/history have matching, slightly larger client ceilings.
	AIResponseTruncLen   = 64 * 1024
	AIThinkingPreviewLen = 300
	AIErrorBodyTruncLen  = 500
	aiRetryMax           = 2
	// Tool results keep enough structured rows for useful summaries. Oversized
	// payloads are reduced to valid JSON with explicit partial-result metadata.
	AIToolResultTruncLen = 1000
	AITaskResultTruncLen = 2000
	AIStreamBufSize      = 8192
	// Providers send tiny token deltas. We expose cumulative snapshots to the
	// browser for backward compatibility, but only after a useful increment so
	// long replies do not create quadratic SSE traffic and render churn.
	// Reduced to 128 for latency-first streaming (chosen).
	AIStreamEmitMinBytes    = 128
	AIChatRequestMaxBytes   = 2 << 20
	AIChatMaxMessages       = 512
	AIConfigRequestMaxBytes = 64 << 10
	// Safety ceilings applied when config.yaml leaves the corresponding
	// max_* field at 0 ("unlimited"). Without these, a tool-calling model
	// can loop forever and the SSE request never ends (UI appears frozen).
	AISafetyMaxTurns          = 16
	AISafetyMaxToolRounds     = 8
	AISafetyMaxDuplicateTools = 2
	AIRoundTimeout            = 3 * time.Minute
	AIStreamChanBuf           = 256
	AIStreamHeartbeatInterval = 15 * time.Second
	AISSEMaxBufferBytes       = 2 << 20 // 2 MB cap for SSE buffer growth
	AIWebSearchTimeout        = 8 * time.Second
	AIWebSearchMaxResults     = 6
	AIWebSearchMaxQueryLen    = 200

	// ─── DNS ───
	DNSTXTChunkSize = 255

	// ─── Misc ───
	MaxSleepSeconds       = 86400
	MaxJitterPercent      = 100
	DefaultRedirectorPort = 443
	DefaultSSHPort        = 22
	CertFileName          = "fullchain.pem"
	KeyFileName           = "privkey.pem"
	AnthropicAPIVersion   = "2023-06-01"

	// ─── Beacon Dedup ───
	MaxBeaconDedupEntries = 10000
	BeaconDedupWindow     = 5 * time.Second  // window for duplicate detection
	BeaconDedupStaleAge   = 30 * time.Second // entries older than this are purged
	BeaconDedupCleanup    = 60 * time.Second // cleanup ticker interval

	// ─── Beacon Protocol v2 ───
	// BeaconSessionRekeyMessages is the session message count after which the
	// agent is asked to rotate keying material with a fresh handshake.
	BeaconSessionRekeyMessages = 10000

	// ─── Batch / Request Limits ───
	MaxBatchAgentLimit   = 500
	MaxTaskIDsPerRequest = 200
	MaxBulkCancelLimit   = 100
	PasswordHistoryMax   = 10
	DefaultBOFLimit      = 20
	TopoRecencyMinutes   = 30
	TopoAgentLimit       = 30

	// ─── Plugin / Callback Timeouts ───
	PluginHookTimeout = 30 * time.Second

	// ─── Server Lifecycle ───
	GracefulShutdownTimeout = 15 * time.Second
	InFlightDrainTimeout    = 5 * time.Second
	// WorkerDrainTimeout bounds the final wait for background goroutines.
	// A wedged worker must never hang shutdown forever (the orchestrator
	// would SIGKILL mid-WAL and risk DB corruption); on timeout we log a
	// goroutine dump and proceed to close the database.
	WorkerDrainTimeout      = 30 * time.Second
	HTTPClientShortTimeout  = 30 * time.Second
	HTTPClientLongTimeout   = 5 * time.Minute
	HTTPMaxIdleConns        = 20
	HTTPMaxIdleConnsPerHost = 5
	HTTPIdleConnTimeout     = 90 * time.Second
	TCPMaxMessageSize       = 16 * 1024 * 1024 // 16 MB
	GeoIPSemaphoreSize      = 10
	TaskWorkerPoolSize      = 32

	// ─── Agent Offline / Stale ───
	DefaultOfflineThresholdSec  = 60
	StaleThresholdMultiplier    = 3
	DefaultCleanupRetentionDays = 30
	// Retention tiers: forensic tables floor at 90d / default 365d; terminal
	// task auto-delete floors at 90d regardless of ops retention.
	DefaultAuditRetentionDays = 365
	MinAuditRetentionDays     = 90
	MinTaskRetentionDays      = 90

	// ─── Backup ───
	BackupRetainCount = 7

	// ─── Auth / Security ───
	BcryptCost             = 12
	AuthUserCacheTTL       = 5 * time.Minute
	PasswordChangeCooldown = 5 * time.Minute

	// ─── Activity Middleware ───
	ActivityCleanupInterval = 10 * time.Minute

	// ─── Audit / SIEM ───
	AuditAlertCheckInterval = 10 * time.Minute
	LockoutEntryMaxEntries  = 10000
	LockoutCleanupInterval  = 10 * time.Minute
	LockoutStaleGrace       = 5 * time.Minute
	SIEMBatchSize           = 100
	SIEMBatchInterval       = 10 * time.Second
	SIEMHTTPTimeout         = 10 * time.Second
)
