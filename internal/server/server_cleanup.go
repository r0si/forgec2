package server

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"gorm.io/gorm"
)

// ── Retention cleanup + agent status thresholds ───────────────────────────

// auditRetentionDays resolves the forensic retention: default 365, floor 90
// (shorter windows would silently destroy the tamper chain investigators
// rely on). Zero value (bare test configs) means default.
func (s *Server) auditRetentionDays() int {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	v := DefaultAuditRetentionDays
	if s.cfg != nil {
		v = s.cfg.Server.AuditRetentionDays
		if v <= 0 {
			v = DefaultAuditRetentionDays
		}
	}
	if v < MinAuditRetentionDays {
		v = MinAuditRetentionDays
	}
	return v
}

// taskRetentionDays floors terminal-task auto-deletion at 90 days so a
// default 30-day ops retention cannot eat forensic task history.
func (s *Server) taskRetentionDays() int {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	v := s.cfg.Server.CleanupRetentionDays
	if v < 1 {
		v = DefaultCleanupRetentionDays
	}
	if v < MinTaskRetentionDays {
		v = MinTaskRetentionDays
	}
	return v
}

// cleanupRetentionDays reads the ops retention window under the config lock
// (the settings save mutates it). Zero/negative falls back to the default.
func (s *Server) cleanupRetentionDays() int {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	v := s.cfg.Server.CleanupRetentionDays
	if v < 1 {
		return DefaultCleanupRetentionDays
	}
	return v
}

// cleanupOldData removes old completed/failed tasks and old files (screenshots + uploads) to prevent bloat
func (s *Server) cleanupOldData() {
	retention := s.cleanupRetentionDays()
	cutoff := time.Now().AddDate(0, 0, -retention)

	// delete old tasks (floored at 90 days regardless of ops retention).
	// cancelled joins completed/failed: it is terminal and otherwise grows
	// forever. sent joins too as a backstop for legacy zombie rows.
	taskCutoff := time.Now().AddDate(0, 0, -s.taskRetentionDays())
	if err := s.db.WithContext(s.ctx).Where("created_at < ? AND status IN ?", taskCutoff, []string{"completed", "failed", "cancelled", "sent"}).Delete(&db.Task{}).Error; err != nil {
		slog.Error("Cleanup tasks failed", "err", err)
	}

	// Forensic timelines outlive operational data: audit/opsec/circuit events
	// use the separate (longer, floored) audit retention.
	auditCutoff := time.Now().AddDate(0, 0, -s.auditRetentionDays())
	for _, table := range []string{"audit_logs", "opsec_history", "circuit_breaker_events"} {
		if err := s.db.WithContext(s.ctx).Table(table).Where("created_at < ?", auditCutoff).Delete(nil).Error; err != nil {
			slog.Error("Cleanup forensic table failed", "table", table, "err", err)
		}
	}

	// The credential-usage ledger is append-only and had no retention at all:
	// every spray/lateral/verify wrote a row forever, and the vault page read
	// the whole table to compute lifecycle labels. Purged on the forensic
	// clock rather than the ops one — deleting evidence of credential use
	// sooner than the audit trail that records the operator would be a
	// forensic regression. CredentialLifecycle only needs the newest row per
	// credential, which survives any cutoff shorter than the engagement.
	if err := s.db.WithContext(s.ctx).Where("created_at < ?", auditCutoff).Delete(&db.CredentialUsage{}).Error; err != nil {
		slog.Error("Cleanup credential usages failed", "err", err)
	}

	// delete old system metrics (monitoring persists one row every 30s; without
	// retention the table grows ~2.9k rows/day and bloats the SQLite database)
	if err := s.db.WithContext(s.ctx).Where("created_at < ?", cutoff).Delete(&db.SystemMetric{}).Error; err != nil {
		slog.Error("Cleanup system metrics failed", "err", err)
	}

	// Agent status flaps (online/stale/offline) are append-only and would grow
	// without bound on a flapping fleet; same retention as other telemetry.
	if err := s.db.WithContext(s.ctx).Where("timestamp < ?", cutoff).Delete(&db.AgentStatusEvent{}).Error; err != nil {
		slog.Error("Cleanup agent status events failed", "err", err)
	}

	// Periodic SQLite maintenance: VACUUM and ANALYZE to prevent bloat and
	// keep query planner statistics fresh. Only runs if the DB is SQLite.
	s.configMu.RLock()
	dbDriver := s.cfg.Database.Driver
	s.configMu.RUnlock()
	if sqlDB, err := s.db.DB(); err == nil {
		if err := sqlDB.Ping(); err == nil {
			dbName := dbDriver
			if dbName == "" || dbName == "sqlite" {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					if _, err := sqlDB.Exec("PRAGMA optimize"); err != nil {
						slog.Debug("SQLite ANALYZE failed", "err", err)
					}
					// The startup block sets auto_vacuum=INCREMENTAL, which
					// queues freed pages instead of returning them to the OS;
					// without this call the .db file grows monotonically with
					// task-result churn (encrypted Result/Error blobs,
					// credential re-encryption on key rotation).
					if _, err := sqlDB.Exec("PRAGMA incremental_vacuum"); err != nil {
						slog.Debug("SQLite incremental_vacuum failed", "err", err)
					}
				}()
			}
		}
	}

	s.cleanupGhostAgents()

	s.configMu.RLock()
	dataDir := s.cfg.Server.DataDir
	s.configMu.RUnlock()
	if dataDir == "" {
		dataDir = "data"
	}

	// Clean old screenshots
	s.cleanOldFiles(filepath.Join(dataDir, "screenshots"), cutoff)

	// Clean old uploads (exfil files)
	s.cleanOldFiles(filepath.Join(dataDir, "uploads"), cutoff)

	// Clean old agent binaries
	s.cleanOldFiles(filepath.Join(dataDir, "agents"), cutoff)

	s.cleanupStaleMapEntries()

	slog.Info("Old data cleanup completed")
}

