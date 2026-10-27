package server

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// ── Agent enrollment (registration + per-beacon row update) ───────────────

// blockedReasonSuffix appends a parenthesized reason to audit details without
// producing dangling parentheses when the reason is blank. The block/unblock
// routes that used to own this helper had no UI entry point, but the
// check-in rejection below still reads Implant.BlockedReason, so the helper
// lives with its only remaining consumer.
func blockedReasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " (" + reason + ")"
}

// processAgentRegistration enrolls or refreshes an implant row.
//
// The returned bool reports whether the agent is NEW; it is NOT a success flag.
// A refusal (blocked implant, ghost registration, failed write) returns a
// zero-value implant with an empty ID, which is what the beacon path checks.
// Keeping the two signals apart matters: treating false as "rejected" would
// drop every returning agent's beacon.
func (s *Server) processAgentRegistration(req beaconRequest, publicIP string, now time.Time) (db.Implant, bool) {
	req.Info = sanitizeInfo(req.Info)
	// C + Go implants send hostname/username/ip base64 with encoding=base64.
	// Decode once so both create and update paths store plaintext (previously
	// the update path stored raw base64 because only the create path decoded).
	applyDecodedBeaconIdentity(req.Info)
	parseInt := func(key string) int {
		if req.Info == nil {
			return 0
		}
		if v, err := strconv.Atoi(req.Info[key]); err == nil {
			return v
		}
		return 0
	}

	var agent db.Implant
	// Unscoped so a previously soft-deleted agent is still found; otherwise a
	// returning agent would be treated as new and hit a primary-key conflict on
	// Create, permanently locking it out of re-registration.
	result := s.db.Unscoped().Where("id = ?", req.UUID).First(&agent)
	isNewAgent := result.Error == gorm.ErrRecordNotFound

	if !isNewAgent && agent.Blocked {
		// Force-offline: refuse all service to this implant. Returning no
		// agent makes the caller emit a well-formed but taskless reply, so the
		// wire protocol is unaffected while nothing is delivered or accepted.
		// Unlike soft-delete, a blocked row is never restored by beaconing;
		// every attempt lands in the audit trail for forensics.
		if uerr := s.db.Model(&db.Implant{}).Where("id = ?", agent.ID).Update("status", "offline").Error; uerr != nil {
			slog.Error("Failed to pin blocked agent offline", "agent_id", agent.ID, "error", uerr)
		}
		s.LogAuditRecord(nil, "blocked_agent_checkin", "agent", agent.ID,
			"blocked implant attempted check-in"+blockedReasonSuffix(agent.BlockedReason), false, nil)
		slog.Debug("Rejected blocked agent check-in", "agent_id", agent.ID, "reason", agent.BlockedReason)
		return db.Implant{}, false
	}
	if !isNewAgent && agent.DeletedAt.Valid {
		// Agent was removed from the UI but is beaconing again: restore the
		// tombstone (un-delete) rather than leaving a row that blocks re-registration.
		if uerr := s.db.Unscoped().Model(&db.Implant{}).Where("id = ?", agent.ID).Update("deleted_at", nil).Error; uerr != nil {
			slog.Error("Failed to restore soft-deleted agent", "agent_id", agent.ID, "error", uerr)
		} else {
			agent.DeletedAt = gorm.DeletedAt{}
			slog.Info("Restored soft-deleted agent on re-beacon", "agent_id", agent.ID)
		}
	}

	if isNewAgent {
		hostname, username, ip := decodeBeaconIdentity(req.Info)
		if strings.TrimSpace(hostname) == "" && strings.TrimSpace(ip) == "" {
			slog.Warn("Rejected ghost agent registration", "agent_id", req.UUID, "public_ip", publicIP)
			return db.Implant{}, false
		}

		// Tenant attribution: when the implant's v3 registration secret was
		// minted by a specific tenant, that secret defines ownership even on
		// the HTTP enrollment path (which itself presents no secret id).
		// Legacy agents without a bound secret keep the bootstrap default.
		tenant := s.regSecretTenantForAgent(req.UUID)
		if tenant == 0 {
			tenant = s.defaultTenantID()
		}
		agent = db.Implant{
			ID:              req.UUID,
			TenantID:        tenant,
			Hostname:        hostname,
			Username:        username,
			OS:              req.Info["os"],
			Arch:            req.Info["arch"],
			IP:              ip,
			PublicIP:        publicIP,
			LastSeen:        now,
			Status:          "online",
			ProtocolVersion: req.ProtocolVersion,
			Version:         req.Info["version"],
			PID:             parseInt("pid"),
			ProcessName:     req.Info["process_name"],
			Integrity:       req.Info["integrity"],
			Elevated:        req.Info["elevated"] == "true",
			Domain:          req.Info["domain"],
			CurrentInterval: parseInt("interval"),
			CurrentJitter:   parseInt("jitter"),
			ActiveWindow:    req.Info["active_window"],
		}
		if v, ok := req.Info["env_threat_score"]; ok {
			if score, err := strconv.Atoi(v); err == nil && score >= 0 && score <= 100 {
				agent.EnvThreatScore = score
			}
		}
		if v, ok := req.Info["env_honeypot"]; ok {
			agent.EnvHoneypot = v == "true"
		}
		if v, ok := req.Info["env_class"]; ok {
			agent.EnvClass = v
		}
		if lid := req.Info["listener_id"]; lid != "" {
			if id, err := strconv.ParseUint(lid, 10, 32); err == nil {
				agent.ListenerID = uint(id)
			}
		}
		if err := s.db.Create(&agent).Error; err != nil {
			// Concurrent first check-in for the same UUID: the other goroutine's
			// Create won the primary-key race. Re-read the now-existing row and
			// continue as an update path instead of dropping this beacon.
			slog.Debug("Agent create raced, re-reading existing row", "agent_id", agent.ID, "error", err)
			if rerr := s.db.Unscoped().Where("id = ?", req.UUID).First(&agent).Error; rerr != nil {
				slog.Error("Failed to create agent", "agent_id", agent.ID, "error", err)
				return db.Implant{}, false
			}
			return agent, false
		}
		slog.Info("New agent registered", "agent_id", agent.ID, "hostname", agent.Hostname, "ip", agent.IP, "listener_id", agent.ListenerID)
		s.broadcastAgentOnline(agent, true)
		s.recordAgentStatusEvent(agent.ID, "online")
		// Feed the SIEM correlator: a brand-new agent is a first-class signal.
		if s.siem != nil {
			s.siem.Send(SIEMEvent{
				Timestamp: now,
				Action:    "implant_checkin",
				Resource:  "beacon",
				AgentID:   agent.ID,
				IP:        agent.IP,
				Success:   true,
				Details:   "New agent registered",
				Hostname:  agent.Hostname,
			})
		}
		s.eventManager.Emit(Event{
			Type:      EventImplantCheckin,
			AgentID:   agent.ID,
			AgentHost: agent.Hostname,
			Timestamp: now,
			Data:      map[string]interface{}{"new": true, "ip": agent.IP},
		})
		// Plugin connect hook fires here (and on reconnect below) — never on
		// steady-state beacons, which previously forked a plugin process per
		// beacon per plugin (log/exec storm, beacon latency).
		s.fireAgentConnectHook(agent, true, now)
	} else {
		prevStatus := s.agentStatus(agent).Status
		if agent.Status == "offline" || agent.Status == "stale" {
			prevStatus = agent.Status
		}

		updates := map[string]interface{}{
			"last_seen": now,
			"status":    "online",
		}
		if publicIP != "" && publicIP != agent.PublicIP {
			updates["public_ip"] = publicIP
		}
		if v := req.Info["version"]; v != "" {
			updates["version"] = v
		}
		if v := req.Info["process_name"]; v != "" {
			updates["process_name"] = v
		}
		if v := req.Info["integrity"]; v != "" {
			updates["integrity"] = v
		}
		if v := req.Info["domain"]; v != "" {
			updates["domain"] = v
		}
		if v := req.Info["elevated"]; v != "" {
			nowElevated := v == "true"
			// Feed the SIEM correlator on a privilege escalation transition.
			if nowElevated && !agent.Elevated && s.siem != nil {
				s.siem.Send(SIEMEvent{
					Timestamp: now,
					Action:    "agent_elevated",
					Resource:  "beacon",
					AgentID:   agent.ID,
					IP:        agent.IP,
					Success:   true,
					Details:   "Agent privilege escalation detected",
					Hostname:  agent.Hostname,
				})
			}
			updates["elevated"] = nowElevated
		}
		if pid := parseInt("pid"); pid > 0 {
			updates["pid"] = pid
		}
		if interval := parseInt("interval"); interval >= 1 && interval <= 86400 {
			updates["current_interval"] = interval
		}
		if jitter := parseInt("jitter"); jitter >= 0 && jitter <= 100 {
			updates["current_jitter"] = jitter
		}
		if req.Info != nil {
			if v, ok := req.Info["active_window"]; ok {
				updates["active_window"] = v
			}
		}
		// Re-assert stable identity attributes reported by the agent. They are
		// set at registration, but a stageless build, a clone, or a partial
		// first beacon can leave them empty forever if we never refresh them.
		// Refresh whenever a non-empty value arrives; never overwrite a populated
		// value with an empty one.
		if v := req.Info["hostname"]; v != "" {
			updates["hostname"] = v
		}
		if v := req.Info["username"]; v != "" {
			updates["username"] = v
		}
		if v := req.Info["os"]; v != "" {
			updates["os"] = v
		}
		if v := req.Info["arch"]; v != "" {
			updates["arch"] = v
		}
		if v := req.Info["ip"]; v != "" && v != agent.IP {
			updates["ip"] = v
		}
		if lid := req.Info["listener_id"]; lid != "" {
			if id, err := strconv.ParseUint(lid, 10, 32); err == nil && agent.ListenerID == 0 {
				updates["listener_id"] = uint(id)
			}
		}
		// Environment threat data from agent self-assessment
		if v, ok := req.Info["env_threat_score"]; ok {
			if score, err := strconv.Atoi(v); err == nil && score >= 0 && score <= 100 {
				updates["env_threat_score"] = score
			}
		}
		if v, ok := req.Info["env_honeypot"]; ok {
			updates["env_honeypot"] = v == "true"
		}
		if v, ok := req.Info["env_class"]; ok {
			updates["env_class"] = v
		}
		// Protocol version is merged into the single registration update so
		// regular beacons do not issue an extra UPDATE per check-in; it only
		// writes when the value actually changes.
		if req.ProtocolVersion > 0 && agent.ProtocolVersion != req.ProtocolVersion {
			updates["protocol_version"] = req.ProtocolVersion
		}

		// Self-heal legacy rows that predate multi-tenant isolation (tenant_id=0
		// placeholders created by ensureBeaconImplantRow before the fix). Without
		// this, an agent registered while the server was running is invisible to
		// its tenant via tenantScope and never shows up in the UI. A bound v3
		// secret with a minting tenant names the correct team; only agents
		// without one fall to the bootstrap default.
		if agent.TenantID == 0 {
			if tenant := s.regSecretTenantForAgent(req.UUID); tenant > 0 {
				updates["tenant_id"] = tenant
			} else {
				updates["tenant_id"] = s.defaultTenantID()
			}
		}

		// Atomic update: only update if last_seen hasn't been changed by a concurrent beacon.
		// Unscoped so the update still applies if the row was just restored from a
		// soft-delete tombstone (deleted_at not yet cleared in this transaction's view).
		// Busy-retried: this runs on every beacon against the single writer.
		updateErr := s.withBusyRetry("enroll", func() error {
			return s.db.Transaction(func(tx *gorm.DB) error {
				txResult := tx.Unscoped().Model(&db.Implant{}).Where("id = ? AND last_seen <= ?", agent.ID, agent.LastSeen).Updates(updates)
				if txResult.Error != nil {
					return txResult.Error
				}
				if txResult.RowsAffected == 0 {
					slog.Warn("Concurrent beacon update conflict, retrying", "agent_id", agent.ID)
				}
				// Re-read to get consistent state
				return tx.Where("id = ?", agent.ID).First(&agent).Error
			})
		})
		if updateErr != nil {
			slog.Error("Failed to update agent", "agent_id", agent.ID, "error", updateErr)
			return agent, false
		}
		agent.LastSeen = now
		agent.Status = "online"

		if prevStatus != "online" {
			s.broadcastAgentOnline(agent, false)
			s.recordAgentStatusEvent(agent.ID, "online")
			s.eventManager.Emit(Event{
				Type:      EventImplantCheckin,
				AgentID:   agent.ID,
				AgentHost: agent.Hostname,
				Timestamp: now,
				Data:      map[string]interface{}{"new": false, "reconnected": true, "prev_status": prevStatus, "ip": agent.IP},
			})
			s.fireAgentConnectHook(agent, false, now)
			// Status transitions stay at Info; the steady-state check-in does
			// not. The beacon path runs once per implant per sleep window, so
			// an unconditional Info line here floods the log at ~5k lines/s on
			// a single loaded core and buries the transition operators
			// actually read. The audit/event rows above still record it.
			slog.Info("Beacon processed", "agent_id", req.UUID, "last_seen", now, "status", "online", "prev_status", prevStatus)
		} else {
			slog.Debug("Beacon processed", "agent_id", req.UUID, "last_seen", now, "status", "online", "prev_status", prevStatus)
		}
	}

	return agent, isNewAgent
}
