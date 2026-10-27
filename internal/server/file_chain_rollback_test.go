package server

import (
	"encoding/hex"
	"testing"
)

// TestChainRollbackAfterLocalFailure pins the robustness fix: the chain link is
// committed by verifyAndCommitChain BEFORE the chunk is written, so a local failure
// (disk full, permission denied, invalid offset) used to advance the server's chain while
// the bytes never landed. The agent's next link then mismatched, and because a mismatch
// poisons the transfer, one transient local error locked the whole upload.
func TestChainRollbackAfterLocalFailure(t *testing.T) {
	s := testServerForChain(t)
	key := s.fileChainKey("agent-rollback")

	chunk1 := []byte("first chunk")
	mac1 := linkMAC(t, key, make([]byte, 32), chunk1)
	if err := s.verifyAndCommitChain("agent-rollback", 71, mac1, chunk1); err != nil {
		t.Fatal(err)
	}

	// The second chunk verifies, then the "write" fails.
	chunk2 := []byte("second chunk")
	prev := s.chainLinkFor(71)
	if prev == nil {
		t.Fatal("no link recorded for the first chunk")
	}
	mac2 := linkMAC(t, key, prev, chunk2)
	if err := s.verifyAndCommitChain("agent-rollback", 71, mac2, chunk2); err != nil {
		t.Fatal(err)
	}
	// The handler rolls the link back because the write failed.
	s.rollbackChainLink(71, prev)

	// The agent retries the same chunk against the same predecessor: it must verify.
	again := s.chainLinkFor(71)
	if hex.EncodeToString(again) != hex.EncodeToString(prev) {
		t.Fatalf("link after rollback = %x, want the previous link %x", again, prev)
	}
	if err := s.verifyAndCommitChain("agent-rollback", 71, mac2, chunk2); err != nil {
		t.Fatalf("retry after a local failure was refused: %v", err)
	}
}

// TestRollbackClearsPoison keeps a local failure from leaving the transfer locked: a
// poisoned transfer would otherwise need a brand new task even though nothing was
// tampered with.
func TestRollbackClearsPoison(t *testing.T) {
	s := testServerForChain(t)
	key := s.fileChainKey("agent-rollback-poison")

	chunk := []byte("chunk")
	mac := linkMAC(t, key, make([]byte, 32), chunk)
	if err := s.verifyAndCommitChain("agent-rollback-poison", 72, mac, chunk); err != nil {
		t.Fatal(err)
	}
	// A mismatching chunk poisons the transfer.
	if err := s.verifyAndCommitChain("agent-rollback-poison", 72, mac, []byte("tampered")); err == nil {
		t.Fatal("the mismatching chunk was accepted")
	}
	if err := s.verifyAndCommitChain("agent-rollback-poison", 72, mac, chunk); err == nil {
		t.Fatal("the poisoned transfer accepted a chunk")
	}

	// Rolling back a LOCAL failure clears the poison so the retry proceeds.
	s.rollbackChainLink(72, nil)
	if err := s.verifyAndCommitChain("agent-rollback-poison", 72, mac, chunk); err != nil {
		t.Fatalf("the retry after a rollback was refused: %v", err)
	}
}