func (s *Server) cleanupStaleMapEntries() {
	cutoff := time.Now().Add(-StaleMapCleanupAge)
	cleaned := 0

	// Clean stale agentStatusCooldown entries
	s.agentStatusCooldownMu.Lock()
	for k, v := range s.agentStatusCooldown {
		if v.Before(cutoff) {
			delete(s.agentStatusCooldown, k)
			cleaned++
		}
	}
	s.agentStatusCooldownMu.Unlock()

	// Clean stale screenMonitorImplants entries
	s.screenMonitorMu.Lock()
	for k, v := range s.screenMonitorImplants {
		if v.Before(cutoff) {
			delete(s.screenMonitorImplants, k)
			cleaned++
		}
	}
	s.screenMonitorMu.Unlock()

	// Live frames are kept in memory only as an HTTP fallback for consoles
	// whose WebSocket briefly reconnects. Bound their lifetime with the same
	// stale-map cleanup used by monitor registrations.
	s.screenFrameMu.Lock()
	for k, frame := range s.screenFrames {
		if frame.capturedAt.Before(cutoff) {
			delete(s.screenFrames, k)
			cleaned++
		}
	}
	s.screenFrameMu.Unlock()

	// Clean empty extC2TaskQueue entries. (Non-empty queues are bounded
	// per-channel by MaxExtC2QueuePerChan and keyed by live channel IDs, so
	// the key space can't grow without bound — contents are left alone so a
	// reconnecting channel never loses queued tasks.)
	s.extC2TaskMu.Lock()
	for k, v := range s.extC2TaskQueue {
		if len(v) == 0 {
			delete(s.extC2TaskQueue, k)
			cleaned++
		}
	}
	s.extC2TaskMu.Unlock()

	// Clean expired phishing landing rate-limiter windows (keyed token+IP;
	// attackers can mint unbounded keys, so anything past the window goes).
	s.landingLimiterMu.Lock()
	if s.landingLimiterSince != nil {
		winCutoff := time.Now().Add(-phishingLandingWindow)
		for k, since := range s.landingLimiterSince {
			if since.Before(winCutoff) {
				delete(s.landingLimiterHits, k)
				delete(s.landingLimiterSince, k)
				cleaned++
			}
		}
	}
	s.landingLimiterMu.Unlock()

	// Clean stale traffic-profile auto-adapt timestamps.
	s.autoAdaptMu.Lock()
	if s.autoAdaptLast != nil {
		for k, ts := range s.autoAdaptLast {
			if ts.Before(cutoff) {
				delete(s.autoAdaptLast, k)
				cleaned++
			}
		}
	}
	s.autoAdaptMu.Unlock()

	// Clean stale nav-stats cache snapshots (keyed by tenant; entries carry
	// their own timestamp and are cheaply recomputed on next request).
	s.navStatsCacheMu.Lock()
	if s.navStatsCache != nil {
		for k, e := range s.navStatsCache {
			if e.at.Before(cutoff) {
				delete(s.navStatsCache, k)
				cleaned++
			}
		}
	}
	s.navStatsCacheMu.Unlock()

	// Drop abandoned file-transfer chain links (agent died mid-transfer).
	if s.fileChains != nil {
		cleaned += s.fileChains.sweep(StaleMapCleanupAge)
	}

	// Expired remote-input limiter windows. Keys are attacker/operator chosen
	// (user id + agent id), so without a sweep a hostile client could leak one
	// map entry per agent id forever.
	cleaned += s.remoteInputLimiter.sweep(cutoff)

	if cleaned > 0 {
		slog.Info("Cleaned stale map entries", "count", cleaned)
	}
}

