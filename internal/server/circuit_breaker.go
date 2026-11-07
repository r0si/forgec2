package server

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/config"
)

type ListenerHealth int

const (
	HealthUnknown  ListenerHealth = 0
	HealthHealthy  ListenerHealth = 1
	HealthUnstable ListenerHealth = 2
	HealthBurned   ListenerHealth = 3
)

type ProbeTarget struct {
	ID     string
	Scheme string
	Host   string
	Port   int
}

type ProbeResult struct {
	Target       ProbeTarget
	TCPOK        bool
	TLSOK        bool
	HTTPStatus   int
	ResponseTime time.Duration
	Burned       bool
	Timestamp    time.Time
	Error        string
	// CertFingerprint is the SHA-256 hex of the leaf certificate presented
	// by an https/tls target ("" when no certificate was captured). Probes
	// intentionally skip chain verification (self-signed redirectors are
	// normal), so this fingerprint is the operator's only signal to notice
	// a MITM or an unexpected cert rotation on a redirector.
	CertFingerprint string
}

type CircuitBreaker struct {
	mu            sync.RWMutex
	targets       map[string]*TargetHealth
	vantagePoints []string // external proxy URLs for probing
	checkInterval time.Duration
	config        *config.Config
	onBurned      func(targetID string) // callback to rotate agents
	stopCh        chan struct{}
	done          chan struct{} // closed when probeLoop exits
	stopOnce      sync.Once
}

type TargetHealth struct {
	Target           ProbeTarget
	Status           ListenerHealth
	LastProbe        time.Time
	ConsecutiveFails int
	Results          []ProbeResult // ring buffer, keep last 10
	FailReasons      []string
}

var (
	circuitBreaker     *CircuitBreaker
	circuitBreakerOnce sync.Once
)

func NewCircuitBreaker(cfg *config.Config) *CircuitBreaker {
	circuitBreakerOnce.Do(func() {
		cb := &CircuitBreaker{
			targets:       make(map[string]*TargetHealth),
			vantagePoints: cfg.Server.VantagePoints,
			checkInterval: CBCheckInterval,
			config:        cfg,
			onBurned:      nil,
			stopCh:        make(chan struct{}),
		}
		circuitBreaker = cb
	})
	return circuitBreaker
}

func GetCircuitBreaker() *CircuitBreaker {
	return circuitBreaker
}

func (cb *CircuitBreaker) RegisterTarget(id, scheme, host string, port int) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.targets[id] = &TargetHealth{
		Target: ProbeTarget{ID: id, Scheme: scheme, Host: host, Port: port},
		Status: HealthUnknown,
	}
}

func (cb *CircuitBreaker) UnregisterTarget(id string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	delete(cb.targets, id)
}

func (cb *CircuitBreaker) Start() {
	cb.done = make(chan struct{})
	go func() {
		defer close(cb.done)
		cb.probeLoop()
	}()
}

func (cb *CircuitBreaker) Stop() {
	cb.stopOnce.Do(func() {
		close(cb.stopCh)
		if cb.done != nil {
			<-cb.done
		}
	})
}

func (cb *CircuitBreaker) probeLoop() {
	for {
		cb.probeAll()
		select {
		case <-cb.stopCh:
			return
		case <-time.After(cb.checkInterval):
		}
	}
}

func (cb *CircuitBreaker) probeAll() {
	cb.mu.RLock()
	targets := make([]*TargetHealth, 0, len(cb.targets))
	for _, th := range cb.targets {
		targets = append(targets, th)
	}
	cb.mu.RUnlock()

	for _, th := range targets {
		result := cb.probeTarget(th.Target)
		cb.recordResult(th.Target.ID, result)
	}
}

