package server

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
)

// File-transfer integrity chain. Every chunked file movement (agent->server
// exfil, server->agent push) is covered by an HMAC-SHA256 chain seeded with 32
// zero bytes: mac[i] = HMAC(chainKey, mac[i-1] || chunk[i]). Any missing,
// reordered, or tampered chunk breaks the chain and is rejected before the
// chunk is committed to disk. The chain key is derived per-implant from its
// registration key (crypto.DeriveFileChainKey), so it is unique per agent and
// unguessable by a MITM that does not hold the reg key.

const (
	// MaxTransferChunkSize caps a single file-transfer chunk (upload push,
	// download pull, exfil upload result) on both sides.
	MaxTransferChunkSize = 4 << 20 // 4 MiB

	// MaxReadFileSize caps the whole-file "read" task (agents refuse larger
	// files and tell the operator to use the chunked download task instead).
	MaxReadFileSize = 64 << 20 // 64 MiB

	// MaxURLDownloadSize caps an agent-side HTTP download (download_url).
	MaxURLDownloadSize = 256 << 20 // 256 MiB
)

// fileChainState tracks the last committed MAC of the transfer chain per task
// (in-memory; a server restart mid-transfer simply breaks the chain and the
// operator re-runs the transfer).
type fileChainState struct {
	mu      sync.Mutex
	chains  map[uint][]byte
	touched map[uint]time.Time
	// broken records transfers that failed a link check. Resetting the chain to zero on
	// failure was not enough: a replay (or a reordered chunk) failed, the failure
	// zeroed the state, and the SAME chunk then matched again - so a duplicate could be
	// accepted repeatedly. A poisoned transfer stays refused until the task lifecycle
	// calls reset (a new upload task), and the sweep expires the poison with the rest.
	broken map[uint]time.Time
}

func newFileChainState() *fileChainState {
	return &fileChainState{
		chains:  make(map[uint][]byte),
		touched: make(map[uint]time.Time),
		broken:  make(map[uint]time.Time),
	}
}

// sweep drops chain links idle longer than ttl (abandoned mid-transfer when
// an agent dies). Returns the evicted count for logging.
func (fc *fileChainState) sweep(ttl time.Duration) int {
	if fc == nil {
		return 0
	}
	cutoff := time.Now().Add(-ttl)
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n := 0
	for id, ts := range fc.touched {
		if ts.Before(cutoff) {
			delete(fc.chains, id)
			delete(fc.touched, id)
			delete(fc.broken, id)
			n++
		}
	}
	return n
}

