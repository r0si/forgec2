package server

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

// TestFileChainRejectsReplay pins the property the chain exists for: the same chunk
// cannot be accepted twice. Without it a relay could duplicate a chunk (or a
// retransmission could be replayed) and the receiver would happily write it again.
func TestFileChainRejectsReplay(t *testing.T) {
	s := testServerForChain(t)
	chainKey := buildChainKey(t, s, "agent-replay")
	chunk := []byte("the same bytes, twice")

	mac := linkMAC(t, chainKey, make([]byte, 32), chunk)
	if err := s.verifyAndCommitChain("agent-replay", 21, mac, chunk); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	// The chain has advanced, so replaying the same link must fail.
	if err := s.verifyAndCommitChain("agent-replay", 21, mac, chunk); err == nil {
		t.Fatal("a replayed chunk was accepted")
	}
	// A failure POISONS the transfer: even a well-formed fresh first link is refused,
	// because a replay must not be able to reset its own way back in.
	fresh := linkMAC(t, chainKey, make([]byte, 32), []byte("new transfer"))
	if err := s.verifyAndCommitChain("agent-replay", 21, fresh, []byte("new transfer")); err == nil {
		t.Fatal("a poisoned transfer accepted a fresh first link without an explicit reset")
	}
	// The task lifecycle resets it (a new upload task), and then it works.
	s.fileChains.reset(21)
	if err := s.verifyAndCommitChain("agent-replay", 21, fresh, []byte("new transfer")); err != nil {
		t.Fatalf("an explicitly reset transfer was refused: %v", err)
	}
}

// TestFileChainRejectsStrippedMACMidTransfer pins the downgrade guard: an agent that
// stops sending MACs mid-transfer (or a relay stripping them) must be refused, while a
// purely legacy transfer that never used MACs keeps working.
func TestFileChainRejectsStrippedMACMidTransfer(t *testing.T) {
	s := testServerForChain(t)
	chainKey := buildChainKey(t, s, "agent-downgrade")
	chunk := []byte("legit first chunk")
	mac := linkMAC(t, chainKey, make([]byte, 32), chunk)
	if err := s.verifyAndCommitChain("agent-downgrade", 31, mac, chunk); err != nil {
		t.Fatal(err)
	}

	// MAC stripped after a chained chunk: refused.
	if err := s.verifyAndCommitChain("agent-downgrade", 31, "", []byte("stripped")); err == nil {
		t.Fatal("a stripped MAC mid-transfer was accepted")
	}

	// A transfer that never used a MAC is still allowed (legacy agents), on its own
	// task id so the previous rejection does not colour it.
	if err := s.verifyAndCommitChain("agent-downgrade", 32, "", []byte("legacy")); err != nil {
		t.Fatalf("a legacy unchained chunk was refused: %v", err)
	}
}

// TestFileChainRejectsMalformedMAC keeps a bad hex value from being treated as a
// legacy chunk: it is a failure, and it resets the transfer.
func TestFileChainRejectsMalformedMAC(t *testing.T) {
	s := testServerForChain(t)
	chainKey := buildChainKey(t, s, "agent-malformed")
	chunk := []byte("payload")
	mac := linkMAC(t, chainKey, make([]byte, 32), chunk)
	if err := s.verifyAndCommitChain("agent-malformed", 41, mac, chunk); err != nil {
		t.Fatal(err)
	}

	for i, bad := range []string{"zz", "00", hex.EncodeToString(make([]byte, 31))} {
		if err := s.verifyAndCommitChain("agent-malformed", 41, bad, chunk); err == nil {
			t.Fatalf("malformed MAC %q was accepted", bad)
		}
		// Each malformed attempt poisons the transfer; reset for the next case so the
		// test exercises the malformed-MAC check rather than the poison guard.
		s.fileChains.reset(41)
		_ = i
	}
}

// TestFileChainConcurrentDuplicateOnlyOneWins pins the lock around check+commit: two
// goroutines presenting the SAME expected link must not both succeed, or a duplicate
// chunk would be written twice.
func TestFileChainConcurrentDuplicateOnlyOneWins(t *testing.T) {
	s := testServerForChain(t)
	chainKey := buildChainKey(t, s, "agent-race")
	chunk := []byte("racy chunk")
	mac := linkMAC(t, chainKey, make([]byte, 32), chunk)

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.verifyAndCommitChain("agent-race", 51, mac, chunk); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("%d goroutines accepted the same chain link, want exactly 1", successes)
	}
}

// TestFileChainSweepExpiresIdleTransfers pins the eviction: an abandoned transfer must
// not keep its chain state forever, and an expired entry must behave like a fresh
// transfer (a zero-prev link).
func TestFileChainSweepExpiresIdleTransfers(t *testing.T) {
	s := testServerForChain(t)
	chainKey := buildChainKey(t, s, "agent-sweep")
	chunk := []byte("chunk")
	mac := linkMAC(t, chainKey, make([]byte, 32), chunk)
	if err := s.verifyAndCommitChain("agent-sweep", 61, mac, chunk); err != nil {
		t.Fatal(err)
	}

	fc := s.fileChains
	// Pretend the entry has been idle past the TTL.
	fc.mu.Lock()
	fc.touched[61] = time.Now().Add(-2 * StaleMapCleanupAge)
	fc.mu.Unlock()

	if n := fc.sweep(StaleMapCleanupAge); n != 1 {
		t.Fatalf("sweep removed %d entries, want 1", n)
	}
	if _, ok := fc.chains[61]; ok {
		t.Fatal("the chain survived the sweep")
	}

	// After expiry the same first link is valid again, as for a new transfer.
	if err := s.verifyAndCommitChain("agent-sweep", 61, mac, chunk); err != nil {
		t.Fatalf("a fresh transfer was refused after expiry: %v", err)
	}
}
