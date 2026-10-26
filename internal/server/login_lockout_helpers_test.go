package server

import (
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
)

// TestServerAccountLockoutHelpers covers the thin wrappers the login path uses:
// a nil tracker never locks, and the account state round-trips through
// record → check → clear.
func TestServerAccountLockoutHelpers(t *testing.T) {
	// A server without a tracker must never report a lock (fail-open here is
	// correct: a missing tracker is a test/background server, and refusing every
	// login would be worse than no lockout).
	bare := &Server{}
	if locked, retry := bare.checkAccountLockout("alice"); locked || retry != 0 {
		t.Fatalf("nil tracker = (%v, %d)", locked, retry)
	}
	if locked, retry := bare.recordAccountLoginFailure("alice", "10.0.0.1"); locked || retry != 0 {
		t.Fatalf("nil tracker record = (%v, %d)", locked, retry)
	}
	if locked, retry := bare.checkLoginLockout("10.0.0.1"); locked || retry != 0 {
		t.Fatalf("nil tracker ip = (%v, %d)", locked, retry)
	}
	if locked, retry := bare.recordLoginFailure("10.0.0.1", "alice"); locked || retry != 0 {
		t.Fatalf("nil tracker ip record = (%v, %d)", locked, retry)
	}

	// A real tracker with two-source failures locks the account, and clearing
	// it unlocks.
	s := mustTenantServer(t)
	s.loginLockout = newLoginLockoutTracker()
	s.cfg = config.DefaultConfig()
	s.cfg.RateLimit.Login.MaxAttempts = 2
	s.cfg.RateLimit.Login.Window = 300
	s.cfg.RateLimit.Login.LockoutTime = 600
	s.cfg.RateLimit.Login.Whitelist = nil

	if locked, _ := s.recordAccountLoginFailure("bob", "10.0.0.1"); locked {
		t.Fatal("first account failure locked")
	}
	if locked, _ := s.recordAccountLoginFailure("bob", "10.0.0.2"); !locked {
		t.Fatal("distributed account failures did not lock")
	}
	if locked, retry := s.checkAccountLockout("bob"); !locked || retry <= 0 {
		t.Fatalf("checkAccountLockout = (%v, %d)", locked, retry)
	}
	s.loginLockout.resetAccount("bob")
	if locked, _ := s.checkAccountLockout("bob"); locked {
		t.Fatal("resetAccount did not clear the lock")
	}

	// The whitelist bypasses the per-IP lockout entirely.
	s.cfg.RateLimit.Login.Whitelist = []string{"10.9.9.9"}
	for i := 0; i < 5; i++ {
		if locked, _ := s.recordLoginFailure("10.9.9.9", "bob"); locked {
			t.Fatal("a whitelisted IP was locked")
		}
	}
	if locked, _ := s.checkLoginLockout("10.9.9.9"); locked {
		t.Fatal("checkLoginLockout locked a whitelisted IP")
	}
	// A non-whitelisted IP still locks.
	var locked bool
	for i := 0; i < 4 && !locked; i++ {
		locked, _ = s.recordLoginFailure("10.0.0.7", "bob")
	}
	if !locked {
		t.Fatal("a non-whitelisted IP never locked")
	}
	if locked, retry := s.checkLoginLockout("10.0.0.7"); !locked || retry <= 0 {
		t.Fatalf("checkLoginLockout = (%v, %d)", locked, retry)
	}
	s.clearLoginLockout("10.0.0.7")
	if locked, _ := s.checkLoginLockout("10.0.0.7"); locked {
		t.Fatal("clearLoginLockout did not clear")
	}
	_ = time.Now()
}