// offlineThresholdWarnGap throttles the invalid-threshold warning. The getter
// runs on every agent list, dashboard poll, metrics scrape and status
// recompute, so an unthrottled Warn emitted one line per call — a
// misconfigured server drowned in its own diagnostic.
const offlineThresholdWarnGap = time.Minute

// offlineThresholdSecLocked reads the configured threshold. The caller MUST
// hold s.configMu (read or write). Keeping the raw read separate from the
// locking wrappers means the composed getters (staleThreshold,
// offlineThresholdFor) take the lock exactly once: nested RLock calls would
// deadlock the moment a writer queued between the two acquisitions.
func (s *Server) offlineThresholdSecLocked() int { // config-lock: caller holds the read or write lock
	if s.cfg == nil {
		return DefaultOfflineThresholdSec
	}
	d := s.cfg.Server.OfflineThreshold
	if d < 1 {
		// Zero value works on bare test servers; CAS keeps exactly one writer
		// warning per gap across concurrent beacons and polls.
		now := time.Now().UnixNano()
		last := s.offlineWarnAt.Load()
		if now-last >= int64(offlineThresholdWarnGap) && s.offlineWarnAt.CompareAndSwap(last, now) {
			slog.Warn("OfflineThreshold is invalid, using default", "configured", d, "default", DefaultOfflineThresholdSec)
		}
		d = DefaultOfflineThresholdSec
	}
	return d
}

func (s *Server) offlineThreshold() time.Duration {
	s.configMu.RLock()
	d := s.offlineThresholdSecLocked()
	s.configMu.RUnlock()
	return time.Duration(d) * time.Second
}

func (s *Server) staleThreshold() time.Duration {
	s.configMu.RLock()
	d := s.offlineThresholdSecLocked()
	s.configMu.RUnlock()
	// The multiplier applies to the threshold in SECONDS, not to the bare int:
	// `time.Duration(d) * StaleThresholdMultiplier` yielded nanoseconds, which
	// made the "stale" band a 180-nanosecond window and classified every agent
	// older than the offline threshold as offline instead.
	return time.Duration(d) * StaleThresholdMultiplier * time.Second
}

// offlineThresholdFor scales the static offline threshold by the agent's own
// sleep interval: an agent that beacons every 5 minutes must not flap
// online/stale/offline between check-ins. Floor is the static threshold so
// chatty agents keep tight detection.
func (s *Server) offlineThresholdFor(a db.Implant) time.Duration {
	s.configMu.RLock()
	base := time.Duration(s.offlineThresholdSecLocked()) * time.Second
	s.configMu.RUnlock()
	if a.CurrentInterval > 0 {
		// 3x interval covers the beacon plus jitter and one missed cycle.
		if d := 3 * time.Duration(a.CurrentInterval) * time.Second; d > base {
			return d
		}
	}
	return base
}

