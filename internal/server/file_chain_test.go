package server

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
)

// testServerForChain builds a minimal Server with a known master beacon key so
// per-agent file chain keys can be derived.
func testServerForChain(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.BeaconKey = strings.Repeat("ab", 32) // 64 hex chars = 32 bytes
	return &Server{
		db:         testutil.SetupTestDB(t),
		cfg:        cfg,
		fileChains: newFileChainState(),
	}
}

// buildChainKey mimics the agent: deriveFileChainKey(regKey).
func buildChainKey(t *testing.T, s *Server, agentID string) []byte {
	t.Helper()
	key := s.fileChainKey(agentID)
	if key == nil {
		t.Fatalf("fileChainKey(%q) returned nil", agentID)
	}
	return key
}

func linkMAC(t *testing.T, key, prev []byte, data []byte) string {
	t.Helper()
	mac := crypto.FileChunkMAC(key, prev, data)
	if mac == nil {
		t.Fatal("FileChunkMAC returned nil")
	}
	return hex.EncodeToString(mac)
}

func TestFileChainVerifyAndCommit(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	chainKey := buildChainKey(t, s, "agent-1")
	chunk1 := []byte("first chunk payload")
	chunk2 := []byte("second chunk payload")

	// First link: chain is seeded with 32 zero bytes.
	mac1 := linkMAC(t, chainKey, make([]byte, 32), chunk1)
	if err := s.verifyAndCommitChain("agent-1", 11, mac1, chunk1); err != nil {
		t.Fatalf("verify chunk1: %v", err)
	}

	// Second link must chain from mac1.
	mac2 := linkMAC(t, chainKey, mustHex(t, mac1), chunk2)
	if err := s.verifyAndCommitChain("agent-1", 11, mac2, chunk2); err != nil {
		t.Fatalf("verify chunk2: %v", err)
	}

	// Tampered chunk (data changed, MAC from different data) must fail.
	if err := s.verifyAndCommitChain("agent-1", 11, mac2, []byte("tampered!!")); err == nil {
		t.Fatal("tampered chunk accepted")
	}

	// A chunk computed with the wrong previous link must fail (reordering).
	wrongPrev := linkMAC(t, chainKey, mustHex(t, mac2), chunk2)
	if err := s.verifyAndCommitChain("agent-1", 12, wrongPrev, chunk2); err == nil {
		t.Fatal("reordered/desynced chunk accepted")
	}

	// Malformed MAC hex must fail.
	if err := s.verifyAndCommitChain("agent-1", 13, "zz-not-hex", chunk1); err == nil {
		t.Fatal("malformed MAC accepted")
	}
}

func TestFileChainMissingMACAllowed(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	// Legacy agents / non-chunk tasks send no MAC — verification is a no-op.
	if err := s.verifyAndCommitChain("agent-1", 21, "", []byte("legacy chunk")); err != nil {
		t.Fatalf("empty MAC should pass, got: %v", err)
	}
}

func TestFileChainForPush(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	chainKey := buildChainKey(t, s, "agent-2")
	chunk1 := []byte("push chunk one")
	chunk2 := []byte("push chunk two")

	prev1, mac1, err := s.chainForPush("agent-2", 31, chunk1)
	if err != nil {
		t.Fatalf("chainForPush chunk1: %v", err)
	}
	if prev1 != hex.EncodeToString(make([]byte, 32)) {
		t.Errorf("first prev = %q, want zero seed", prev1)
	}
	if mac1 != linkMAC(t, chainKey, make([]byte, 32), chunk1) {
		t.Error("chunk1 MAC mismatch")
	}

	// Second push must chain from mac1.
	prev2, mac2, err := s.chainForPush("agent-2", 31, chunk2)
	if err != nil {
		t.Fatalf("chainForPush chunk2: %v", err)
	}
	if prev2 != mac1 {
		t.Errorf("second prev = %q, want mac1 %q", prev2, mac1)
	}
	if mac2 != linkMAC(t, chainKey, mustHex(t, mac1), chunk2) {
		t.Error("chunk2 MAC mismatch")
	}
}

func TestFileChainTaskIsolation(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	// Different tasks must not share chain state (both start from the seed).
	chunk := []byte("same bytes")
	macA := linkMAC(t, buildChainKey(t, s, "agent-3"), make([]byte, 32), chunk)
	macB := linkMAC(t, buildChainKey(t, s, "agent-3"), make([]byte, 32), chunk)
	if err := s.verifyAndCommitChain("agent-3", 41, macA, chunk); err != nil {
		t.Fatalf("task A: %v", err)
	}
	if err := s.verifyAndCommitChain("agent-3", 42, macB, chunk); err != nil {
		t.Fatalf("task B should verify independently: %v", err)
	}
}

// TestFileChainDowngradeRejected proves a MAC stripped mid-transfer fails
// closed: once a chained link exists, an empty-MAC chunk is a downgrade
// (compromised relay parent dodging HMAC), not legacy tolerance.
func TestFileChainDowngradeRejected(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	chainKey := buildChainKey(t, s, "agent-9")
	chunk1 := []byte("chained first chunk")
	mac1 := linkMAC(t, chainKey, make([]byte, 32), chunk1)
	if err := s.verifyAndCommitChain("agent-9", 91, mac1, chunk1); err != nil {
		t.Fatalf("verify chunk1: %v", err)
	}
	if err := s.verifyAndCommitChain("agent-9", 91, "", []byte("stripped chunk")); err == nil {
		t.Fatal("stripped MAC mid-transfer must be rejected")
	}
	// A stripped MAC is an active downgrade, not a glitch: the transfer is POISONED,
	// so a retry is refused until the task lifecycle resets it. Resetting on failure
	// (the previous behaviour) let the very same chunk through again.
	macRetry := linkMAC(t, chainKey, make([]byte, 32), chunk1)
	if err := s.verifyAndCommitChain("agent-9", 91, macRetry, chunk1); err == nil {
		t.Fatal("a poisoned transfer accepted a retry without an explicit reset")
	}
	s.fileChains.reset(91)
	if err := s.verifyAndCommitChain("agent-9", 91, macRetry, chunk1); err != nil {
		t.Fatalf("an explicitly reset transfer should restart from the seed: %v", err)
	}
}