func (cb *CircuitBreaker) probeTarget(target ProbeTarget) ProbeResult {
	start := time.Now()
	result := ProbeResult{
		Target:    target,
		Timestamp: time.Now(),
	}

	// Reachability probing only: TLS certificate verification is skipped
	// on purpose (IP-address and self-signed-redirector hosts are normal).
	// Plain HTTP targets get a plain TCP dial; only https/tls targets get a
	// TLS handshake, so an HTTP-only endpoint is never flagged as burned.
	var conn net.Conn
	var err error
	switch target.Scheme {
	case "https", "tls":
		// #nosec G402 -- availability probe, not a secret-bearing session; the expired-leaf check below still applies.
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: CBDialTimeout}, "tcp", fmt.Sprintf("%s:%d", target.Host, target.Port), &tls.Config{InsecureSkipVerify: true}) // #nosec G402
	default:
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("%s:%d", target.Host, target.Port), CBDialTimeout)
	}
	if err != nil {
		result.Error = fmt.Sprintf("TCP dial failed: %v", err)
		// Burned must describe the probe, not just the HTTP sub-check below:
		// a refused/unreachable transport is the most severe failure there is,
		// and this path used to return before the flag was ever set.
		result.Burned = true
		result.ResponseTime = time.Since(start)
		return result
	}
	conn.Close()
	result.TCPOK = true
	if target.Scheme == "https" || target.Scheme == "tls" {
		result.TLSOK = true
		// Record what the other end presented: with verification skipped,
		// the leaf fingerprint is the only MITM/rotation signal available.
		if tlsConn, ok := conn.(*tls.Conn); ok {
			if state := tlsConn.ConnectionState(); len(state.PeerCertificates) > 0 {
				sum := sha256.Sum256(state.PeerCertificates[0].Raw)
				result.CertFingerprint = hex.EncodeToString(sum[:])
			}
		}
		// TLS handshake completion is the health signal for https/tls;
		// a GET would need a matching certificate to avoid false burns.
		result.ResponseTime = time.Since(start)
		return result
	}

	// HTTP check (plain http only)
	if target.Scheme == "http" {
		url := fmt.Sprintf("http://%s:%d/health", target.Host, target.Port)
		client := &http.Client{Timeout: CBHealthCheckTimeout}
		resp, err := client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			result.HTTPStatus = resp.StatusCode
		} else {
			result.Error = fmt.Sprintf("HTTP check failed: %v", err)
		}
	}

	result.ResponseTime = time.Since(start)

	// Detect burn: TCP RST or unexpected response patterns
	if result.Error != "" {
		result.Burned = true
	}

	return result
}

func (cb *CircuitBreaker) recordResult(targetID string, result ProbeResult) {
	// Burn notification is edge-triggered. The handler rotates every agent on
	// the listener and rewrites the redirector's status row, so firing it on
	// each further probe while the target stays burned churned agents and
	// re-broadcast the same event once per check interval until an operator
	// reset — the probe loop is the only thing that ever clears a burn.
	var notifyBurned bool

	cb.mu.Lock()

	th, ok := cb.targets[targetID]
	if !ok {
		cb.mu.Unlock()
		return
	}

	// Keep last 10 results
	th.Results = append(th.Results, result)
	if len(th.Results) > 10 {
		th.Results = th.Results[len(th.Results)-10:]
	}

	th.LastProbe = time.Now()

	if result.Error != "" {
		th.ConsecutiveFails++
		th.FailReasons = append(th.FailReasons, result.Error)
		if len(th.FailReasons) > 5 {
			th.FailReasons = th.FailReasons[len(th.FailReasons)-5:]
		}
	} else {
		th.ConsecutiveFails = 0
		th.FailReasons = nil
	}

	// Determine health status
	previous := th.Status
	switch {
	case th.ConsecutiveFails >= 5:
		th.Status = HealthBurned
		notifyBurned = previous != HealthBurned && cb.onBurned != nil
	case th.ConsecutiveFails >= 1:
		th.Status = HealthUnstable
	default:
		th.Status = HealthHealthy
	}
	handler := cb.onBurned
	cb.mu.Unlock()

	// Outside the critical section: the handler itself queries the breaker
	// and the database, so calling it under the lock would stall every probe.
	if notifyBurned {
		go handler(targetID)
	}
}

func (cb *CircuitBreaker) GetStatus(targetID string) (ListenerHealth, *TargetHealth) {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	th, ok := cb.targets[targetID]
	if !ok {
		return HealthUnknown, nil
	}
	return th.Status, th
}

func (cb *CircuitBreaker) GetAllStatus() map[string]ListenerHealth {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	result := make(map[string]ListenerHealth)
	for id, th := range cb.targets {
		result[id] = th.Status
	}
	return result
}

func (cb *CircuitBreaker) SetOnBurnedHandler(handler func(targetID string)) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.onBurned = handler
}

// circuitBreakerStateName is the single mapping from the in-memory health enum
// to the wire/audit vocabulary. Detail, toggle and reset all report through it
// so an event row can never disagree with the state it claims to describe.
func circuitBreakerStateName(status ListenerHealth) string {
	switch status {
	case HealthHealthy:
		return "healthy"
	case HealthUnstable:
		return "unstable"
	case HealthBurned:
		return "burned"
	default:
		return "unknown"
	}
}
