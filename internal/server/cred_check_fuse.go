package server

import (
	"strings"
	"sync"
	"time"
)

// credentialSeparator is the field separator of the cred_check / password_spray
// wire format. The implant splits the command on it, so a value containing it
// shifts every later field.
const credentialSeparator = '|'

// credentialCommandHasSeparator reports whether any field would break the
// separator-joined command format.
func credentialCommandHasSeparator(fields ...string) bool {
	for _, f := range fields {
		if strings.ContainsRune(f, credentialSeparator) {
			return true
		}
	}
	return false
}

// credCheckFuseMax is the number of consecutive invalid/locked results per
// (agent, domain) allowed before the server refuses to dispatch further
// credential checks. Mirrors the agent-side fuse to protect target accounts
// from lockouts even when agents are re-queued or redeployed.
const credCheckFuseMax = 5

// credCheckFuseTTL bounds how long a failure entry survives without new
// failures. Without it the map grows forever across the server lifetime —
// every (agent, domain) pair that ever tripped stays resident, including
// keys for agents deleted long ago (P3-9). 24h matches the natural cooldown
// an operator would want after a lockout storm anyway.
const credCheckFuseTTL = 24 * time.Hour

type credFuseFailure struct {
	count    int
	lastFail time.Time
}

// credCheckFuseTracker is a process-level consecutive-failure counter keyed by
// agent ID + lowercase domain. A "valid" result resets the counter.
type credCheckFuseTracker struct {
	mu       sync.Mutex
	failures map[string]credFuseFailure
}

func newCredCheckFuseTracker() *credCheckFuseTracker {
	return &credCheckFuseTracker{failures: make(map[string]credFuseFailure)}
}

func credCheckFuseKey(agentID, domain string) string {
	return agentID + "|" + strings.ToLower(strings.TrimSpace(domain))
}

// sweepLocked drops entries that have been failure-only (no reset) for longer
// than the TTL. Caller must hold f.mu.
func (f *credCheckFuseTracker) sweepLocked(now time.Time) {
	for k, v := range f.failures {
		if now.Sub(v.lastFail) > credCheckFuseTTL {
			delete(f.failures, k)
		}
	}
}

func (f *credCheckFuseTracker) tripped(agentID, domain string) bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.sweepLocked(now)
	v, ok := f.failures[credCheckFuseKey(agentID, domain)]
	return ok && v.count >= credCheckFuseMax
}

func (f *credCheckFuseTracker) recordFailure(agentID, domain string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	// Amortized sweep: piggyback on writes so no background ticker needed.
	f.sweepLocked(now)
	key := credCheckFuseKey(agentID, domain)
	v := f.failures[key]
	v.count++
	v.lastFail = now
	f.failures[key] = v
}

func (f *credCheckFuseTracker) reset(agentID, domain string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	delete(f.failures, credCheckFuseKey(agentID, domain))
	f.mu.Unlock()
}