// TestFileChainTouchedRefreshed proves both commit paths refresh the sweep
// timestamp: push chains and verified chunks must expire when abandoned,
// not leak map entries forever.
func TestFileChainTouchedRefreshed(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	if _, _, err := s.chainForPush("agent-2", 77, []byte("push")); err != nil {
		t.Fatalf("chainForPush: %v", err)
	}
	chainKey := buildChainKey(t, s, "agent-2")
	mac := linkMAC(t, chainKey, make([]byte, 32), []byte("verify me"))
	if err := s.verifyAndCommitChain("agent-2", 78, mac, []byte("verify me")); err != nil {
		t.Fatalf("verify: %v", err)
	}
	s.fileChains.mu.Lock()
	_, pushTouched := s.fileChains.touched[77]
	_, verifyTouched := s.fileChains.touched[78]
	s.fileChains.mu.Unlock()
	if !pushTouched || !verifyTouched {
		t.Fatalf("sweep timestamps missing: push=%v verify=%v", pushTouched, verifyTouched)
	}
	// Aged entries must sweep away entirely (no chains-without-touched).
	s.fileChains.mu.Lock()
	old := time.Now().Add(-time.Hour)
	s.fileChains.touched[77] = old
	s.fileChains.touched[78] = old
	s.fileChains.mu.Unlock()
	if n := s.fileChains.sweep(time.Minute); n != 2 {
		t.Fatalf("sweep evicted %d, want 2", n)
	}
}

func seedRelayChild(t *testing.T, s *Server, childID, parentID, taskType string) uint {
	t.Helper()
	if err := s.db.Create(&db.Implant{ID: childID, ParentID: parentID, Status: "online"}).Error; err != nil {
		t.Fatalf("seed child: %v", err)
	}
	task := db.Task{AgentID: childID, Type: taskType, Status: "pending"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task.ID
}

func relayResult(taskID uint, chunk []byte, macHex string) taskResult {
	return taskResult{
		TaskID:   taskID,
		Type:     "upload",
		Output:   base64.StdEncoding.EncodeToString(chunk),
		Encoding: "base64",
		MAC:      macHex,
	}
}

func TestRelayedFileChunkChain(t *testing.T) {
	chunk := []byte("relayed exfil chunk")

	t.Run("valid child MAC applies", func(t *testing.T) {
		s := testServerForChain(t)
		defer s.db.DB()
		taskID := seedRelayChild(t, s, "child-a", "parent-a", "upload")
		mac := linkMAC(t, buildChainKey(t, s, "child-a"), make([]byte, 32), chunk)
		s.processRelayedResults([]relayedData{{
			AgentID: "child-a",
			Results: []taskResult{relayResult(taskID, chunk, mac)},
		}}, "parent-a", time.Now())
		var task db.Task
		if err := s.db.First(&task, taskID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if task.Status != "completed" {
			t.Fatalf("valid relayed chunk not applied: status=%q", task.Status)
		}
	})

	t.Run("forged MAC dropped", func(t *testing.T) {
		s := testServerForChain(t)
		defer s.db.DB()
		taskID := seedRelayChild(t, s, "child-b", "parent-b", "upload")
		// MAC computed over different bytes (or a parent-held key): must fail.
		bad := linkMAC(t, buildChainKey(t, s, "child-b"), make([]byte, 32), []byte("something else"))
		s.processRelayedResults([]relayedData{{
			AgentID: "child-b",
			Results: []taskResult{relayResult(taskID, chunk, bad)},
		}}, "parent-b", time.Now())
		var task db.Task
		if err := s.db.First(&task, taskID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if task.Status != "pending" {
			t.Fatalf("forged relayed chunk applied: status=%q", task.Status)
		}
	})

	t.Run("empty MAC tolerated like direct path", func(t *testing.T) {
		s := testServerForChain(t)
		defer s.db.DB()
		taskID := seedRelayChild(t, s, "child-c", "parent-c", "upload")
		s.processRelayedResults([]relayedData{{
			AgentID: "child-c",
			Results: []taskResult{relayResult(taskID, chunk, "")},
		}}, "parent-c", time.Now())
		var task db.Task
		if err := s.db.First(&task, taskID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if task.Status != "completed" {
			t.Fatalf("legacy empty-MAC relayed chunk not applied: status=%q", task.Status)
		}
	})
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestFileChainKeyStability(t *testing.T) {
	s := testServerForChain(t)
	defer s.db.DB()

	k1 := s.fileChainKey("agent-9")
	k2 := s.fileChainKey("agent-9")
	if k1 == nil || k2 == nil || hex.EncodeToString(k1) != hex.EncodeToString(k2) {
		t.Fatal("file chain key must be deterministic per agent")
	}
	k3 := s.fileChainKey("agent-8")
	if k3 == nil || strings.EqualFold(hex.EncodeToString(k1), hex.EncodeToString(k3)) {
		t.Fatal("different agents must have different chain keys")
	}
}
