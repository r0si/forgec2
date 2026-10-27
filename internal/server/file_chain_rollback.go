package server

import "time"

// rollbackChainLink restores the link that a verified chunk replaced, so a LOCAL failure
// (disk full, permission denied, invalid offset) does not advance the server's chain.
//
// Without this the server believed the chunk had been stored, the agent's next link then
// mismatched, and - because a mismatch poisons the transfer - one transient local error
// locked the whole upload and forced the operator to start over.
func (s *Server) rollbackChainLink(taskID uint, prev []byte) {
	fc := s.fileChains
	if fc == nil {
		return
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(prev) == 0 {
		delete(fc.chains, taskID)
	} else {
		fc.chains[taskID] = prev
	}
	// A local failure is not tampering: clear any poison so the retry can proceed.
	delete(fc.broken, taskID)
	fc.touched[taskID] = time.Now()
}

// chainLinkFor returns the current link for a task (nil when it has none). The chunk
// handlers call it before verifying so they can roll back on a local failure.
func (s *Server) chainLinkFor(taskID uint) []byte {
	fc := s.fileChains
	if fc == nil {
		return nil
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	prev := fc.chains[taskID]
	if prev == nil {
		return nil
	}
	out := make([]byte, len(prev))
	copy(out, prev)
	return out
}