// reset drops chain state for a task (task errored or completed).
func (fc *fileChainState) reset(taskID uint) { // clears the link AND the poisoned mark
	if fc == nil {
		return
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	delete(fc.chains, taskID)
	delete(fc.touched, taskID)
	delete(fc.broken, taskID)
}

// fileChainKey returns the HMAC chain key for an implant (derived from its
// registration key). Returns nil when the reg key cannot be derived.
func (s *Server) fileChainKey(agentID string) []byte {
	return crypto.DeriveFileChainKey(s.deriveRegKey(agentID))
}

// fileChainNote counts a chain integrity event for observability.
func (s *Server) fileChainNote(outcome string, taskID uint, agentID string) {
	if s.metrics != nil && s.metrics.FileChainEventsTotal != nil {
		s.metrics.FileChainEventsTotal.WithLabelValues(outcome).Inc()
	}
}

// verifyAndCommitChain checks chunkData against the expected chain link. On
// success it commits the new link and returns nil. On any failure (bad hex,
// wrong MAC) the chain is reset so the transfer cannot silently continue.
// The check+commit is performed under a single lock to prevent concurrent
// chunks for the same task from interleaving and bypassing the chain.
func (s *Server) verifyAndCommitChain(agentID string, taskID uint, expectedHex string, chunkData []byte) error {
	fc := s.fileChains
	if fc == nil {
		return fmt.Errorf("file chain state not initialized")
	}
	chainKey := s.fileChainKey(agentID)

	// EVERYTHING below runs under one lock: the poison check, the legacy downgrade
	// check, the comparison and the commit. Splitting them let a goroutine pass the
	// poison check, wait on the lock while another chunk failed and zeroed the chain,
	// and then match the zero-prev link - four of eight concurrent duplicates were
	// accepted that way.
	fc.mu.Lock()
	defer fc.mu.Unlock()

	if _, poisoned := fc.broken[taskID]; poisoned {
		s.fileChainNote("poisoned_rejected", taskID, agentID)
		return fmt.Errorf("file transfer is locked after an integrity failure")
	}

	if expectedHex == "" {
		// Legacy agents that don't compute MACs: allow an empty MAC only while the
		// transfer has no chained state. Once a MAC'd chunk committed, a stripped chunk
		// is a downgrade (a compromised relay parent dropping MACs to dodge HMAC) and
		// fails closed.
		if _, chained := fc.chains[taskID]; chained {
			s.fileChainNote("downgrade_rejected", taskID, agentID)
			slog.Warn("File chunk MAC stripped mid-transfer, rejecting", "agent_id", agentID, "task_id", taskID)
			delete(fc.chains, taskID)
			delete(fc.touched, taskID)
			fc.broken[taskID] = time.Now()
			return fmt.Errorf("chain MAC stripped mid-transfer")
		}
		s.fileChainNote("legacy_bypass", taskID, agentID)
		return nil
	}

	want, err := hex.DecodeString(expectedHex)
	if err != nil || len(want) != 32 {
		s.poisonTransferLocked(fc, taskID)
		s.fileChainNote("mismatch", taskID, agentID)
		return fmt.Errorf("malformed chain MAC")
	}
	if chainKey == nil {
		s.poisonTransferLocked(fc, taskID)
		s.fileChainNote("mismatch", taskID, agentID)
		return fmt.Errorf("cannot derive file chain key")
	}

	prev := fc.chains[taskID]
	if prev == nil {
		prev = make([]byte, 32)
	}
	got := crypto.FileChunkMAC(chainKey, prev, chunkData)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		s.poisonTransferLocked(fc, taskID)
		s.fileChainNote("mismatch", taskID, agentID)
		return fmt.Errorf("chunk HMAC mismatch (tampered/reordered chunk)")
	}
	fc.chains[taskID] = want
	fc.touched[taskID] = time.Now()
	return nil
}

// poisonTransferLocked marks a transfer unusable until the task lifecycle resets it.
// Caller holds fc.mu.
func (s *Server) poisonTransferLocked(fc *fileChainState, taskID uint) {
	delete(fc.chains, taskID)
	delete(fc.touched, taskID)
	fc.broken[taskID] = time.Now()
}

// chainForPush computes the chain link the agent must verify for a push-upload
// chunk and records the new link. Returns the prev + expected MAC hex values.
// The prev read + commit is atomic to prevent reordering bypass.
func (s *Server) chainForPush(agentID string, taskID uint, chunkData []byte) (prevHex, macHex string, err error) {
	chainKey := s.fileChainKey(agentID)
	if chainKey == nil {
		return "", "", fmt.Errorf("cannot derive file chain key")
	}
	fc := s.fileChains
	if fc == nil {
		return "", "", fmt.Errorf("file chain state not initialized")
	}
	fc.mu.Lock()
	prev := fc.chains[taskID]
	if prev == nil {
		prev = make([]byte, 32)
	}
	mac := crypto.FileChunkMAC(chainKey, prev, chunkData)
	fc.chains[taskID] = mac
	// Push legs must refresh touched too, or the sweep never expires them
	// (the old code only set chains — a slow drip of pushes leaked map
	// entries forever).
	fc.touched[taskID] = time.Now()
	fc.mu.Unlock()
	return hex.EncodeToString(prev), hex.EncodeToString(mac), nil
}