func (s *Server) staleThresholdFor(a db.Implant) time.Duration {
	return s.offlineThresholdFor(a) * StaleThresholdMultiplier
}

// AgentStatusInfo holds display info for an agent's status
type AgentStatusInfo struct {
	Status    string // "online", "stale", "offline"
	Label     string // "Online", "Timeout", "Offline"
	DotColor  string // tailwind bg class
	BgColor   string // tailwind bg class
	TextColor string // tailwind text class
	Anim      string // animate-pulse or empty
}

func (s *Server) agentStatus(a db.Implant) AgentStatusInfo {
	since := time.Since(a.LastSeen)
	threshold := s.offlineThresholdFor(a)
	switch {
	case since < threshold:
		return AgentStatusInfo{"online", "Online", "bg-emerald-500", "bg-emerald-50", "text-emerald-700", "animate-pulse"}
	case since < s.staleThresholdFor(a):
		return AgentStatusInfo{"stale", "Timeout", "bg-amber-500", "bg-amber-50", "text-amber-700", ""}
	default:
		return AgentStatusInfo{"offline", "Offline", "bg-red-500", "bg-red-50", "text-red-700", ""}
	}
}

// cleanupGhostAgents removes invalid or long-dead implant records.
func (s *Server) cleanupGhostAgents() {
	ghostCutoff := time.Now().Add(-GhostAgentCutoff)
	var ghosts []db.Implant
	if err := s.db.Where("(hostname = '' OR hostname IS NULL) AND (ip = '' OR ip IS NULL) AND last_seen < ?", ghostCutoff).Limit(500).Find(&ghosts).Error; err != nil {
		return
	}
	if len(ghosts) > 0 {
		ghostIDs := make([]string, len(ghosts))
		for i, a := range ghosts {
			ghostIDs[i] = a.ID
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("agent_id IN ?", ghostIDs).Delete(&db.Task{}).Error; err != nil {
				return err
			}
			// Soft delete (tombstone): the row keeps id/hostname/last_seen/
			// listener_id for later forensics and lost-agent recovery.
			// Hard deletes previously destroyed the only record of weak
			// assets the moment they went quiet.
			return tx.Where("id IN ?", ghostIDs).Delete(&db.Implant{}).Error
		}); err != nil {
			slog.Error("Failed to remove ghost agents", "err", err)
		} else {
			slog.Info("Removed ghost agents", "count", len(ghosts))
		}
	}

	retention := s.cleanupRetentionDays()
	offlineCutoff := time.Now().AddDate(0, 0, -retention)
	var stale []db.Implant
	if err := s.db.Where("last_seen < ?", offlineCutoff).Limit(500).Find(&stale).Error; err != nil {
		return
	}
	if len(stale) > 0 {
		idStrs := make([]string, len(stale))
		for i, a := range stale {
			idStrs[i] = a.ID
		}
		slog.Info("Removing stale offline agents", "count", len(stale), "age_days", retention)
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("agent_id IN ?", idStrs).Delete(&db.Task{}).Error; err != nil {
				return err
			}
			// Soft delete (tombstone), same rationale as ghosts above.
			return tx.Where("id IN ?", idStrs).Delete(&db.Implant{}).Error
		}); err != nil {
			slog.Error("Failed to remove stale agents", "err", err)
		}
	}
}

// cleanOldFiles recursively removes files older than cutoff in the given dir
func (s *Server) cleanOldFiles(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("Failed to read cleanup directory", "dir", dir, "err", err)
		}
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			s.cleanOldFiles(path, cutoff)
			remaining, _ := os.ReadDir(path)
			if len(remaining) == 0 {
				if err := os.Remove(path); err != nil {
					slog.Debug("Failed to remove empty directory", "path", path, "err", err)
				}
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				slog.Debug("Failed to remove old file", "path", path, "err", err)
			}
		}
	}
}
