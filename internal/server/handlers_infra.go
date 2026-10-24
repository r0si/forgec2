package server

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/infrastructure"
	"github.com/gin-gonic/gin"
)

type infraGenerateRequest struct {
	Domain     string   `json:"domain"`
	ListenPort int      `json:"listen_port"`
	BackendURL string   `json:"backend_url"`
	CertPath   string   `json:"cert_path"`
	KeyPath    string   `json:"key_path"`
	WSEnabled  bool     `json:"ws_enabled"`
	ExtC2Paths []string `json:"extc2_paths"`
	BlockedIPs []string `json:"blocked_ips"`
	UserAgent  string   `json:"user_agent"`
	Profile    string   `json:"profile"`
}

type acmeProvisionRequest struct {
	Domain     string `json:"domain"`
	Email      string `json:"email"`
	Port       int    `json:"port"`
	UseStaging bool   `json:"use_staging"`
}

func (s *Server) handleInfrastructurePage(c *gin.Context) {
	listeners := s.getListeners()
	s.renderPageOrJSON(c, gin.H{
		"Title":     "Infrastructure Automation",
		"ActiveNav": "infrastructure",
		"Listeners": listeners,
	})
}

func (s *Server) handleGenerateNginx(c *gin.Context) {
	var req infraGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid request")
		return
	}
	rc := toRedirectorConfig(req, "nginx")
	config := infrastructure.GenerateNginxConfig(rc)
	respond(c, gin.H{"config": config})
}

func (s *Server) handleGenerateApache(c *gin.Context) {
	var req infraGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid request")
		return
	}
	rc := toRedirectorConfig(req, "apache")
	config := infrastructure.GenerateApacheConfig(rc)
	respond(c, gin.H{"config": config})
}

func (s *Server) handleGenerateHAProxy(c *gin.Context) {
	var req infraGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid request")
		return
	}
	rc := toRedirectorConfig(req, "haproxy")
	config := infrastructure.GenerateHAProxyConfig(rc)
	respond(c, gin.H{"config": config})
}

func (s *Server) handleACMECertProvision(c *gin.Context) {
	var req acmeProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErrorSafe(c, http.StatusBadRequest, err, "invalid request")
		return
	}
	if req.Domain == "" || req.Email == "" {
		respondError(c, http.StatusBadRequest, "domain and email are required")
		return
	}

	safeDomain := sanitizeRe.ReplaceAllString(req.Domain, "_")
	if safeDomain == "" {
		respondError(c, http.StatusBadRequest, "invalid domain")
		return
	}

	dataDirRoot := s.rawDataDir()
	dataDir := filepath.Join(dataDirRoot, "certs", safeDomain)
	resolvedDir := filepath.Clean(dataDir)
	baseDir := filepath.Clean(filepath.Join(dataDirRoot, "certs"))
	if !strings.HasPrefix(resolvedDir, baseDir+string(os.PathSeparator)) && resolvedDir != baseDir {
		respondError(c, http.StatusBadRequest, "invalid domain")
		return
	}
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Infrastructure mkdir"))
		return
	}

	acmeCfg := infrastructure.ACMEConfig{
		Domain:     req.Domain,
		Email:      req.Email,
		DataDir:    dataDir,
		UseStaging: req.UseStaging,
		Port:       req.Port,
	}

	client := infrastructure.NewACMEClient(acmeCfg)
	ctx, cancel := context.WithTimeout(context.Background(), ACMEProvisionTimeout)
	defer cancel()

	certPEM, keyPEM, err := client.Provision(ctx)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "ACME provision"))
		return
	}

	certFile := filepath.Join(dataDir, "fullchain.pem")
	keyFile := filepath.Join(dataDir, "privkey.pem")

	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Write cert"))
		return
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Write key"))
		return
	}

	// Parse the actual certificate to report its real NotAfter expiry instead
	// of an invented "now + 80 days" estimate.
	expires := ""
	if block, _ := pem.Decode(certPEM); block != nil {
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			expires = cert.NotAfter.Format(time.RFC3339)
		}
	}

	respond(c, gin.H{
		"cert_file": certFile,
		"key_file":  keyFile,
		"expires":   expires,
	})
}

func (s *Server) handleProfileExport(c *gin.Context) {
	format := c.DefaultQuery("format", "json")

	s.configMu.RLock()
	profile := s.cfg.Malleable // struct copy: the header maps are replaced, not mutated in place
	s.configMu.RUnlock()

	var content string
	switch format {
	case "json":
		data, err := json.MarshalIndent(profile, "", "  ")
		if err != nil {
			slog.Error("JSON marshal indent failed", "error", err)
			respondError(c, http.StatusInternalServerError, "failed to marshal profile")
			return
		}
		content = string(data)
	case "nginx":
		content = fmt.Sprintf(`# ForgeC2 Malleable Profile 鈥?Nginx map
# Apply to your nginx redirector to replicate C2 response behavior

map $request_uri $c2_content_type {
    default "%s";
}

map $request_uri $c2_status_code {
    default %d;
}
`, profile.ContentType, profile.StatusCode)
	case "env":
		content = fmt.Sprintf(`# ForgeC2 Malleable Profile 鈥?Environment variables
FORGEC2_PROFILE_NAME=%s
FORGEC2_STATUS_CODE=%d
FORGEC2_CONTENT_TYPE=%s
`, profile.ProfileName, profile.StatusCode, profile.ContentType)
	default:
		respondError(c, http.StatusBadRequest, "unsupported format: "+format)
		return
	}

	respond(c, gin.H{"content": content})
}

func toRedirectorConfig(req infraGenerateRequest, rtype string) infrastructure.RedirectorConfig {
	if req.ExtC2Paths == nil {
		req.ExtC2Paths = []string{}
	}
	if req.ListenPort == 0 {
		req.ListenPort = DefaultRedirectorPort
	}
	return infrastructure.RedirectorConfig{
		Type:       rtype,
		Domain:     req.Domain,
		ListenPort: req.ListenPort,
		BackendURL: req.BackendURL,
		CertPath:   req.CertPath,
		KeyPath:    req.KeyPath,
		ExtC2Paths: req.ExtC2Paths,
		WSEnabled:  req.WSEnabled,
		BlockedIPs: req.BlockedIPs,
		UserAgent:  req.UserAgent,
		Profile:    req.Profile,
	}
}
