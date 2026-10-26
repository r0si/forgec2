package server

import (
	"testing"
	"time"
)

// TestRecordAccountFailureSingleSourceDoesNotLock pins the anti-DoS rule: all
// failures from ONE source IP must never lock the account. The per-IP lockout
// already throttles that attacker, and letting a single source lock a username
// would be a trivial denial of service against any operator.
func TestRecordAccountFailureSingleSourceDoesNotLock(t *testing.T) {
	tr := newLoginLockoutTracker()
	now := time.Now()

	for i := 0; i < 10; i++ {
		locked, retry := tr.recordAccountFailure("alice", "10.0.0.1", 3, 300, 900, now)
		if locked {
			t.Fatalf("attempt %d from a single IP locked the account (retry %d)", i+1, retry)
		}
	}
}

// TestRecordAccountFailureDistributedLocks covers the case the feature exists
// for: the same username failing from two different addresses locks it.
func TestRecordAccountFailureDistributedLocks(t *testing.T) {
	tr := newLoginLockoutTracker()
	now := time.Now()

	if locked, _ := tr.recordAccountFailure("bob", "10.0.0.1", 3, 300, 900, now); locked {
		t.Fatal("first failure locked the account")
	}
	if locked, _ := tr.recordAccountFailure("bob", "10.0.0.2", 3, 300, 900, now); locked {
		t.Fatal("second failure locked the account too early")
	}
	locked, retry := tr.recordAccountFailure("bob", "10.0.0.1", 3, 300, 900, now)
	if !locked {
		t.Fatal("distributed failures did not lock the account")
	}
	if retry != 900 {
		t.Fatalf("retry after = %d, want the lockout window 900", retry)
	}

	// While locked, further failures report the lock with a positive retry.
	locked, retry = tr.recordAccountFailure("bob", "10.0.0.3", 3, 300, 900, now.Add(10*time.Second))
	if !locked || retry <= 0 || retry > 900 {
		t.Fatalf("locked re-check = (%v, %d)", locked, retry)
	}

	// Once the lockout expires the account is usable again.
	locked, _ = tr.recordAccountFailure("bob", "10.0.0.1", 3, 300, 900, now.Add(901*time.Second))
	if locked {
		t.Fatal("the account stayed locked past its lockout window")
	}
}

// TestRecordAccountFailureWindowRollover covers the sliding window: failures
// spread across more than the window do not accumulate.
func TestRecordAccountFailureWindowRollover(t *testing.T) {
	tr := newLoginLockoutTracker()
	now := time.Now()

	// Two failures from two IPs, then wait past the window: the counter resets.
	if locked, _ := tr.recordAccountFailure("carol", "10.0.0.1", 3, 60, 600, now); locked {
		t.Fatal("first failure locked")
	}
	if locked, _ := tr.recordAccountFailure("carol", "10.0.0.2", 3, 60, 600, now); locked {
		t.Fatal("second failure locked")
	}
	later := now.Add(2 * time.Minute)
	if locked, _ := tr.recordAccountFailure("carol", "10.0.0.3", 3, 60, 600, later); locked {
		t.Fatal("failures older than the window still counted toward the lock")
	}
}

// TestRecordAccountFailureEntryCap proves the account map is bounded: past
// maxLockoutEntries it evicts instead of growing without limit.
func TestRecordAccountFailureEntryCap(t *testing.T) {
	tr := newLoginLockoutTracker()
	now := time.Now()

	for i := 0; i <= maxLockoutEntries; i++ {
		tr.recordAccountFailure("user-"+jsonNumber(uint(i)), "10.0.0.1", 3, 300, 900, now)
	}

	tr.mu.Lock()
	size := len(tr.accountEntries)
	tr.mu.Unlock()
	if size > maxLockoutEntries {
		t.Fatalf("account lockout map grew to %d entries, cap is %d", size, maxLockoutEntries)
	}
}

// TestLoginLockoutTrackerIPPath covers the per-IP half of the tracker: the same
// IP failing repeatedly is locked with a positive retry, and clearing it makes
// the IP usable again.
func TestLoginLockoutTrackerIPPath(t *testing.T) {
	tr := newLoginLockoutTracker()
	now := time.Now()

	var locked bool
	var retry int
	for i := 0; i < 4; i++ {
		locked, retry = tr.recordFailure("10.9.9.9", 3, 300, 120, now)
	}
	if !locked || retry != 120 {
		t.Fatalf("per-IP lockout = (%v, %d), want locked with 120s", locked, retry)
	}
	if locked, _ := tr.isLocked("10.9.9.9", now); !locked {
		t.Fatal("a locked IP was reported as unlocked")
	}
	tr.reset("10.9.9.9")
	if locked, _ := tr.isLocked("10.9.9.9", now); locked {
		t.Fatal("a cleared IP stayed locked")
	}
}
