package infrastructure

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	LetsEncryptProd    = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

type ACMEClient struct {
	config ACMEConfig
	client *acme.Client
	domain string
}

func NewACMEClient(cfg ACMEConfig) *ACMEClient {
	dirURL := LetsEncryptStaging
	if !cfg.UseStaging {
		dirURL = LetsEncryptProd
	}
	return &ACMEClient{
		config: cfg,
		client: &acme.Client{
			DirectoryURL: dirURL,
			UserAgent:    "forgec2-acme/1.0",
		},
		domain: cfg.Domain,
	}
}

func (a *ACMEClient) Provision(ctx context.Context) (certPEM, keyPEM []byte, err error) {
	if err := os.MkdirAll(a.config.DataDir, 0750); err != nil {
		return nil, nil, fmt.Errorf("mkdir: %w", err)
	}

	// Check existing cached certs
	certFile := filepath.Join(a.config.DataDir, "fullchain.pem")
	keyFile := filepath.Join(a.config.DataDir, "privkey.pem")
	if cached, err := loadCachedCerts(certFile, keyFile); err == nil && len(cached.certPEM) > 0 {
		return cached.certPEM, cached.keyPEM, nil
	}

	// Generate account key
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("account key: %w", err)
	}
	a.client.Key = accountKey

	// Register account (accept TOS)
	acct := &acme.Account{Contact: []string{"mailto:" + a.config.Email}}
	if _, err := a.client.Register(ctx, acct, acme.AcceptTOS); err != nil && err != acme.ErrAccountAlreadyExists {
		return nil, nil, fmt.Errorf("acme register: %w", err)
	}

	// Create order
	order, err := a.client.AuthorizeOrder(ctx, acme.DomainIDs(a.domain))
	if err != nil {
		return nil, nil, fmt.Errorf("authorize order: %w", err)
	}

	// Fulfill authorizations via HTTP-01
	for _, authzURL := range order.AuthzURLs {
		az, err := a.client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, nil, fmt.Errorf("get authz: %w", err)
		}

		// Find http-01 challenge
		var chal *acme.Challenge
		for _, c := range az.Challenges {
			if c.Type == "http-01" {
				chal = c
				break
			}
		}
		if chal == nil {
			return nil, nil, fmt.Errorf("no http-01 challenge for %s", az.Identifier.Value)
		}

		// Prepare key authorization
		keyAuth, err := a.client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, nil, fmt.Errorf("http01 response: %w", err)
		}

		// Start temporary HTTP server to serve the challenge
		stopCh := make(chan struct{})
		serverErr := make(chan error, 1)
		srv := a.startChallengeServer(chal.Token, keyAuth, stopCh, serverErr)

		// Accept challenge
		if _, err := a.client.Accept(ctx, chal); err != nil {
			close(stopCh)
			srv.Close()
			return nil, nil, fmt.Errorf("accept challenge: %w", err)
		}

		// Wait for authorization
		if _, err := a.client.WaitAuthorization(ctx, authzURL); err != nil {
			close(stopCh)
			srv.Close()
			return nil, nil, fmt.Errorf("wait authz: %w", err)
		}

		// Stop challenge server
		close(stopCh)
		srv.Close()
		// Give server time to shutdown
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for order to be ready
	order, err = a.client.WaitOrder(ctx, order.URI)
	if err != nil {
		return nil, nil, fmt.Errorf("wait order: %w", err)
	}

	// Generate CSR
	certKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("cert key: %w", err)
	}

	csr, err := a.createCSR(certKey, a.domain)
	if err != nil {
		return nil, nil, fmt.Errorf("csr: %w", err)
	}

	// Request certificate
	derCerts, _, err := a.client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, nil, fmt.Errorf("create cert: %w", err)
	}

	// Encode PEM
	for _, der := range derCerts {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}

	keyBytes := x509.MarshalPKCS1PrivateKey(certKey)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})

	// Cache certs; a cache write failure is non-fatal (the fresh cert is
	// already being returned) but losing the cache silently costs an extra
	// order next boot — surface it.
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		slog.Warn("acme: failed to cache certificate", "file", certFile, "err", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		slog.Warn("acme: failed to cache key", "file", keyFile, "err", err)
	}

	return certPEM, keyPEM, nil
}

func (a *ACMEClient) startChallengeServer(token, keyAuth string, stopCh chan struct{}, errCh chan error) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/acme-challenge/"+token, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(keyAuth)) // client-side disconnect is not fatal for the challenge answer
	})

	srv := &http.Server{
		Handler:           mux,
		Addr:              fmt.Sprintf(":%d", a.config.Port),
		// The challenge server is internet-facing for the duration of one
		// ACME validation; without timeouts a slow client can hold the
		// connection (Slowloris) for the whole session.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-stopCh
		srv.Close()
	}()

	go func() {
		listener, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			errCh <- fmt.Errorf("challenge listen: %w", err)
			return
		}
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("challenge serve: %w", err)
		}
	}()

	return srv
}

func (a *ACMEClient) createCSR(key *rsa.PrivateKey, domain string) ([]byte, error) {
	tpl := &x509.CertificateRequest{
		DNSNames: []string{domain},
	}
	return x509.CreateCertificateRequest(rand.Reader, tpl, key)
}

type cachedCerts struct {
	certPEM []byte
	keyPEM  []byte
}

func loadCachedCerts(certFile, keyFile string) (*cachedCerts, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}

	// Check expiration
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("invalid cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("cert expired")
	}

	return &cachedCerts{certPEM: certPEM, keyPEM: keyPEM}, nil
}
