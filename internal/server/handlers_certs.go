package server

import (
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleGetCertInfo(c *gin.Context) {
	s.configMu.RLock()
	certPath := s.cfg.Server.CertFile
	s.configMu.RUnlock()
	if certPath == "" {
		certPath = "data/certs/server.crt"
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		respondError(c, http.StatusNotFound, "no certificate found")
		return
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		respondError(c, http.StatusInternalServerError, "failed to decode certificate")
		return
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to parse certificate")
		return
	}

	respond(c, gin.H{
		"subject":        cert.Subject.CommonName,
		"issuer":         cert.Issuer.CommonName,
		"expires_at":     cert.NotAfter,
		"expires_in":     int(time.Until(cert.NotAfter).Hours() / 24),
		"dns_names":      cert.DNSNames,
		"ip_addresses":   cert.IPAddresses,
		"is_self_signed": cert.Subject.CommonName == cert.Issuer.CommonName,
		"serial":         cert.SerialNumber.String(),
		"key_usage":      cert.KeyUsage,
	})
}

func (s *Server) handleRegenerateCert(c *gin.Context) {
	s.configMu.RLock()
	certPath, keyPathCfg := s.cfg.Server.CertFile, s.cfg.Server.KeyFile
	s.configMu.RUnlock()
	if certPath == "" {
		certPath = "data/certs/server.crt"
	}
	keyPath := keyPathCfg
	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(certPath), "server.key")
	}

	// Remove existing files
	if err := os.Remove(certPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("Failed to remove existing cert", "path", certPath, "err", err)
	}
	if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("Failed to remove existing key", "path", keyPath, "err", err)
	}

	if err := crypto.GenerateSelfSignedCert(certPath, keyPath); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to regenerate certificate")
		return
	}

	s.LogAuditRecord(c, "regenerate_cert", "settings", "", "Regenerated self-signed certificate", true, nil)

	// Return new cert info
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		respond(c, gin.H{"status": "certificate regenerated"})
		return
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		respond(c, gin.H{"status": "certificate regenerated"})
		return
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		respond(c, gin.H{"status": "certificate regenerated"})
		return
	}

	respond(c, gin.H{
		"status":         "certificate regenerated (takes effect for new connections immediately, no restart needed)",
		"subject":        cert.Subject.CommonName,
		"issuer":         cert.Issuer.CommonName,
		"expires_at":     cert.NotAfter,
		"expires_in":     int(time.Until(cert.NotAfter).Hours() / 24),
		"dns_names":      cert.DNSNames,
		"ip_addresses":   cert.IPAddresses,
		"is_self_signed": cert.Subject.CommonName == cert.Issuer.CommonName,
	})
}

func (s *Server) handleUploadCert(c *gin.Context) {
	certFile, _, err := c.Request.FormFile("cert")
	if err != nil {
		respondError(c, http.StatusBadRequest, "cert file required")
		return
	}
	defer certFile.Close()

	certData, err := io.ReadAll(io.LimitReader(certFile, 1<<20+1))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read cert file")
		return
	}
	if len(certData) > 1<<20 {
		respondError(c, http.StatusBadRequest, "cert file too large (max 1MB)")
		return
	}

	keyFile, _, err := c.Request.FormFile("key")
	if err != nil {
		respondError(c, http.StatusBadRequest, "key file required")
		return
	}
	defer keyFile.Close()

	keyData, err := io.ReadAll(io.LimitReader(keyFile, 1<<20+1))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read key file")
		return
	}
	if len(keyData) > 1<<20 {
		respondError(c, http.StatusBadRequest, "key file too large (max 1MB)")
		return
	}

	// Validate PEM format
	certBlock, _ := pem.Decode(certData)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		respondError(c, http.StatusBadRequest, "invalid certificate PEM format")
		return
	}
	// Refuse already-expired certificates: installing one would silently
	// break every TLS beacon at the next handshake.
	if parsed, err := x509.ParseCertificate(certBlock.Bytes); err != nil {
		respondError(c, http.StatusBadRequest, "certificate unparseable")
		return
	} else if time.Now().After(parsed.NotAfter) {
		respondError(c, http.StatusBadRequest, "certificate already expired")
		return
	}

	keyBlock, _ := pem.Decode(keyData)
	if keyBlock == nil || (keyBlock.Type != "EC PRIVATE KEY" && keyBlock.Type != "RSA PRIVATE KEY") {
		respondError(c, http.StatusBadRequest, "invalid private key PEM format")
		return
	}

	s.configMu.RLock()
	certPath, keyPathCfg := s.cfg.Server.CertFile, s.cfg.Server.KeyFile
	s.configMu.RUnlock()
	if certPath == "" {
		certPath = "data/certs/server.crt"
	}
	// Honor an explicit key_file: deriving the key next to the certificate
	// silently ignored the configured path and paired the new certificate
	// with whatever key still lived at server.key_file.
	keyPath := keyPathCfg
	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(certPath), "server.key")
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create certificate directory")
		return
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create key directory")
		return
	}

	if err := os.WriteFile(certPath, certData, 0600); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to write certificate")
		return
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to write private key")
		return
	}

	s.LogAuditRecord(c, "upload_cert", "settings", "", "Uploaded custom TLS certificate", true, nil)

	respond(c, gin.H{"status": "certificate uploaded (takes effect for new connections immediately, no restart needed)"})
}
